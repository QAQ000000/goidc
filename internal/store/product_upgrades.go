package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ProductUpgradeTarget struct {
	ProductID      int64 `json:"product_id"`
	AllowDowngrade bool  `json:"allow_downgrade"`
}
type ProductUpgradeTargetsInput struct {
	ExpectedRevision int64                  `json:"expected_revision"`
	Targets          []ProductUpgradeTarget `json:"targets"`
}
type ProductChangeInput struct {
	TargetProductID int64  `json:"target_product_id"`
	TargetRevision  int64  `json:"target_revision"`
	PromoCode       string `json:"promo_code"`
	ConfigurationChangeInput
}

func (s *Store) SaveProductUpgradeTargets(ctx context.Context, actor, productID int64, in ProductUpgradeTargetsInput) error {
	if len(in.Targets) > 100 {
		return conflict("invalid_upgrade_targets", "升级目标最多100个")
	}
	tx, err := s.configurationTx(ctx, actor, productID, &in.ExpectedRevision, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var kind, mode string
	if err = tx.QueryRow(ctx, "SELECT product_type,provision_mode FROM products WHERE id=$1", productID).Scan(&kind, &mode); err != nil {
		return err
	}
	if (mode == "nokvm" || mode == "supplier") && len(in.Targets) > 0 {
		return conflict("unsupported_supplier_upgrade", "该交付方式暂不支持商品升级，请联系管理员处理")
	}
	seen := map[int64]bool{}
	for _, target := range in.Targets {
		if target.ProductID < 1 || target.ProductID == productID || seen[target.ProductID] {
			return conflict("invalid_upgrade_targets", "升级目标不能重复或指向当前商品")
		}
		seen[target.ProductID] = true
		var compatible bool
		if err = tx.QueryRow(ctx, "SELECT product_type=$2 AND provision_mode=$3 FROM products WHERE id=$1", target.ProductID, kind, mode).Scan(&compatible); err != nil {
			return err
		}
		if !compatible {
			return conflict("incompatible_upgrade_target", "升级目标需具有相同产品类型和交付模式")
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM product_upgrade_targets WHERE product_id=$1", productID); err != nil {
		return err
	}
	for _, target := range in.Targets {
		if _, err = tx.Exec(ctx, "INSERT INTO product_upgrade_targets(product_id,target_product_id,allow_downgrade) VALUES($1,$2,$3)", productID, target.ProductID, target.AllowDowngrade); err != nil {
			return err
		}
	}
	return configurationCommit(ctx, tx, actor, productID, productID, "product.upgrade_targets.updated")
}

func (s *Store) QuoteProductChange(ctx context.Context, userID, serviceID int64, in ProductChangeInput) (ConfigurationChange, error) {
	var out ConfigurationChange
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = CatalogReadLock(ctx, tx); err != nil {
		return out, err
	}
	var source int64
	var status, cycle, mode, kind string
	var due *time.Time
	var original []byte
	var released bool
	if err = tx.QueryRow(ctx, `SELECT s.product_id,s.status,s.billing_cycle,s.provision_mode,p.product_type,s.version,s.next_due_at,s.inventory_released,
 coalesce(s.recurring_price_cents,oi.price_cents),coalesce(s.config_snapshot,oi.config_snapshot,'[]'::jsonb)
 FROM services s JOIN products p ON p.id=s.product_id LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE s.id=$1 AND s.user_id=$2 FOR UPDATE OF s`, serviceID, userID).Scan(&source, &status, &cycle, &mode, &kind, &out.ServiceVersion, &due, &released, &out.OldRecurring, &original); err != nil {
		return out, err
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return out, err
	}
	if mode == "nokvm" || mode == "supplier" {
		return out, conflict("unsupported_supplier_upgrade", "该交付方式暂不支持商品升级，请联系管理员处理")
	}
	if status != "active" || due == nil || !due.After(time.Now()) || released || (cycle != "monthly" && cycle != "quarterly" && cycle != "annually") {
		return out, conflict("product_change_unavailable", "只有未到期且已启用的月、季、年服务可以升级商品")
	}
	if in.ExpectedVersion != out.ServiceVersion {
		return out, conflict("version_conflict", "服务已变更，请重新加载")
	}
	if err = requireServiceChangeIdle(ctx, tx, serviceID); err != nil {
		return out, err
	}
	var downgrade, visible, compatible, approval bool
	var stock *int
	var price int64
	if err = tx.QueryRow(ctx, `SELECT src.revision,t.allow_downgrade,p.revision,p.name,p.active AND catalog_category_visible(p.category_id) AND pp.active,
 p.product_type=$4 AND p.provision_mode=$5,p.approval_required,p.stock,pp.price_cents
 FROM product_upgrade_targets t JOIN products src ON src.id=t.product_id JOIN products p ON p.id=t.target_product_id
 JOIN product_prices pp ON pp.product_id=p.id AND pp.billing_cycle=$3 WHERE t.product_id=$1 AND t.target_product_id=$2`, source, in.TargetProductID, cycle, kind, mode).Scan(&out.ProductRevision, &downgrade, &out.TargetRevision, &out.TargetName, &visible, &compatible, &approval, &stock, &price); err != nil {
		return out, err
	}
	if out.ProductRevision != in.ExpectedRevision || out.TargetRevision != in.TargetRevision {
		return out, conflict("version_conflict", "商品已变更，请重新加载")
	}
	if !visible || !compatible || (approval && mode != "manual") {
		return out, conflict("product_change_unavailable", "升级商品不可用或需要人工交付")
	}
	if stock != nil && *stock < 1 {
		return out, conflict("insufficient_stock", "升级目标库存不足")
	}
	selection, err := ValidateProductOptionsForCycleWithQuantities(ctx, tx, in.TargetProductID, cycle, in.OptionIDs, in.OptionQuantities)
	if err != nil {
		return out, err
	}
	var hidden bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM product_options WHERE id=ANY($1) AND hidden)", selection.IDs).Scan(&hidden); err != nil {
		return out, err
	}
	if hidden {
		return out, conflict("product_change_unavailable", "升级商品所选配置不可用")
	}
	// Target setup fees do not form part of an existing service's upgrade contract.
	recurring, _, err := OptionAmounts(price, 0, OptionSelection{PriceDeltaCents: selection.PriceDeltaCents})
	if err != nil {
		return out, err
	}
	delta := recurring - out.OldRecurring
	if delta < 0 && !downgrade {
		return out, conflict("product_downgrade_disabled", "当前升级路径不允许降低周期金额")
	}
	prorated, err := prorateServiceChange(ctx, tx, cycle, *due, delta)
	if err != nil {
		return out, err
	}
	out.ChangeType = "product"
	out.SourceProductID = &source
	out.TargetProductID = &in.TargetProductID
	out.NewRecurring = recurring
	out.AmountCents = max(prorated, 0)
	out.CreditCents = max(-prorated, 0)
	if strings.TrimSpace(in.PromoCode) != "" {
		promo, promoErr := QuoteUpgradePromo(ctx, tx, userID, in.PromoCode, out.AmountCents, in.TargetProductID, cycle, false)
		if promoErr != nil {
			return out, promoErr
		}
		out.UpgradeBaseCents = promo.BaseCents
		out.UpgradeDiscountCents = promo.DiscountCents
		out.UpgradePromoCode = promo.Code
		out.AmountCents -= promo.DiscountCents
	}
	out.DueAt = *due
	out.ServiceID = serviceID
	out.Status = "quoted"
	out.Snapshot = selection.Snapshot
	snapshot, _ := json.Marshal(selection.Snapshot)
	ids, _ := json.Marshal(selection.IDs)
	qty, _ := json.Marshal(selection.Quantities)
	if err = tx.QueryRow(ctx, `INSERT INTO service_configuration_changes(service_id,user_id,product_revision,service_version,billing_cycle,new_billing_cycle,due_at,old_recurring_cents,new_recurring_cents,amount_cents,credit_cents,old_snapshot,new_snapshot,option_ids,option_quantities,change_type,source_product_id,target_product_id,target_product_revision,target_product_name,upgrade_base_cents,upgrade_discount_cents,upgrade_promo_code)
	 VALUES($1,$2,$3,$4,$5,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'product',$15,$16,$17,$18,$19,$20,$21) RETURNING id,expires_at`, serviceID, userID, out.ProductRevision, out.ServiceVersion, cycle, due, out.OldRecurring, recurring, out.AmountCents, out.CreditCents, original, snapshot, ids, qty, source, in.TargetProductID, out.TargetRevision, out.TargetName, out.UpgradeBaseCents, out.UpgradeDiscountCents, out.UpgradePromoCode).Scan(&out.ID, &out.ExpiresAt); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func requireServiceChangeIdle(ctx context.Context, tx pgx.Tx, serviceID int64) error {
	if err := requireNoConfigurationChange(ctx, tx, serviceID); err != nil {
		return err
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND status IN ('pending','running','waiting_manual','waiting_verification'))
 OR EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=$1 AND status IN ('pending','approved'))
 OR EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE ii.service_id=$1 AND ii.item_type='renew' AND i.status='unpaid')`, serviceID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return conflict("service_operation_pending", "服务已有任务、取消申请或续费账单")
	}
	return nil
}

func prorateServiceChange(ctx context.Context, tx pgx.Tx, cycle string, due time.Time, delta int64) (int64, error) {
	if cycle != "monthly" && cycle != "quarterly" && cycle != "annually" {
		return 0, conflict("unsupported_upgrade_cycle", "当前服务账期尚不支持变更")
	}
	var remaining, duration int64
	err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT greatest(0,floor(extract(epoch FROM ($1::timestamptz-clock_timestamp()))))::bigint,
 floor(extract(epoch FROM ($1::timestamptz-($1::timestamptz-interval '%s'))))::bigint`, renewalInterval(cycle)), due).Scan(&remaining, &duration)
	if err != nil {
		return 0, err
	}
	return proratedConfigurationDelta(delta, remaining, duration)
}

func reserveProductChange(ctx context.Context, tx pgx.Tx, change ConfigurationChange, source int64) error {
	if change.ChangeType != "product" {
		return nil
	}
	var revision int64
	var available bool
	var stock *int
	if err := tx.QueryRow(ctx, `SELECT p.revision,p.active AND catalog_category_visible(p.category_id) AND p.product_type=src.product_type AND p.provision_mode=s.provision_mode AND pp.active AND NOT s.inventory_released,p.stock
 FROM products p JOIN products src ON src.id=$2 JOIN services s ON s.id=$3 JOIN product_prices pp ON pp.product_id=p.id AND pp.billing_cycle=s.billing_cycle
 WHERE p.id=$1`, change.TargetProductID, source, change.ServiceID).Scan(&revision, &available, &stock); err != nil {
		return err
	}
	if change.SourceProductID == nil || *change.SourceProductID != source || revision != change.TargetRevision || !available {
		return conflict("version_conflict", "升级目标或服务已变化，请重新报价")
	}
	// Checkout and other upgrades take the same target row lock before decrement.
	if err := tx.QueryRow(ctx, "SELECT stock FROM products WHERE id=$1 FOR UPDATE", change.TargetProductID).Scan(&stock); err != nil {
		return err
	}
	if stock != nil {
		if *stock < 1 {
			return conflict("insufficient_stock", "升级目标库存不足")
		}
		if _, err := tx.Exec(ctx, "UPDATE products SET stock=stock-1,updated_at=now() WHERE id=$1", change.TargetProductID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE service_configuration_changes SET target_stock_reserved=true WHERE id=$1", change.ID); err != nil {
			return err
		}
	}
	return nil
}

func releaseProductChangeStock(ctx context.Context, tx pgx.Tx, changeID int64) error {
	_, err := tx.Exec(ctx, `WITH released AS (UPDATE service_configuration_changes SET target_stock_reserved=false WHERE id=$1 AND target_stock_reserved RETURNING target_product_id)
 UPDATE products p SET stock=p.stock+1,updated_at=now() FROM released r WHERE p.id=r.target_product_id AND p.stock IS NOT NULL`, changeID)
	return err
}

func completeProductChange(ctx context.Context, tx pgx.Tx, changeID, serviceID int64) error {
	var source, target, userID int64
	var name string
	if err := tx.QueryRow(ctx, "SELECT source_product_id,target_product_id,target_product_name,user_id FROM service_configuration_changes WHERE id=$1 AND change_type='product'", changeID).Scan(&source, &target, &name, &userID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "SELECT id FROM products WHERE id=ANY($1) ORDER BY id FOR UPDATE", []int64{source, target})
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var valid bool
	if err = tx.QueryRow(ctx, "SELECT product_id=$2 AND NOT inventory_released FROM services WHERE id=$1", serviceID, source).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return conflict("upgrade_service_changed", "服务商品或库存状态已变化")
	}
	if _, err = tx.Exec(ctx, "UPDATE products SET stock=stock+1,updated_at=now() WHERE id=$1 AND stock IS NOT NULL", source); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE services SET product_id=$2,name=$3 WHERE id=$1", serviceID, target, name); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE service_configuration_changes SET target_stock_reserved=false WHERE id=$1", changeID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM service_option_selections WHERE service_id=$1", serviceID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO service_navigation_preferences(user_id,group_id,visible)
 SELECT $1,service_group_id,true FROM products WHERE id=$2 AND service_group_id IS NOT NULL
 ON CONFLICT(user_id,group_id) DO UPDATE SET visible=true,version=service_navigation_preferences.version+1
 WHERE NOT service_navigation_preferences.visible`, userID, target)
	return err
}
