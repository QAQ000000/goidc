package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

type Conflict struct{ Code, Message string }

func (e *Conflict) Error() string         { return e.Message }
func conflict(code, message string) error { return &Conflict{code, message} }

type CheckoutInput struct {
	CartVersion int64   `json:"cart_version"`
	ItemIDs     []int64 `json:"item_ids"`
}
type CheckoutResult struct {
	OrderID       int64  `json:"order_id"`
	InvoiceID     int64  `json:"invoice_id"`
	OrderNo       string `json:"order_no"`
	TotalCents    int64  `json:"total_cents"`
	DiscountCents int64  `json:"discount_cents"`
	PromoCode     string `json:"promo_code,omitempty"`
	Replayed      bool   `json:"replayed"`
}

func (s *Store) Checkout(ctx context.Context, userID int64, key string, input CheckoutInput) (CheckoutResult, error) {
	var result CheckoutResult
	if len(key) < 8 || len(key) > 128 || input.CartVersion < 1 || len(input.ItemIDs) == 0 || len(input.ItemIDs) > 100 {
		return result, conflict("invalid_checkout", "请选择购物车项目并提供有效版本与幂等键")
	}
	ids := append([]int64(nil), input.ItemIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if id < 1 || (i > 0 && ids[i-1] == id) {
			return result, conflict("invalid_selection", "购物车项目无效或重复")
		}
	}
	input.ItemIDs = ids
	b, _ := json.Marshal(input)
	sum := sha256.Sum256(b)
	requestHash := hex.EncodeToString(sum[:])
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	var cartID string
	var version int64
	if err = tx.QueryRow(ctx, "SELECT id::text,version FROM carts WHERE user_id=$1 FOR UPDATE", userID).Scan(&cartID, &version); err != nil {
		return result, err
	}
	var savedHash string
	err = tx.QueryRow(ctx, "SELECT o.id,i.id,o.order_no,o.total_cents,o.request_hash FROM orders o JOIN invoices i ON i.order_id=o.id WHERE o.user_id=$1 AND o.idempotency_key=$2", userID, key).Scan(&result.OrderID, &result.InvoiceID, &result.OrderNo, &result.TotalCents, &savedHash)
	if err == nil {
		if savedHash != requestHash {
			return result, conflict("idempotency_conflict", "相同幂等键对应不同的请求")
		}
		result.Replayed = true
		return result, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if version != input.CartVersion {
		return result, conflict("stale_cart", "购物车已发生变化，请重新确认")
	}
	var promoCode string
	var promoID int64
	if err = tx.QueryRow(ctx, "SELECT promo_code_id,code FROM cart_promo_codes WHERE cart_id=$1", cartID).Scan(&promoID, &promoCode); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		promoCode = ""
		promoID = 0
	}
	if err = CatalogReadLock(ctx, tx); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT ci.id,ci.product_id,ci.quantity,ci.product_revision,p.revision,p.name,ci.billing_cycle,ci.option_ids,ci.option_quantities,pp.price_cents,pp.setup_fee_cents,p.stock,(p.active AND catalog_category_visible(p.category_id)),pp.active,p.approval_required,ci.purchase_fields
 FROM cart_items ci JOIN products p ON p.id=ci.product_id JOIN product_prices pp ON pp.product_id=ci.product_id AND pp.billing_cycle=ci.billing_cycle WHERE ci.cart_id=$1 AND ci.id=ANY($2) ORDER BY p.id FOR UPDATE OF p,ci,pp`, cartID, ids)
	if err != nil {
		return result, err
	}
	type item struct {
		ID, ProductID, Revision, CurrentRevision, Price, Setup int64
		Qty                                                    int
		Name, Cycle                                            string
		Stock                                                  *int
		Active, PriceActive                                    bool
		ApprovalRequired                                       bool
		OptionJSON                                             []byte
		OptionQuantityJSON                                     []byte
		OptionIDs                                              []int64
		OptionQuantities                                       map[int64]int64
		OptionPrice, OptionSetup                               int64
		Snapshot                                               []OptionSnapshot
		PurchaseJSON                                           []byte
		PurchaseSnapshot                                       []PurchaseFieldSnapshot
	}
	items := []item{}
	promoLines := []PromoLine{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.ID, &v.ProductID, &v.Qty, &v.Revision, &v.CurrentRevision, &v.Name, &v.Cycle, &v.OptionJSON, &v.OptionQuantityJSON, &v.Price, &v.Setup, &v.Stock, &v.Active, &v.PriceActive, &v.ApprovalRequired, &v.PurchaseJSON); err != nil {
			rows.Close()
			return result, err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(items) != len(ids) {
		return result, conflict("stale_selection", "选中的购物车项目不存在")
	}
	requiresApproval := false
	for index := range items {
		v := &items[index]
		requiresApproval = requiresApproval || v.ApprovalRequired
		if !v.Active || !v.PriceActive {
			return result, conflict("product_unavailable", "商品已下架")
		}
		if v.Revision != v.CurrentRevision {
			return result, conflict("price_changed", "商品价格或配置已变化，请重新加入购物车")
		}
		if v.Stock != nil && *v.Stock < v.Qty {
			return result, conflict("insufficient_stock", "商品库存不足")
		}
		if err = json.Unmarshal(v.OptionJSON, &v.OptionIDs); err != nil {
			return result, conflict("invalid_product_options", "产品配置项无效")
		}
		v.OptionQuantities = map[int64]int64{}
		if err = json.Unmarshal(v.OptionQuantityJSON, &v.OptionQuantities); err != nil {
			return result, conflict("invalid_product_options", "产品配置数量无效")
		}
		selection, selectionErr := ValidateProductOptionsForCycleWithQuantities(ctx, tx, v.ProductID, v.Cycle, v.OptionIDs, v.OptionQuantities)
		if selectionErr != nil {
			return result, selectionErr
		}
		v.OptionIDs = selection.IDs
		v.OptionQuantities = selection.Quantities
		v.OptionPrice, v.OptionSetup = selection.PriceDeltaCents, selection.SetupDeltaCents
		v.Snapshot = selection.Snapshot
		var fieldValues map[string]string
		if err = json.Unmarshal(v.PurchaseJSON, &fieldValues); err != nil {
			return result, err
		}
		_, v.PurchaseSnapshot, err = ValidatePurchaseFieldsWithKey(ctx, tx, v.ProductID, fieldValues, s.PurchaseFieldEncryptionKey)
		if err != nil {
			return result, err
		}
		for i := range v.PurchaseSnapshot {
			if v.PurchaseSnapshot[i].Encrypted {
				v.PurchaseSnapshot[i].Value, err = SealPurchaseValue(s.PurchaseFieldEncryptionKey, v.PurchaseSnapshot[i].Value)
				if err != nil {
					return result, conflict("purchase_field_encryption_unconfigured", "敏感购买字段未配置加密密钥")
				}
			}
		}
		profile := map[string]string{}
		for _, field := range v.PurchaseSnapshot {
			if field.Scope == "customer" && field.ProfileKey != "" {
				profile[field.ProfileKey] = field.Value
			}
		}
		if len(profile) > 0 {
			profileJSON, marshalErr := json.Marshal(profile)
			if marshalErr != nil {
				return result, marshalErr
			}
			if _, err = tx.Exec(ctx, `UPDATE users SET metadata=metadata || jsonb_build_object('purchase_profile',coalesce(metadata->'purchase_profile','{}'::jsonb) || $2::jsonb),updated_at=now() WHERE id=$1`, userID, profileJSON); err != nil {
				return result, err
			}
		}
		price, setup, amountErr := OptionAmounts(v.Price, v.Setup, selection)
		if amountErr != nil {
			return result, amountErr
		}
		unitTotal := price + setup
		if unitTotal < 0 || (v.Qty > 0 && unitTotal > maxInt64/int64(v.Qty)) || (result.TotalCents > maxInt64-unitTotal*int64(v.Qty)) {
			return result, conflict("invalid_product_options", "配置后金额无效")
		}
		result.TotalCents += unitTotal * int64(v.Qty)
		promoLines = append(promoLines, PromoLine{ProductID: v.ProductID, Cycle: v.Cycle, BaseCents: unitTotal * int64(v.Qty), SetupCents: setup * int64(v.Qty), Quantity: v.Qty})
	}
	renewalBenefits := make([]RenewalBenefit, len(items))
	if promoCode != "" {
		quote, quoteErr := QuotePromo(ctx, tx, userID, promoCode, promoLines, true)
		if quoteErr != nil {
			return result, quoteErr
		}
		result.DiscountCents = quote.DiscountCents
		result.PromoCode = quote.Code
		if result.DiscountCents > result.TotalCents {
			result.DiscountCents = result.TotalCents
		}
		result.TotalCents -= result.DiscountCents
		promoID = quote.ID
		renewalBenefits = quote.RenewalBenefits
	}
	reviewStatus := "approved"
	if requiresApproval {
		reviewStatus = "pending"
	}
	if err = tx.QueryRow(ctx, `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,promo_code_id,promo_code,discount_cents,review_status)
 VALUES('O-'||replace(gen_random_uuid()::text,'-',''),$1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,order_no`, userID, result.TotalCents, key, requestHash, nullableInt64(promoID), result.PromoCode, result.DiscountCents, reviewStatus).Scan(&result.OrderID, &result.OrderNo); err != nil {
		return result, err
	}
	if err = InitializeServiceNavigation(ctx, tx, userID); err != nil {
		return result, err
	}
	for index, v := range items {
		benefit := renewalBenefits[index]
		var orderItemID int64
		optionJSON, marshalErr := json.Marshal(v.OptionIDs)
		if marshalErr != nil {
			return result, marshalErr
		}
		optionQuantityJSON, marshalErr := json.Marshal(v.OptionQuantities)
		if marshalErr != nil {
			return result, marshalErr
		}
		snapshot, marshalErr := json.Marshal(v.Snapshot)
		if marshalErr != nil {
			return result, marshalErr
		}
		unitTotal := v.Price + v.Setup + v.OptionPrice + v.OptionSetup
		purchaseJSON, marshalErr := json.Marshal(v.PurchaseSnapshot)
		if marshalErr != nil {
			return result, marshalErr
		}
		if err = tx.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents,config_options,option_quantities,config_snapshot,renewal_discount_percent,renewal_promo_code,purchase_fields,renewal_fixed_discount_cents,renewal_discount_limit)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING id`, result.OrderID, v.ProductID, v.Name, v.Cycle, v.Qty, v.Price+v.OptionPrice, v.Setup+v.OptionSetup, unitTotal*int64(v.Qty), optionJSON, optionQuantityJSON, snapshot, benefit.Percent, benefit.Code, purchaseJSON, benefit.FixedCents, benefit.Limit).Scan(&orderItemID); err != nil {
			return result, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,unit_number,provision_mode,renewal_discount_percent,renewal_promo_code,renewal_fixed_discount_cents,renewal_discount_limit,delivery_snapshot)
SELECT $1,$2,$3,p.id,$5,'svc-'||replace(gen_random_uuid()::text,'-',''),$6,n,p.provision_mode,$8,$9,$10,$11,p.delivery_config
FROM products p CROSS JOIN generate_series(1,$7::int) n WHERE p.id=$4`, userID, result.OrderID, orderItemID, v.ProductID, v.Name, v.Cycle, v.Qty, benefit.Percent, benefit.Code, benefit.FixedCents, benefit.Limit); err != nil {
			return result, err
		}
		if v.Stock != nil {
			if _, err = tx.Exec(ctx, "UPDATE products SET stock=stock-$1,updated_at=now() WHERE id=$2", v.Qty, v.ProductID); err != nil {
				return result, err
			}
		}
		if _, err = tx.Exec(ctx, `INSERT INTO service_navigation_preferences(user_id,group_id,visible)
 SELECT $1,service_group_id,true FROM products WHERE id=$2 AND service_group_id IS NOT NULL
 ON CONFLICT(user_id,group_id) DO UPDATE SET visible=true,version=service_navigation_preferences.version+1
 WHERE NOT service_navigation_preferences.visible`, userID, v.ProductID); err != nil {
			return result, err
		}
	}
	if err = tx.QueryRow(ctx, "INSERT INTO invoices(invoice_no,user_id,order_id,amount_cents) VALUES('I-'||replace(gen_random_uuid()::text,'-',''),$1,$2,$3) RETURNING id", userID, result.OrderID, result.TotalCents).Scan(&result.InvoiceID); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM cart_items WHERE cart_id=$1 AND id=ANY($2)", cartID, ids); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, "UPDATE carts SET version=version+1,updated_at=now() WHERE id=$1", cartID); err != nil {
		return result, err
	}
	if promoID > 0 {
		if _, err = tx.Exec(ctx, "INSERT INTO promo_code_redemptions(promo_code_id,user_id,order_id,discount_cents) VALUES($1,$2,$3,$4)", promoID, userID, result.OrderID, result.DiscountCents); err != nil {
			return result, err
		}
		if _, err = tx.Exec(ctx, "UPDATE promo_codes SET used_count=used_count+1,updated_at=now() WHERE id=$1", promoID); err != nil {
			return result, err
		}
	}
	if err = Audit(ctx, tx, userID, "order.created", "order", result.OrderID, map[string]any{"total_cents": result.TotalCents, "item_ids": ids}); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func nullableInt64(value int64) any {
	if value < 1 {
		return nil
	}
	return value
}

func Audit(ctx context.Context, tx pgx.Tx, actor int64, action, entity string, id int64, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var actorValue any
	if actor > 0 {
		actorValue = actor
	}
	_, err = tx.Exec(ctx, "INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES($1,$2,$3,$4,$5)", actorValue, action, entity, fmt.Sprint(id), b)
	return err
}
