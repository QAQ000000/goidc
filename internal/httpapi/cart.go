package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) cart(w http.ResponseWriter, r *http.Request) {
	// Read the version and items from one snapshot so a refresh cannot mix revisions.
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var cartID string
	var version int64
	if err = tx.QueryRow(r.Context(), "SELECT id::text,version FROM carts WHERE user_id=$1", identityFrom(r).ID).Scan(&cartID, &version); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT ci.id,ci.product_id,ci.quantity,p.name,p.slug,ci.billing_cycle,ci.option_ids,ci.option_ids::text AS option_ids_json,ci.option_quantities::text AS option_quantities_json,ci.purchase_fields,ci.purchase_fields::text AS purchase_fields_json,pp.price_cents AS base_price_cents,pp.setup_fee_cents AS base_setup_cents,
 pp.price_cents+COALESCE((SELECT sum(COALESCE(pop.price_cents,po.price_delta_cents)) FROM product_options po LEFT JOIN product_option_prices pop ON pop.option_id=po.id AND pop.billing_cycle=ci.billing_cycle AND pop.active WHERE po.id=ANY(SELECT jsonb_array_elements_text(ci.option_ids)::bigint)),0) AS price_cents,
 pp.setup_fee_cents+COALESCE((SELECT sum(COALESCE(pop.setup_fee_cents,po.setup_delta_cents)) FROM product_options po LEFT JOIN product_option_prices pop ON pop.option_id=po.id AND pop.billing_cycle=ci.billing_cycle AND pop.active WHERE po.id=ANY(SELECT jsonb_array_elements_text(ci.option_ids)::bigint)),0) AS setup_fee_cents,
 p.stock,(p.active AND pp.active AND catalog_category_visible(p.category_id)) AS active,(ci.product_revision=p.revision) AS current,
 (pp.price_cents+pp.setup_fee_cents+COALESCE((SELECT sum(COALESCE(pop.price_cents,po.price_delta_cents)+COALESCE(pop.setup_fee_cents,po.setup_delta_cents)) FROM product_options po LEFT JOIN product_option_prices pop ON pop.option_id=po.id AND pop.billing_cycle=ci.billing_cycle AND pop.active WHERE po.id=ANY(SELECT jsonb_array_elements_text(ci.option_ids)::bigint)),0))*ci.quantity AS total_cents FROM cart_items ci JOIN products p ON p.id=ci.product_id JOIN product_prices pp ON pp.product_id=ci.product_id AND pp.billing_cycle=ci.billing_cycle WHERE ci.cart_id=$1 ORDER BY ci.id`, cartID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	for _, item := range items {
		var ids []int64
		if err = json.Unmarshal([]byte(item["option_ids_json"].(string)), &ids); err != nil {
			s.fail(w, r, err)
			return
		}
		quantities := map[int64]int64{}
		if err = json.Unmarshal([]byte(item["option_quantities_json"].(string)), &quantities); err != nil {
			s.fail(w, r, err)
			return
		}
		selection, selectionErr := store.ValidateProductOptionsForCycleWithQuantities(r.Context(), tx, item["product_id"].(int64), item["billing_cycle"].(string), ids, quantities)
		if selectionErr == nil {
			price, setup, amountErr := store.OptionAmounts(item["base_price_cents"].(int64), item["base_setup_cents"].(int64), selection)
			selectionErr = amountErr
			if selectionErr == nil {
				item["price_cents"], item["setup_fee_cents"] = price, setup
				item["total_cents"] = (price + setup) * mapInteger(item["quantity"])
			}
		}
		if selectionErr != nil {
			if _, ok := selectionErr.(*store.Conflict); !ok {
				s.fail(w, r, selectionErr)
				return
			}
			item["active"] = false
		}
		item["options_valid"] = selectionErr == nil
		item["config_snapshot"] = selection.Snapshot
		item["option_quantities"] = selection.Quantities
		var fieldValues map[string]string
		if err = json.Unmarshal([]byte(item["purchase_fields_json"].(string)), &fieldValues); err != nil {
			s.fail(w, r, err)
			return
		}
		_, fieldSnapshot, fieldErr := store.ValidatePurchaseFields(r.Context(), tx, item["product_id"].(int64), fieldValues)
		if fieldErr != nil {
			if _, ok := fieldErr.(*store.Conflict); !ok {
				s.fail(w, r, fieldErr)
				return
			}
			item["active"] = false
			item["fields_error"] = fieldErr.Error()
		}
		item["fields_valid"] = fieldErr == nil
		item["purchase_field_snapshot"] = fieldSnapshot
		delete(item, "purchase_fields_json")
		delete(item, "option_ids_json")
		delete(item, "option_quantities_json")
		delete(item, "base_price_cents")
		delete(item, "base_setup_cents")
	}
	var promoCode string
	var promoID int64
	if err = tx.QueryRow(r.Context(), "SELECT promo_code_id,code FROM cart_promo_codes WHERE cart_id=$1", cartID).Scan(&promoID, &promoCode); err != nil && err != pgx.ErrNoRows {
		s.fail(w, r, err)
		return
	}
	var promo any
	var discountCents int64
	if promoCode != "" {
		quote, subtotal, quoteErr := s.db.CartPromoQuote(r.Context(), tx, identityFrom(r).ID, cartID, promoCode, false)
		if quoteErr == nil {
			discountCents = quote.DiscountCents
			promo = map[string]any{"id": promoID, "code": quote.Code, "discount_type": quote.DiscountType, "discount_cents": quote.DiscountCents, "subtotal_cents": subtotal, "total_cents": subtotal - quote.DiscountCents, "recurring": quote.Recurring, "recurring_limit": quote.RecurringLimit}
		} else {
			promo = map[string]any{"id": promoID, "code": promoCode, "discount_cents": 0, "error": quoteErr.Error()}
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"id": cartID, "version": version, "currency": "CNY", "items": items, "promo": promo, "discount_cents": discountCents})
}

func mapInteger(value any) int64 {
	switch n := value.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}

type cartInput struct {
	PurchaseFields   map[string]string `json:"purchase_fields"`
	Version          int64             `json:"version"`
	ProductID        int64             `json:"product_id,omitempty"`
	Quantity         int               `json:"quantity,omitempty"`
	BillingCycle     string            `json:"billing_cycle,omitempty"`
	OptionIDs        []int64           `json:"option_ids,omitempty"`
	OptionQuantities map[int64]int64   `json:"option_quantities,omitempty"`
}

func (s *Server) addCartItem(w http.ResponseWriter, r *http.Request)    { s.mutateCart(w, r, "add") }
func (s *Server) updateCartItem(w http.ResponseWriter, r *http.Request) { s.mutateCart(w, r, "update") }
func (s *Server) removeCartItem(w http.ResponseWriter, r *http.Request) { s.mutateCart(w, r, "delete") }
func (s *Server) mutateCart(w http.ResponseWriter, r *http.Request, action string) {
	var input cartInput
	if !decode(w, r, &input) {
		return
	}
	if input.Version < 1 || (action != "delete" && (input.Quantity < 1 || input.Quantity > 100)) || (action == "add" && input.ProductID < 1) {
		writeError(w, 400, "invalid_cart", "购物车版本、商品或数量无效")
		return
	}
	var itemID int64
	if action != "add" {
		var ok bool
		itemID, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var cartID string
	var version int64
	if err = tx.QueryRow(r.Context(), "SELECT id::text,version FROM carts WHERE user_id=$1 FOR UPDATE", identityFrom(r).ID).Scan(&cartID, &version); err != nil {
		s.fail(w, r, err)
		return
	}
	if version != input.Version {
		writeError(w, 409, "stale_cart", "购物车已更新，请刷新后操作")
		return
	}
	if action == "delete" {
		tag, err := tx.Exec(r.Context(), "DELETE FROM cart_items WHERE id=$1 AND cart_id=$2", itemID, cartID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if tag.RowsAffected() != 1 {
			writeError(w, 404, "not_found", "购物车项目不存在")
			return
		}
	} else {
		if err = store.CatalogReadLock(r.Context(), tx); err != nil {
			s.fail(w, r, err)
			return
		}
		if action == "update" {
			var currentCycle string
			var currentOptionJSON []byte
			var currentQuantityJSON []byte
			var currentFieldJSON []byte
			if err = tx.QueryRow(r.Context(), "SELECT product_id,billing_cycle,option_ids,option_quantities,purchase_fields FROM cart_items WHERE id=$1 AND cart_id=$2", itemID, cartID).Scan(&input.ProductID, &currentCycle, &currentOptionJSON, &currentQuantityJSON, &currentFieldJSON); err != nil {
				s.fail(w, r, err)
				return
			}
			preserveOptions := input.OptionIDs == nil
			if input.PurchaseFields == nil {
				if err = json.Unmarshal(currentFieldJSON, &input.PurchaseFields); err != nil {
					s.fail(w, r, err)
					return
				}
			}
			if input.BillingCycle == "" {
				input.BillingCycle = currentCycle
			}
			if preserveOptions {
				if err = json.Unmarshal(currentOptionJSON, &input.OptionIDs); err != nil {
					s.fail(w, r, err)
					return
				}
			}
			if input.OptionQuantities == nil && preserveOptions {
				if err = json.Unmarshal(currentQuantityJSON, &input.OptionQuantities); err != nil {
					s.fail(w, r, err)
					return
				}
			}
			if input.OptionQuantities == nil {
				input.OptionQuantities = map[int64]int64{}
			}
		}
		var revision int64
		var stock *int
		var active bool
		var defaultCycle string
		if err = tx.QueryRow(r.Context(), "SELECT revision,stock,(active AND catalog_category_visible(category_id)),billing_cycle FROM products WHERE id=$1 FOR SHARE", input.ProductID).Scan(&revision, &stock, &active, &defaultCycle); err != nil {
			s.fail(w, r, err)
			return
		}
		if input.BillingCycle == "" {
			input.BillingCycle = defaultCycle
		}
		var priceActive bool
		var price, setup int64
		if err = tx.QueryRow(r.Context(), "SELECT COALESCE((SELECT active FROM product_prices WHERE product_id=$1 AND billing_cycle=$2),false),COALESCE((SELECT price_cents FROM product_prices WHERE product_id=$1 AND billing_cycle=$2),0),COALESCE((SELECT setup_fee_cents FROM product_prices WHERE product_id=$1 AND billing_cycle=$2),0)", input.ProductID, input.BillingCycle).Scan(&priceActive, &price, &setup); err != nil {
			s.fail(w, r, err)
			return
		}
		if !active || !priceActive || (stock != nil && *stock < input.Quantity) {
			writeError(w, 409, "product_unavailable", "商品不可售或库存不足")
			return
		}
		selection, err := store.ValidateProductOptionsForCycleWithQuantities(r.Context(), tx, input.ProductID, input.BillingCycle, input.OptionIDs, input.OptionQuantities)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if _, _, err = store.OptionAmounts(price, setup, selection); err != nil {
			s.fail(w, r, err)
			return
		}
		fieldValues, _, err := store.ValidatePurchaseFields(r.Context(), tx, input.ProductID, input.PurchaseFields)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		fieldJSON, err := json.Marshal(fieldValues)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		optionJSON, err := json.Marshal(selection.IDs)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		quantityJSON, err := json.Marshal(selection.Quantities)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		optionKeyInput, _ := json.Marshal(struct {
			IDs        []int64           `json:"ids"`
			Quantities map[int64]int64   `json:"quantities"`
			Fields     map[string]string `json:"fields,omitempty"`
		}{selection.IDs, selection.Quantities, fieldValues})
		optionSum := sha256.Sum256(optionKeyInput)
		optionKey := hex.EncodeToString(optionSum[:])
		if action == "add" {
			_, err = tx.Exec(r.Context(), "INSERT INTO cart_items(cart_id,product_id,billing_cycle,option_ids,option_quantities,option_key,quantity,product_revision,purchase_fields) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(cart_id,product_id,billing_cycle,option_key) DO UPDATE SET option_ids=EXCLUDED.option_ids,option_quantities=EXCLUDED.option_quantities,purchase_fields=EXCLUDED.purchase_fields,quantity=EXCLUDED.quantity,product_revision=EXCLUDED.product_revision", cartID, input.ProductID, input.BillingCycle, optionJSON, quantityJSON, optionKey, input.Quantity, revision, fieldJSON)
		} else {
			_, err = tx.Exec(r.Context(), "UPDATE cart_items SET billing_cycle=$1,option_ids=$2,option_quantities=$3,option_key=$4,quantity=$5,product_revision=$6,purchase_fields=$9 WHERE id=$7 AND cart_id=$8", input.BillingCycle, optionJSON, quantityJSON, optionKey, input.Quantity, revision, itemID, cartID, fieldJSON)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), "UPDATE carts SET version=version+1,updated_at=now() WHERE id=$1", cartID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]int64{"version": version + 1})
}
func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	var input store.CheckoutInput
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.Checkout(r.Context(), identityFrom(r).ID, r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 201
	if result.Replayed {
		status = 200
	}
	write(w, status, result)
}
func adminRoute(r *http.Request) bool {
	return len(r.URL.Path) >= 14 && r.URL.Path[:14] == "/api/v1/admin/"
}
func (s *Server) orders(w http.ResponseWriter, r *http.Request) {
	uid := identityFrom(r).ID
	limit, offset := pagination(r)
	admin := adminRoute(r)
	where := "(($1::boolean AND admin_customer_allowed($2,o.user_id)) OR (NOT $1::boolean AND o.user_id=$2))"
	args := []any{admin, uid}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		valid := map[string]bool{"pending": true, "paid": true, "active": true, "cancelled": true, "refunded": true}
		if !valid[status] {
			writeError(w, http.StatusBadRequest, "invalid_status", "订单状态无效")
			return
		}
		args = append(args, status)
		where += fmt.Sprintf(" AND o.status=$%d", len(args))
	}
	if query := strings.TrimSpace(r.URL.Query().Get("q")); query != "" {
		if len(query) > 120 {
			writeError(w, http.StatusBadRequest, "invalid_query", "搜索条件过长")
			return
		}
		args = append(args, "%"+query+"%")
		where += fmt.Sprintf(" AND (o.order_no ILIKE $%d OR EXISTS(SELECT 1 FROM order_items oi WHERE oi.order_id=o.id AND oi.product_name ILIKE $%d) OR ($1::boolean AND (u.display_name ILIKE $%d OR u.email ILIKE $%d)))", len(args), len(args), len(args), len(args))
	}
	projection := "o.id,o.order_no,o.user_id,u.display_name AS customer_name,o.status,o.review_status,o.total_cents,o.currency,o.created_at,i.id AS invoice_id,i.status AS invoice_status"
	if admin {
		projection += ",o.notes"
	}
	from := " FROM orders o JOIN users u ON u.id=o.user_id LEFT JOIN invoices i ON i.order_id=o.id AND i.user_id=o.user_id WHERE " + where
	if strings.HasSuffix(r.URL.Path, "/page") {
		s.transactionPage(w, r, projection, from, "o.id", args, limit, offset)
		return
	}
	args = append(args, limit, offset)
	query := fmt.Sprintf("SELECT %s%s ORDER BY o.id DESC LIMIT $%d OFFSET $%d", projection, from, len(args)-1, len(args))
	s.list(w, r, query, args...)
}
func (s *Server) invoices(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	args := []any{adminRoute(r), identityFrom(r).ID}
	where := "(($1::boolean AND admin_customer_allowed($2,i.user_id)) OR (NOT $1::boolean AND i.user_id=$2))"
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		if status != "unpaid" && status != "paid" && status != "cancelled" && status != "refunded" {
			writeError(w, 400, "invalid_status", "账单状态无效")
			return
		}
		args = append(args, status)
		where += fmt.Sprintf(" AND i.status=$%d", len(args))
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		if len(q) > 120 {
			writeError(w, 400, "invalid_query", "搜索条件过长")
			return
		}
		args = append(args, "%"+q+"%")
		where += fmt.Sprintf(" AND (i.invoice_no ILIKE $%d OR o.order_no ILIKE $%d OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.description ILIKE $%d) OR EXISTS(SELECT 1 FROM order_items oi WHERE oi.order_id=o.id AND oi.product_name ILIKE $%d) OR ($1::boolean AND (u.display_name ILIKE $%d OR u.email ILIKE $%d)))", len(args), len(args), len(args), len(args), len(args), len(args))
	}
	projection := "i.id,i.invoice_no,o.id AS order_id,o.order_no,i.user_id,u.display_name AS customer_name,i.status,i.amount_cents,i.due_at,i.paid_at,i.created_at,i.invoice_kind,i.version,i.import_review_required," + invoicePendingProjection
	from := " FROM invoices i JOIN users u ON u.id=i.user_id LEFT JOIN orders o ON o.id=i.order_id AND o.user_id=i.user_id WHERE " + where
	if strings.HasSuffix(r.URL.Path, "/page") {
		s.transactionPage(w, r, projection, from, "i.id", args, limit, offset)
		return
	}
	args = append(args, limit, offset)
	s.list(w, r, fmt.Sprintf("SELECT %s%s ORDER BY i.id DESC LIMIT $%d OFFSET $%d", projection, from, len(args)-1, len(args)), args...)
}
