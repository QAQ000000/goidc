package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type checkoutLine struct {
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
	FirstPriceOverride                                     *int64
	RenewalPriceOverride                                   *int64
	StoredFields                                           bool
	UnitPrice, UnitSetup                                   int64
	DefaultRecurring                                       int64
	SupplierSelection                                      *supplierCheckoutSelection
	Pricing                                                *PurchasePricingLine
}

type checkoutOptions struct {
	GenerateInvoice, Confirmed bool
	ActorID                    int64
	Source                     string
	ItemIDs                    []int64
}

func (s *Store) prepareCheckoutLines(ctx context.Context, tx pgx.Tx, userID int64, items []checkoutLine, promoCode string) (CheckoutResult, []RenewalBenefit, bool, error) {
	var result CheckoutResult
	var err error
	rules, err := loadGroupPricing(ctx, tx, userID)
	if err != nil {
		return result, nil, false, err
	}
	promoLines := []PromoLine{}
	requiresApproval := false
	stockUse := map[int64]int{}
	for index := range items {
		v := &items[index]
		if err = s.CheckOrderAdmission(ctx, tx, userID, v.ProductID, true); err != nil {
			return result, nil, false, err
		}
		if err = lockSupplierAtCheckout(ctx, tx, v.ProductID, v.Cycle); err != nil {
			return result, nil, false, err
		}
		requiresApproval = requiresApproval || v.ApprovalRequired
		if !v.Active || !v.PriceActive {
			return result, nil, false, conflict("product_unavailable", "商品已下架")
		}
		if v.Revision != v.CurrentRevision {
			return result, nil, false, conflict("price_changed", "商品价格或配置已变化，请重新加入购物车")
		}
		stockUse[v.ProductID] += v.Qty
		if v.Stock != nil && *v.Stock < stockUse[v.ProductID] {
			return result, nil, false, conflict("insufficient_stock", "商品库存不足")
		}
		if err = json.Unmarshal(v.OptionJSON, &v.OptionIDs); err != nil {
			return result, nil, false, conflict("invalid_product_options", "产品配置项无效")
		}
		v.OptionQuantities = map[int64]int64{}
		if err = json.Unmarshal(v.OptionQuantityJSON, &v.OptionQuantities); err != nil {
			return result, nil, false, conflict("invalid_product_options", "产品配置数量无效")
		}
		selection, selectionErr := ValidateProductOptionsForCycleWithQuantities(ctx, tx, v.ProductID, v.Cycle, v.OptionIDs, v.OptionQuantities)
		if selectionErr != nil {
			return result, nil, false, selectionErr
		}
		v.OptionIDs = selection.IDs
		v.OptionQuantities = selection.Quantities
		v.OptionPrice, v.OptionSetup = selection.PriceDeltaCents, selection.SetupDeltaCents
		v.Snapshot = selection.Snapshot
		var fieldValues map[string]string
		if err = json.Unmarshal(v.PurchaseJSON, &fieldValues); err != nil {
			return result, nil, false, err
		}
		if v.StoredFields {
			_, v.PurchaseSnapshot, err = ValidateStoredPurchaseFields(ctx, tx, userID, v.ProductID, fieldValues, s.PurchaseFieldEncryptionKey)
		} else {
			_, v.PurchaseSnapshot, err = ValidatePurchaseFieldsWithKey(ctx, tx, v.ProductID, fieldValues, s.PurchaseFieldEncryptionKey)
		}
		if err != nil {
			return result, nil, false, err
		}
		v.SupplierSelection, err = s.prepareSupplierCheckoutSelection(ctx, tx, *v)
		if err != nil {
			return result, nil, false, err
		}

		price, setup, amountErr := OptionAmounts(v.Price, v.Setup, selection)
		if amountErr != nil {
			return result, nil, false, amountErr
		}
		v.DefaultRecurring = price
		if v.FirstPriceOverride != nil {
			price, setup = *v.FirstPriceOverride, 0
			v.Price, v.Setup, v.OptionPrice, v.OptionSetup = price, 0, 0, 0
		}
		v.UnitPrice, v.UnitSetup = price, setup
		unitTotal := price + setup
		if unitTotal < 0 || (v.Qty > 0 && unitTotal > maxInt64/int64(v.Qty)) || (result.TotalCents > maxInt64-unitTotal*int64(v.Qty)) {
			return result, nil, false, conflict("invalid_product_options", "配置后金额无效")
		}
		result.TotalCents += unitTotal * int64(v.Qty)
		pricing, setupNet, priceErr := rules.price(*v)
		if priceErr != nil {
			return result, nil, false, priceErr
		}
		if rules.enabled {
			v.Pricing = &pricing
		}
		result.GroupDiscountCents += pricing.GroupDiscountCents
		promoLines = append(promoLines, PromoLine{ProductID: v.ProductID, Cycle: v.Cycle, BaseCents: pricing.NetCents, SetupCents: setupNet, Quantity: v.Qty, GroupDiscountCents: pricing.GroupDiscountCents})
	}
	result.DiscountCents = result.GroupDiscountCents
	result.TotalCents -= result.GroupDiscountCents
	renewalBenefits := make([]RenewalBenefit, len(items))
	if promoCode != "" {
		quote, quoteErr := QuotePromo(ctx, tx, userID, promoCode, promoLines, true)
		if quoteErr != nil {
			return result, nil, false, quoteErr
		}
		result.PromoDiscountCents = quote.DiscountCents
		result.DiscountCents += quote.DiscountCents
		result.PromoCode = quote.Code
		result.TotalCents -= quote.DiscountCents
		result.promoID = quote.ID
		renewalBenefits = quote.RenewalBenefits
		for i := range items {
			if items[i].Pricing != nil {
				p := items[i].Pricing
				p.PromoDiscountCents = quote.LineDiscounts[i]
				p.NetCents -= quote.LineDiscounts[i]
				p.PromoCode = quote.Code
				p.PromoVersion = quote.Version
				p.PromoCustomerGroupIDs = quote.CustomerGroupIDs
			}
		}
	}
	return result, renewalBenefits, requiresApproval, nil
}

func (s *Store) persistCheckoutLines(ctx context.Context, tx pgx.Tx, userID int64, key, requestHash string, items []checkoutLine, result CheckoutResult, renewalBenefits []RenewalBenefit, requiresApproval bool, promoID int64, options checkoutOptions) (CheckoutResult, error) {
	var err error
	reviewStatus := "approved"
	if requiresApproval && !options.Confirmed {
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
		for i := range v.PurchaseSnapshot {
			if v.PurchaseSnapshot[i].Encrypted {
				v.PurchaseSnapshot[i].Value, err = SealPurchaseValue(s.PurchaseFieldEncryptionKey, userID, v.ProductID, v.PurchaseSnapshot[i].Key, v.PurchaseSnapshot[i].Value)
				if err != nil {
					return result, conflict("purchase_field_encryption_unconfigured", "敏感购买字段未配置加密密钥")
				}
			}
		}

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
		unitTotal := v.UnitPrice + v.UnitSetup
		purchaseJSON, marshalErr := json.Marshal(v.PurchaseSnapshot)
		if marshalErr != nil {
			return result, marshalErr
		}
		if err = tx.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents,config_options,option_quantities,config_snapshot,renewal_discount_percent,renewal_promo_code,purchase_fields,renewal_fixed_discount_cents,renewal_discount_limit)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING id`, result.OrderID, v.ProductID, v.Name, v.Cycle, v.Qty, v.UnitPrice, v.UnitSetup, unitTotal*int64(v.Qty), optionJSON, optionQuantityJSON, snapshot, benefit.Percent, benefit.Code, purchaseJSON, benefit.FixedCents, benefit.Limit).Scan(&orderItemID); err != nil {
			return result, err
		}
		if v.Pricing != nil {
			if err = persistPurchasePricing(ctx, tx, orderItemID, *v.Pricing); err != nil {
				return result, err
			}
		}
		delivery, freezeErr := s.freezeSupplierCheckoutSelection(v.SupplierSelection, orderItemID)
		if freezeErr != nil {
			return result, freezeErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,unit_number,provision_mode,renewal_discount_percent,renewal_promo_code,renewal_fixed_discount_cents,renewal_discount_limit,delivery_snapshot)
SELECT $1,$2,$3,p.id,$5,'svc-'||replace(gen_random_uuid()::text,'-',''),$6,n,p.provision_mode,$8,$9,$10,$11,coalesce($12::jsonb,p.delivery_config)
FROM products p CROSS JOIN generate_series(1,$7::int) n WHERE p.id=$4`, userID, result.OrderID, orderItemID, v.ProductID, v.Name, v.Cycle, v.Qty, benefit.Percent, benefit.Code, benefit.FixedCents, benefit.Limit, delivery); err != nil {
			return result, err
		}
		if v.Pricing != nil || v.RenewalPriceOverride != nil || v.FirstPriceOverride != nil {
			renewalPrice := v.DefaultRecurring
			if v.Pricing != nil {
				renewalPrice = v.Pricing.RecurringCents
			}
			if v.RenewalPriceOverride != nil {
				renewalPrice = *v.RenewalPriceOverride
			}
			if v.Cycle == "once" {
				renewalPrice = 0
			}
			if _, err = tx.Exec(ctx, "UPDATE services SET recurring_price_cents=$2 WHERE order_item_id=$1", orderItemID, renewalPrice); err != nil {
				return result, err
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE order_items SET first_price_override_cents=$2,renewal_price_override_cents=$3 WHERE id=$1", orderItemID, v.FirstPriceOverride, v.RenewalPriceOverride); err != nil {
			return result, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO external_refund_order_stock_reservations(order_item_id,order_id,product_id,units,item_snapshot,service_ids) VALUES($1,$2,$3,$4,'{}','[]')`, orderItemID, result.OrderID, v.ProductID, v.Qty); err != nil {
			return result, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO service_navigation_preferences(user_id,group_id,visible)
 SELECT $1,service_group_id,true FROM products WHERE id=$2 AND service_group_id IS NOT NULL
 ON CONFLICT(user_id,group_id) DO UPDATE SET visible=true,version=service_navigation_preferences.version+1
 WHERE NOT service_navigation_preferences.visible`, userID, v.ProductID); err != nil {
			return result, err
		}
	}
	if options.GenerateInvoice {
		if err = tx.QueryRow(ctx, "INSERT INTO invoices(invoice_no,user_id,order_id,amount_cents) VALUES('I-'||replace(gen_random_uuid()::text,'-',''),$1,$2,$3) RETURNING id", userID, result.OrderID, result.TotalCents).Scan(&result.InvoiceID); err != nil {
			return result, err
		}
	}
	if promoID > 0 {
		if _, err = tx.Exec(ctx, "INSERT INTO promo_code_redemptions(promo_code_id,user_id,order_id,discount_cents) VALUES($1,$2,$3,$4)", promoID, userID, result.OrderID, result.PromoDiscountCents); err != nil {
			return result, err
		}
		if _, err = tx.Exec(ctx, "UPDATE promo_codes SET used_count=used_count+1,updated_at=now() WHERE id=$1", promoID); err != nil {
			return result, err
		}
	}
	payload := map[string]any{"total_cents": result.TotalCents, "source": options.Source}
	if options.ItemIDs != nil {
		payload["item_ids"] = options.ItemIDs
	}
	if err = Audit(ctx, tx, options.ActorID, "order.created", "order", result.OrderID, payload); err != nil {
		return result, err
	}
	if err = s.QueueLifecycleNotification(ctx, tx, "order.created", result.InvoiceID, result.OrderID, 0, fmt.Sprintf("order.created:%d", result.OrderID), nil); err != nil {
		return result, err
	}
	return result, nil
}
