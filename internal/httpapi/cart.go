package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
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
	limit, offset := pagination(r)
	in := store.OrderAccountListFilter{Actor: identityFrom(r).ID, Admin: adminRoute(r), Supplier: r.URL.Path == "/api/v1/admin/supplier-orders", Page: strings.HasSuffix(r.URL.Path, "/page"), Limit: limit, Offset: offset}
	if in.Supplier {
		in.SupplierID = r.URL.Query().Get("supplier_id")
		if in.SupplierID != "" {
			if _, err := parsePositiveID(in.SupplierID); err != nil {
				writeError(w, 400, "invalid_supplier", "供应商筛选无效")
				return
			}
		}
	}
	if in.Admin {
		var err error
		in.CustomerID, err = s.db.AdminCustomerFilter(r.Context(), in.Actor, r.URL.Query())
		if accountBusinessFailure(s, w, r, err) {
			return
		}
	}
	in.Status = strings.TrimSpace(r.URL.Query().Get("status"))
	if in.Status != "" {
		valid := map[string]bool{"pending": true, "paid": true, "active": true, "cancelled": true, "refunded": true, "review_pending": true}
		if !valid[in.Status] {
			writeError(w, 400, "invalid_status", "订单状态无效")
			return
		}
	}
	in.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	if len(in.Query) > 120 {
		writeError(w, 400, "invalid_query", "搜索条件过长")
		return
	}
	if in.Admin {
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
				in.DateFrom = &value
			} else {
				end = value
				exclusive := value.AddDate(0, 0, 1)
				in.DateTo = &exclusive
			}
		}
		if !start.IsZero() && !end.IsZero() && end.Before(start) {
			writeError(w, 400, "invalid_order_date", "结束日期不能早于开始日期")
			return
		}
		in.PaymentStatus = r.URL.Query().Get("payment_status")
		if in.PaymentStatus != "" && in.PaymentStatus != "deferred" && in.PaymentStatus != "unpaid" && in.PaymentStatus != "paid" && in.PaymentStatus != "cancelled" && in.PaymentStatus != "refunded" {
			writeError(w, 400, "invalid_payment_status", "付款状态无效")
			return
		}
	}
	result, err := s.db.OrderAccountList(r.Context(), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}

// renewalOrders exposes renewal invoices as a stable admin list. Renewals do
// not create rows in orders, so the invoice item is the immutable record and
// the service/product joins provide the business context for the table.
func (s *Server) renewalOrders(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	in := store.OrderAccountListFilter{Actor: identityFrom(r).ID, Admin: true, Page: true, Supplier: r.URL.Path == "/api/v1/admin/supplier-renewals", Limit: limit, Offset: offset}
	var err error
	in.CustomerID, err = s.db.AdminCustomerFilter(r.Context(), in.Actor, r.URL.Query())
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	if in.Supplier {
		in.SupplierID = r.URL.Query().Get("supplier_id")
		if in.SupplierID != "" {
			if _, err := parsePositiveID(in.SupplierID); err != nil {
				writeError(w, 400, "invalid_supplier", "供应商筛选无效")
				return
			}
		}
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
				in.DateFrom = &value
			} else {
				end = value
				exclusive := value.AddDate(0, 0, 1)
				in.DateTo = &exclusive
			}
		}
		if !start.IsZero() && !end.IsZero() && end.Before(start) {
			writeError(w, 400, "invalid_order_date", "结束日期不能早于开始日期")
			return
		}
	}
	in.Status = strings.TrimSpace(r.URL.Query().Get("status"))
	if in.Status != "" && in.Status != "pending" && in.Status != "paid" && in.Status != "cancelled" && in.Status != "refunded" {
		writeError(w, 400, "invalid_status", "续费订单状态无效")
		return
	}
	in.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	if len(in.Query) > 120 {
		writeError(w, 400, "invalid_query", "搜索条件过长")
		return
	}
	result, err := s.db.RenewalAccountList(r.Context(), in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, result)
}
func (s *Server) invoices(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	in := store.InvoiceAccountListFilter{Actor: identityFrom(r).ID, Admin: adminRoute(r), Page: strings.HasSuffix(r.URL.Path, "/page"), Limit: limit, Offset: offset}
	if in.Admin {
		var err error
		in.CustomerID, err = s.db.AdminCustomerFilter(r.Context(), in.Actor, r.URL.Query())
		if accountBusinessFailure(s, w, r, err) {
			return
		}
	}
	in.Status = strings.TrimSpace(r.URL.Query().Get("status"))
	if in.Status != "" {
		if in.Status != "unpaid" && in.Status != "paid" && in.Status != "cancelled" && in.Status != "refunded" {
			writeError(w, 400, "invalid_status", "账单状态无效")
			return
		}
	}
	in.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	if len(in.Query) > 120 {
		writeError(w, 400, "invalid_query", "搜索条件过长")
		return
	}
	result, err := s.db.InvoiceAccountList(r.Context(), in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, result)
}
