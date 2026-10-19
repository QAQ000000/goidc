package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) cart(w http.ResponseWriter, r *http.Request) {
	out, err := s.db.ReadCart(r.Context(), identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
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
			var existingFields map[string]string
			if err = json.Unmarshal(currentFieldJSON, &existingFields); err != nil {
				s.fail(w, r, err)
				return
			}
			existingPlain, existingSnapshot, existingErr := store.ValidateStoredPurchaseFields(r.Context(), tx, identityFrom(r).ID, input.ProductID, existingFields, s.db.PurchaseFieldEncryptionKey)
			if input.PurchaseFields == nil {
				if existingErr != nil {
					s.fail(w, r, existingErr)
					return
				}
				input.PurchaseFields = existingPlain
			} else {
				// A mask preserves only an encrypted value from this exact owned cart item.
				for _, field := range existingSnapshot {
					if field.Encrypted && input.PurchaseFields[field.Key] == "********" && existingErr == nil {
						input.PurchaseFields[field.Key] = existingPlain[field.Key]
					}
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
		fieldValues, fieldSnapshots, err := store.ValidatePurchaseFieldsWithKey(r.Context(), tx, input.ProductID, input.PurchaseFields, s.db.PurchaseFieldEncryptionKey)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		identityFields := make(map[string]string, len(fieldValues))
		sensitive := false
		for key, value := range fieldValues {
			identityFields[key] = value
		}
		for _, field := range fieldSnapshots {
			if field.Encrypted {
				sensitive = true
				sealed, sealErr := store.SealPurchaseValue(s.db.PurchaseFieldEncryptionKey, identityFrom(r).ID, input.ProductID, field.Key, field.Value)
				if sealErr != nil {
					s.fail(w, r, sealErr)
					return
				}
				fieldValues[field.Key] = sealed
			}
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
		}{selection.IDs, selection.Quantities, identityFields})
		optionSum := sha256.Sum256(optionKeyInput)
		optionKey := hex.EncodeToString(optionSum[:])
		if sensitive {
			mac := hmac.New(sha256.New, s.db.PurchaseFieldEncryptionKey)
			fmt.Fprintf(mac, "purchase-variant:%d:%d:", identityFrom(r).ID, input.ProductID)
			mac.Write(optionKeyInput)
			optionKey = hex.EncodeToString(mac.Sum(nil))
		}
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
	where := "o.deleted_at IS NULL AND (($1::boolean AND admin_customer_allowed($2,o.user_id)) OR (NOT $1::boolean AND o.user_id=$2))"
	args := []any{admin, uid}
	supplierList := r.URL.Path == "/api/v1/admin/supplier-orders"
	if supplierList {
		var ok bool
		where, args, ok = supplierOrderWhere(w, r, where, args, false)
		if !ok {
			return
		}
	}
	if admin && r.URL.Query().Get("customer_id") != "" {
		customerID, err := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
		if err != nil || customerID < 1 {
			writeError(w, http.StatusBadRequest, "invalid_customer", "客户筛选无效")
			return
		}
		args = append(args, customerID)
		where += fmt.Sprintf(" AND o.user_id=$%d", len(args))
	}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		valid := map[string]bool{"pending": true, "paid": true, "active": true, "cancelled": true, "refunded": true, "review_pending": true}
		if !valid[status] {
			writeError(w, http.StatusBadRequest, "invalid_status", "订单状态无效")
			return
		}
		if status == "review_pending" {
			where += " AND o.review_status='pending' AND o.deleted_at IS NULL"
		} else {
			args = append(args, status)
			where += fmt.Sprintf(" AND o.status=$%d", len(args))
		}
	}
	if query := strings.TrimSpace(r.URL.Query().Get("q")); query != "" {
		if len(query) > 120 {
			writeError(w, http.StatusBadRequest, "invalid_query", "搜索条件过长")
			return
		}
		args = append(args, "%"+query+"%")
		where += fmt.Sprintf(" AND (o.order_no ILIKE $%d OR EXISTS(SELECT 1 FROM order_items oi WHERE oi.order_id=o.id AND oi.product_name ILIKE $%d) OR ($1::boolean AND (u.display_name ILIKE $%d OR u.email ILIKE $%d)))", len(args), len(args), len(args), len(args))
	}
	if admin {
		zone := time.FixedZone("Asia/Shanghai", 8*3600)
		var start, end time.Time
		for _, field := range []string{"date_from", "date_to"} {
			raw := r.URL.Query().Get(field)
			if raw == "" {
				continue
			}
			value, err := time.ParseInLocation("2006-01-02", raw, zone)
			if err != nil {
				writeError(w, 400, "invalid_order_date", "日期筛选无效")
				return
			}
			if field == "date_from" {
				start = value
				args = append(args, value)
				where += fmt.Sprintf(" AND o.created_at >= $%d", len(args))
			} else {
				end = value
				args = append(args, value.AddDate(0, 0, 1))
				where += fmt.Sprintf(" AND o.created_at < $%d", len(args))
			}
		}
		if !start.IsZero() && !end.IsZero() && end.Before(start) {
			writeError(w, 400, "invalid_order_date", "结束日期不能早于开始日期")
			return
		}
		if paymentStatus := r.URL.Query().Get("payment_status"); paymentStatus != "" {
			if paymentStatus != "deferred" && paymentStatus != "unpaid" && paymentStatus != "paid" && paymentStatus != "cancelled" && paymentStatus != "refunded" {
				writeError(w, 400, "invalid_payment_status", "付款状态无效")
				return
			}
			if paymentStatus == "deferred" {
				where += " AND i.id IS NULL"
			} else {
				args = append(args, paymentStatus)
				where += fmt.Sprintf(" AND i.status=$%d", len(args))
			}
		}
	}
	projection := "o.version,o.id,o.order_no,o.user_id,u.display_name AS customer_name,o.status,o.review_status,o.total_cents,o.currency,o.created_at,i.id AS invoice_id,i.status AS invoice_status"
	if admin {
		projection += `,external_refund_order_hold_json(o.id) AS external_refund_order_hold,COALESCE((SELECT cp.admin_note FROM customer_profiles cp WHERE cp.customer_id=o.user_id),'') AS customer_note,o.notes,o.invoice_deferred,o.payment_provider,o.payment_channel_instance_id,COALESCE((SELECT string_agg(DISTINCT nullif(s.resource_info->>'dedicated_ip',''),'、') FROM services s WHERE s.order_id=o.id),'') AS service_ips,COALESCE((SELECT string_agg(oi.product_name,'、' ORDER BY oi.id) FROM order_items oi WHERE oi.order_id=o.id),'') AS product_names,COALESCE((SELECT string_agg(s.domain,'、' ORDER BY s.id) FROM services s WHERE s.order_id=o.id),'') AS service_domains,COALESCE((SELECT display_name FROM users WHERE id=u.sales_owner_id),'') AS sales_owner_name,COALESCE((SELECT pt.provider FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status='succeeded' ORDER BY pt.id DESC LIMIT 1),CASE WHEN i.status='paid' AND EXISTS(SELECT 1 FROM balance_ledger WHERE reference_type='invoice' AND reference_id=i.id AND entry_type='payment') THEN 'balance' ELSE '' END) AS payment_method`
	}
	if supplierList {
		projection += supplierOrderProjection
	}
	from := " FROM orders o JOIN users u ON u.id=o.user_id LEFT JOIN invoices i ON i.order_id=o.id AND i.user_id=o.user_id WHERE " + where
	if supplierList || strings.HasSuffix(r.URL.Path, "/page") {
		s.transactionPage(w, r, projection, from, "o.id", args, limit, offset)
		return
	}
	args = append(args, limit, offset)
	query := fmt.Sprintf("SELECT %s%s ORDER BY o.id DESC LIMIT $%d OFFSET $%d", projection, from, len(args)-1, len(args))
	s.list(w, r, query, args...)
}

// renewalOrders exposes renewal invoices as a stable admin list. Renewals do
// not create rows in orders, so the invoice item is the immutable record and
// the service/product joins provide the business context for the table.
func (s *Server) renewalOrders(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	args := []any{true, identityFrom(r).ID}
	where := "(($1::boolean AND admin_customer_allowed($2,i.user_id)) OR (NOT $1::boolean AND i.user_id=$2)) AND ii.item_type='renew'"
	supplierList := r.URL.Path == "/api/v1/admin/supplier-renewals"
	if supplierList {
		var ok bool
		where, args, ok = supplierOrderWhere(w, r, where, args, true)
		if !ok {
			return
		}
	}

	if value := r.URL.Query().Get("customer_id"); value != "" {
		customerID, err := strconv.ParseInt(value, 10, 64)
		if err != nil || customerID < 1 {
			writeError(w, http.StatusBadRequest, "invalid_customer", "客户筛选无效")
			return
		}
		args = append(args, customerID)
		where += fmt.Sprintf(" AND i.user_id=$%d", len(args))
	}
	if status := strings.TrimSpace(r.URL.Query().Get("status")); status != "" {
		if status != "pending" && status != "paid" && status != "cancelled" && status != "refunded" {
			writeError(w, http.StatusBadRequest, "invalid_status", "续费订单状态无效")
			return
		}
		args = append(args, status)
		where += fmt.Sprintf(" AND CASE WHEN i.status='unpaid' THEN 'pending' WHEN i.status='cancelled' THEN 'cancelled' ELSE i.status END=$%d", len(args))
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		if len(q) > 120 {
			writeError(w, http.StatusBadRequest, "invalid_query", "搜索条件过长")
			return
		}
		args = append(args, "%"+q+"%")
		where += fmt.Sprintf(" AND (i.invoice_no ILIKE $%d OR ii.description ILIKE $%d OR ($1::boolean AND (u.display_name ILIKE $%d OR u.email ILIKE $%d)))", len(args), len(args), len(args), len(args))
	}
	projection := "ii.id,i.invoice_no AS order_no,i.user_id,u.display_name AS customer_name,CASE WHEN i.status='unpaid' THEN 'pending' WHEN i.status='cancelled' THEN 'cancelled' ELSE i.status END AS status,'approved'::text AS review_status,ii.amount_cents AS total_cents,'CNY'::text AS currency,i.created_at,i.id AS invoice_id,i.status AS invoice_status,i.due_at,ii.description AS product_name,ii.service_id,s.domain AS service_domain,COALESCE((SELECT pt.provider FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status='succeeded' ORDER BY pt.id DESC LIMIT 1),CASE WHEN i.status='paid' AND EXISTS(SELECT 1 FROM balance_ledger WHERE reference_type='invoice' AND reference_id=i.id AND entry_type='payment') THEN 'balance' ELSE '' END) AS payment_method"
	if supplierList {
		projection += ",s.resource_info->>'dedicated_ip' AS service_ips,s.delivery_snapshot->>'supplier_id' AS supplier_id,(SELECT name FROM supplier_accounts a WHERE a.id::text=s.delivery_snapshot->>'supplier_id') AS supplier_name,(SELECT (j.result->>'cost_cents')::bigint FROM provision_jobs j WHERE j.idempotency_key='supplier-renew-'||i.id::text AND j.service_id=s.id AND j.status='succeeded' AND j.result->>'currency'='CNY') AS cost_cents,(SELECT j.status FROM provision_jobs j WHERE j.idempotency_key='supplier-renew-'||i.id::text AND j.service_id=s.id) AS upstream_renewal_status,'local_renewal_invoice'::text AS record_kind"
	}
	from := " FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id JOIN users u ON u.id=i.user_id LEFT JOIN services s ON s.id=ii.service_id AND s.user_id=i.user_id WHERE " + where
	s.transactionPage(w, r, projection, from, "ii.id", args, limit, offset)
}
func (s *Server) invoices(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	args := []any{adminRoute(r), identityFrom(r).ID}
	where := "(($1::boolean AND admin_customer_allowed($2,i.user_id)) OR (NOT $1::boolean AND i.user_id=$2))"
	if adminRoute(r) && r.URL.Query().Get("customer_id") != "" {
		customerID, err := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
		if err != nil || customerID < 1 {
			writeError(w, http.StatusBadRequest, "invalid_customer", "客户筛选无效")
			return
		}
		args = append(args, customerID)
		where += fmt.Sprintf(" AND i.user_id=$%d", len(args))
	}
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
