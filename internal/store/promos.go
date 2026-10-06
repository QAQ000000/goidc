package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type PromoLine struct {
	ProductID  int64
	Cycle      string
	BaseCents  int64
	SetupCents int64
	Quantity   int
}

type PromoQuote struct {
	ID             int64  `json:"id"`
	Code           string `json:"code"`
	DiscountType   string `json:"discount_type"`
	DiscountCents  int64  `json:"discount_cents"`
	Recurring      bool   `json:"recurring"`
	RecurringLimit int64  `json:"recurring_limit"`
	// Per-line benefits are captured only for eligible renewable purchases.
	RenewalBenefits []RenewalBenefit `json:"-"`
}

type UpgradePromoQuote struct {
	ID            int64  `json:"id"`
	Code          string `json:"code"`
	DiscountType  string `json:"discount_type"`
	BaseCents     int64  `json:"base_cents"`
	DiscountCents int64  `json:"discount_cents"`
}

// PercentDiscount floors to integer cents without overflowing at bigint limits.
func PercentDiscount(cents, percent int64) (int64, error) {
	if cents < 0 || percent < 0 || percent > 100 {
		return 0, conflict("invalid_discount", "优惠金额或百分比无效")
	}
	return cents/100*percent + cents%100*percent/100, nil
}

type promoRecord struct {
	ID, Value, MaxUses, UsedCount int64
	Code, DiscountType            string
	Cycles, Applies, Requires     []string
	AppliesIDs, RequiresIDs       []int64
	RequiresExisting              bool
	StartsAt, ExpiresAt           *time.Time
	OneTime, OnlyNew, OnlyOld     bool
	OncePerCustomer, Recurring    bool
	RecurringLimit                int64
	UpgradeOnly                   bool
	Active                        bool
}

func loadPromo(ctx context.Context, tx pgx.Tx, code string, lock bool) (promoRecord, error) {
	var p promoRecord
	query := `SELECT id,code,discount_type,value,cycles,applies_product_ids,requires_product_ids,requires_existing,starts_at,expires_at,max_uses,used_count,one_time,only_new_customer,only_old_customer,once_per_customer,recurring,active,recurring_limit,upgrade_only FROM promo_codes WHERE code=$1`
	if lock {
		query += " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, query, strings.ToUpper(strings.TrimSpace(code))).Scan(&p.ID, &p.Code, &p.DiscountType, &p.Value, &p.Cycles, &p.AppliesIDs, &p.RequiresIDs, &p.RequiresExisting, &p.StartsAt, &p.ExpiresAt, &p.MaxUses, &p.UsedCount, &p.OneTime, &p.OnlyNew, &p.OnlyOld, &p.OncePerCustomer, &p.Recurring, &p.Active, &p.RecurringLimit, &p.UpgradeOnly)
	if err != nil {
		if err == pgx.ErrNoRows {
			return p, conflict("promo_not_found", "优惠码不存在")
		}
		return p, err
	}
	return p, nil
}

func QuotePromo(ctx context.Context, tx pgx.Tx, userID int64, code string, lines []PromoLine, lock bool) (PromoQuote, error) {
	var quote PromoQuote
	p, err := loadPromo(ctx, tx, code, lock)
	if err != nil {
		return quote, err
	}
	if !p.Active {
		return quote, conflict("promo_inactive", "优惠码已停用")
	}
	if p.UpgradeOnly {
		return quote, conflict("promo_upgrade_only", "该优惠码仅适用于商品升级")
	}
	if p.Recurring && (p.DiscountType != "percent" && p.DiscountType != "fixed" || p.Value < 1 || p.DiscountType == "percent" && p.Value > 100) {
		return quote, conflict("recurring_promo_unsupported", "循环续费优惠支持百分比或固定金额")
	}
	now := time.Now()
	if p.StartsAt != nil && now.Before(*p.StartsAt) {
		return quote, conflict("promo_not_started", "优惠码尚未生效")
	}
	if p.ExpiresAt != nil && !now.Before(*p.ExpiresAt) {
		return quote, conflict("promo_expired", "优惠码已过期")
	}
	if p.MaxUses > 0 && p.UsedCount >= p.MaxUses || p.OneTime && p.UsedCount > 0 {
		return quote, conflict("promo_exhausted", "优惠码使用次数已用完")
	}
	var orderCount int64
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM orders WHERE user_id=$1", userID).Scan(&orderCount); err != nil {
		return quote, err
	}
	if p.OnlyNew && orderCount > 0 {
		return quote, conflict("promo_new_customer_only", "优惠码仅适用于新客户")
	}
	if p.OnlyOld && orderCount == 0 {
		return quote, conflict("promo_old_customer_only", "优惠码仅适用于老客户")
	}
	if p.OncePerCustomer {
		var used bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM promo_code_redemptions WHERE promo_code_id=$1 AND user_id=$2)", p.ID, userID).Scan(&used); err != nil {
			return quote, err
		}
		if used {
			return quote, conflict("promo_already_used", "该客户已经使用过此优惠码")
		}
	}
	if len(lines) == 0 {
		return quote, conflict("promo_cart_empty", "购物车没有可优惠的商品")
	}
	products := make(map[int64]bool, len(lines))
	eligibleBase, eligibleSetup := int64(0), int64(0)
	renewalBenefits := make([]RenewalBenefit, len(lines))
	for index, line := range lines {
		products[line.ProductID] = true
		if len(p.AppliesIDs) > 0 && !containsInt64(p.AppliesIDs, line.ProductID) {
			continue
		}
		if len(p.Cycles) > 0 && !containsString(p.Cycles, line.Cycle) {
			continue
		}
		if line.BaseCents < 0 || line.SetupCents < 0 || line.Quantity < 1 {
			return quote, conflict("promo_invalid_amount", "优惠金额无效")
		}
		if line.BaseCents > maxInt64-eligibleBase || line.SetupCents > maxInt64-eligibleSetup {
			return quote, conflict("promo_invalid_amount", "优惠金额超出范围")
		}
		eligibleBase += line.BaseCents
		eligibleSetup += line.SetupCents
		if p.Recurring && line.Cycle != "once" {
			benefit := RenewalBenefit{Code: p.Code, Limit: p.RecurringLimit}
			if p.DiscountType == "fixed" {
				benefit.FixedCents = p.Value
			} else {
				benefit.Percent = p.Value
			}
			renewalBenefits[index] = benefit
		}
	}
	if len(p.RequiresIDs) > 0 {
		matched := false
		for _, id := range p.RequiresIDs {
			if products[id] {
				matched = true
				break
			}
		}
		if p.RequiresExisting && !matched {
			var existing bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM services WHERE user_id=$1 AND product_id=ANY($2) AND status NOT IN ('terminated','cancelled'))", userID, p.RequiresIDs).Scan(&existing); err != nil {
				return quote, err
			}
			matched = existing
		}
		if !matched {
			return quote, conflict("promo_requirement_failed", "不满足优惠码商品条件")
		}
	}
	if eligibleBase == 0 {
		return quote, conflict("promo_not_applicable", "优惠码不适用于当前购物车")
	}
	discount := int64(0)
	switch p.DiscountType {
	case "percent":
		if p.Value < 0 || p.Value > 100 {
			return quote, conflict("promo_invalid_amount", "优惠码折扣比例无效")
		}
		discount, err = PercentDiscount(eligibleBase, p.Value)
		if err != nil {
			return quote, err
		}
	case "fixed":
		discount = p.Value
	case "free":
		discount = eligibleSetup
	default:
		return quote, conflict("promo_type_unsupported", "该优惠码类型暂不支持")
	}
	if discount > eligibleBase {
		discount = eligibleBase
	}
	if discount <= 0 {
		return quote, conflict("promo_no_discount", "优惠码未产生折扣")
	}
	quote = PromoQuote{ID: p.ID, Code: p.Code, DiscountType: p.DiscountType, DiscountCents: discount, Recurring: p.Recurring, RecurringLimit: p.RecurringLimit, RenewalBenefits: renewalBenefits}
	return quote, nil
}

func QuoteUpgradePromo(ctx context.Context, tx pgx.Tx, userID int64, code string, base, targetProductID int64, cycle string, lock bool) (UpgradePromoQuote, error) {
	var quote UpgradePromoQuote
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return quote, nil
	}
	if base <= 0 || targetProductID < 1 {
		return quote, conflict("promo_not_applicable", "升级差额没有可优惠金额")
	}
	p, err := loadPromo(ctx, tx, code, lock)
	if err != nil {
		return quote, err
	}
	if !p.UpgradeOnly {
		return quote, conflict("promo_upgrade_only_required", "该优惠码不是升级专用优惠")
	}
	if p.Recurring || p.DiscountType == "free" {
		return quote, conflict("promo_upgrade_unsupported", "升级优惠不支持循环或免初装费类型")
	}
	now := time.Now()
	if !p.Active || p.StartsAt != nil && now.Before(*p.StartsAt) || p.ExpiresAt != nil && !now.Before(*p.ExpiresAt) {
		return quote, conflict("promo_unavailable", "升级优惠码当前不可用")
	}
	if p.MaxUses > 0 && p.UsedCount >= p.MaxUses || p.OneTime && p.UsedCount > 0 {
		return quote, conflict("promo_exhausted", "升级优惠码使用次数已用完")
	}
	var orders int64
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM orders WHERE user_id=$1", userID).Scan(&orders); err != nil {
		return quote, err
	}
	if p.OnlyNew && orders > 0 || p.OnlyOld && orders == 0 {
		return quote, conflict("promo_customer_ineligible", "当前客户不符合升级优惠条件")
	}
	if p.OncePerCustomer {
		var used bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM promo_upgrade_redemptions WHERE promo_code_id=$1 AND user_id=$2)", p.ID, userID).Scan(&used); err != nil {
			return quote, err
		}
		if used {
			return quote, conflict("promo_already_used", "该客户已经使用过此升级优惠码")
		}
	}
	if len(p.AppliesIDs) > 0 && !containsInt64(p.AppliesIDs, targetProductID) || len(p.Cycles) > 0 && !containsString(p.Cycles, cycle) {
		return quote, conflict("promo_not_applicable", "升级优惠码不适用于目标商品或账期")
	}
	if len(p.RequiresIDs) > 0 {
		matched := containsInt64(p.RequiresIDs, targetProductID)
		if !matched && p.RequiresExisting {
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM services WHERE user_id=$1 AND product_id=ANY($2) AND status NOT IN ('terminated','cancelled'))", userID, p.RequiresIDs).Scan(&matched); err != nil {
				return quote, err
			}
		}
		if !matched {
			return quote, conflict("promo_requirement_failed", "不满足升级优惠商品条件")
		}
	}
	var discount int64
	switch p.DiscountType {
	case "percent":
		discount, err = PercentDiscount(base, p.Value)
	case "fixed":
		discount = p.Value
	default:
		return quote, conflict("promo_type_unsupported", "该升级优惠类型暂不支持")
	}
	if err != nil {
		return quote, err
	}
	if discount > base {
		discount = base
	}
	if discount <= 0 {
		return quote, conflict("promo_no_discount", "升级优惠码未产生折扣")
	}
	return UpgradePromoQuote{ID: p.ID, Code: p.Code, DiscountType: p.DiscountType, BaseCents: base, DiscountCents: discount}, nil
}

func containsInt64(values []int64, value int64) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (s *Store) CartPromoQuote(ctx context.Context, tx pgx.Tx, userID int64, cartID, code string, lock bool) (PromoQuote, int64, error) {
	if err := CatalogReadLock(ctx, tx); err != nil {
		return PromoQuote{}, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT ci.product_id,ci.quantity,ci.billing_cycle,pp.price_cents,pp.setup_fee_cents,ci.option_ids,ci.option_quantities,(p.active AND pp.active AND catalog_category_visible(p.category_id) AND ci.product_revision=p.revision),ci.purchase_fields FROM cart_items ci JOIN products p ON p.id=ci.product_id JOIN product_prices pp ON pp.product_id=ci.product_id AND pp.billing_cycle=ci.billing_cycle WHERE ci.cart_id=$1 ORDER BY ci.id`, cartID)
	if err != nil {
		return PromoQuote{}, 0, err
	}
	defer rows.Close()
	type configuredLine struct {
		line       PromoLine
		options    []byte
		quantities []byte
		current    bool
		fields     []byte
	}
	configured := []configuredLine{}
	for rows.Next() {
		var entry configuredLine
		line := &entry.line
		if err = rows.Scan(&line.ProductID, &line.Quantity, &line.Cycle, &line.BaseCents, &line.SetupCents, &entry.options, &entry.quantities, &entry.current, &entry.fields); err != nil {
			return PromoQuote{}, 0, err
		}
		configured = append(configured, entry)
	}
	if err = rows.Err(); err != nil {
		return PromoQuote{}, 0, err
	}
	rows.Close()
	lines := []PromoLine{}
	var subtotal int64
	for _, entry := range configured {
		if !entry.current {
			return PromoQuote{}, 0, conflict("price_changed", "商品价格或配置已变化，请重新确认购物车")
		}
		var fields map[string]string
		if err = json.Unmarshal(entry.fields, &fields); err != nil {
			return PromoQuote{}, 0, err
		}
		if _, _, err = ValidatePurchaseFields(ctx, tx, entry.line.ProductID, fields); err != nil {
			return PromoQuote{}, 0, err
		}
		var ids []int64
		if err = json.Unmarshal(entry.options, &ids); err != nil {
			return PromoQuote{}, 0, err
		}
		quantities := map[int64]int64{}
		if err = json.Unmarshal(entry.quantities, &quantities); err != nil {
			return PromoQuote{}, 0, err
		}
		selection, selectionErr := ValidateProductOptionsForCycleWithQuantities(ctx, tx, entry.line.ProductID, entry.line.Cycle, ids, quantities)
		if selectionErr != nil {
			return PromoQuote{}, 0, selectionErr
		}
		price, setup, amountErr := OptionAmounts(entry.line.BaseCents, entry.line.SetupCents, selection)
		if amountErr != nil {
			return PromoQuote{}, 0, amountErr
		}
		line := entry.line
		unit := price + setup
		if unit > maxInt64/int64(line.Quantity) || subtotal > maxInt64-unit*int64(line.Quantity) {
			return PromoQuote{}, 0, conflict("invalid_product_options", "配置后金额超出范围")
		}
		line.BaseCents = unit * int64(line.Quantity)
		line.SetupCents = setup * int64(line.Quantity)
		subtotal += line.BaseCents
		lines = append(lines, line)
	}
	quote, err := QuotePromo(ctx, tx, userID, code, lines, lock)
	return quote, subtotal, err
}
