package store

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/jackc/pgx/v5"
)

type CartPurchaseQuote struct {
	QuoteHash          string                `json:"quote_hash"`
	SubtotalCents      int64                 `json:"subtotal_cents"`
	GroupDiscountCents int64                 `json:"group_discount_cents"`
	PromoDiscountCents int64                 `json:"promo_discount_cents"`
	DiscountCents      int64                 `json:"discount_cents"`
	TotalCents         int64                 `json:"total_cents"`
	PromoCode          string                `json:"promo_code,omitempty"`
	Items              []PurchasePricingLine `json:"items"`
}

func cartPurchaseHash(version int64, ids []int64, items []checkoutLine, result CheckoutResult, benefits []RenewalBenefit) string {
	type line struct {
		ID, Revision int64
		Pricing      *PurchasePricingLine
		Options      []OptionSnapshot
		Fields       []PurchaseFieldSnapshot
	}
	lines := make([]line, len(items))
	for i, v := range items {
		lines[i] = line{v.ID, v.CurrentRevision, v.Pricing, v.Snapshot, v.PurchaseSnapshot}
	}
	return adminOrderHash(struct {
		Version         int64
		IDs             []int64
		Lines           []line
		Total, Discount int64
		Promo           string
		Benefits        []RenewalBenefit
	}{version, ids, lines, result.TotalCents, result.DiscountCents, result.PromoCode, benefits})
}

// Quote uses the same validation/pricing pipeline as checkout. It only holds
// transaction locks until returning and never creates an order or reservation.
func (s *Store) QuoteCartPurchase(ctx context.Context, userID int64, input CheckoutInput) (CartPurchaseQuote, error) {
	var out CartPurchaseQuote
	if input.CartVersion < 1 || len(input.ItemIDs) < 1 || len(input.ItemIDs) > 100 {
		return out, conflict("invalid_checkout", "请选择购物车项目并提供有效版本")
	}
	ids := append([]int64(nil), input.ItemIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if id < 1 || (i > 0 && id == ids[i-1]) {
			return out, conflict("invalid_selection", "购物车项目无效或重复")
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var cartID string
	var version int64
	if err = tx.QueryRow(ctx, "SELECT id::text,version FROM carts WHERE user_id=$1 FOR UPDATE", userID).Scan(&cartID, &version); err != nil {
		return out, err
	}
	if version != input.CartVersion {
		return out, conflict("stale_cart", "购物车已发生变化，请重新确认")
	}
	if err = GroupPricingReadLock(ctx, tx); err != nil {
		return out, err
	}
	if err = CatalogReadLock(ctx, tx); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT ci.id,ci.product_id,ci.quantity,ci.product_revision,p.revision,p.name,ci.billing_cycle,ci.option_ids,ci.option_quantities,pp.price_cents,pp.setup_fee_cents,p.stock,(p.active AND catalog_category_visible(p.category_id)),pp.active,p.approval_required,ci.purchase_fields FROM cart_items ci JOIN products p ON p.id=ci.product_id JOIN product_prices pp ON pp.product_id=ci.product_id AND pp.billing_cycle=ci.billing_cycle WHERE ci.cart_id=$1 AND ci.id=ANY($2) ORDER BY p.id,ci.id FOR UPDATE OF p,ci,pp`, cartID, ids)
	if err != nil {
		return out, err
	}
	items := []checkoutLine{}
	for rows.Next() {
		var v checkoutLine
		if err = rows.Scan(&v.ID, &v.ProductID, &v.Qty, &v.Revision, &v.CurrentRevision, &v.Name, &v.Cycle, &v.OptionJSON, &v.OptionQuantityJSON, &v.Price, &v.Setup, &v.Stock, &v.Active, &v.PriceActive, &v.ApprovalRequired, &v.PurchaseJSON); err != nil {
			rows.Close()
			return out, err
		}
		v.StoredFields = true
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(items) != len(ids) {
		return out, conflict("stale_selection", "选中的购物车项目不存在")
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	var code string
	err = tx.QueryRow(ctx, "SELECT code FROM cart_promo_codes WHERE cart_id=$1", cartID).Scan(&code)
	if err != nil && err != pgx.ErrNoRows {
		return out, err
	}
	result, benefits, _, err := s.prepareCheckoutLines(ctx, tx, userID, items, code)
	if err != nil {
		return out, err
	}
	out = CartPurchaseQuote{QuoteHash: cartPurchaseHash(version, ids, items, result, benefits), SubtotalCents: result.TotalCents + result.DiscountCents, GroupDiscountCents: result.GroupDiscountCents, PromoDiscountCents: result.PromoDiscountCents, DiscountCents: result.DiscountCents, TotalCents: result.TotalCents, PromoCode: result.PromoCode, Items: []PurchasePricingLine{}}
	for _, v := range items {
		if v.Pricing != nil {
			out.Items = append(out.Items, *v.Pricing)
		}
	}
	return out, tx.Commit(ctx)
}

func loadCheckoutPricingTotals(ctx context.Context, tx pgx.Tx, result *CheckoutResult) error {
	ok, err := purchasePricingAvailable(ctx, tx)
	if err != nil || !ok {
		return err
	}
	return tx.QueryRow(ctx, `SELECT coalesce(sum(p.group_discount_cents),0)::bigint,coalesce(sum(p.promo_discount_cents),0)::bigint FROM order_item_pricing p JOIN order_items oi ON oi.id=p.order_item_id WHERE oi.order_id=$1`, result.OrderID).Scan(&result.GroupDiscountCents, &result.PromoDiscountCents)
}
func enrichPurchaseReadPricing(ctx context.Context, tx pgx.Tx, head map[string]any, items []map[string]any) error {
	ok, err := purchasePricingAvailable(ctx, tx)
	if err != nil || !ok {
		return err
	}
	ids := []int64{}
	for _, item := range items {
		ids = append(ids, purchaseReadInteger(item["id"]))
	}
	rows, err := tx.Query(ctx, "SELECT order_item_id,snapshot FROM order_item_pricing WHERE order_item_id=ANY($1)", ids)
	if err != nil {
		return err
	}
	snapshots := map[int64]PurchasePricingLine{}
	for rows.Next() {
		var id int64
		var b []byte
		if err = rows.Scan(&id, &b); err != nil {
			rows.Close()
			return err
		}
		var p PurchasePricingLine
		if err = json.Unmarshal(b, &p); err != nil {
			rows.Close()
			return err
		}
		snapshots[id] = p
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var group, promo int64
	for _, item := range items {
		if p, found := snapshots[purchaseReadInteger(item["id"])]; found {
			item["pricing"] = p
			group += p.GroupDiscountCents
			promo += p.PromoDiscountCents
		}
	}
	if len(items) > 0 && len(snapshots) == len(items) {
		head["group_discount_cents"] = group
		head["promo_discount_cents"] = promo
	}
	return nil
}
