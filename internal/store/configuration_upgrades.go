package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ConfigurationChangeInput struct {
	OptionIDs        []int64         `json:"option_ids"`
	OptionQuantities map[int64]int64 `json:"option_quantities"`
	ExpectedVersion  int64           `json:"expected_version"`
	ExpectedRevision int64           `json:"expected_revision"`
}

type BillingCycleChangeInput struct {
	TargetBillingCycle string `json:"target_billing_cycle"`
	ExpectedVersion    int64  `json:"expected_version"`
	ExpectedRevision   int64  `json:"expected_revision"`
}

type ConfigurationUpgradePolicy struct {
	Enabled          bool  `json:"enabled"`
	AllowDowngrade   bool  `json:"allow_downgrade"`
	ExpectedRevision int64 `json:"expected_revision"`
}

func (s *Store) SaveConfigurationUpgradePolicy(ctx context.Context, actor, productID int64, in ConfigurationUpgradePolicy, keys ...string) error {
	if !in.Enabled && in.AllowDowngrade {
		return conflict("invalid_upgrade_policy", "请先开启配置变更")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = CheckAdminCreationPermission(ctx, tx, actor, "products"); err != nil {
		return err
	}
	key := ""
	if len(keys) > 0 {
		key = keys[0]
	}
	operation := fmt.Sprintf("product.configuration_upgrade_policy.updated.%d", productID)
	var receipt struct {
		ProductID int64 `json:"product_id"`
	}
	hash, replay, err := CreationReceipt(ctx, tx, actor, operation, key, in, &receipt)
	if err != nil {
		return err
	}
	if replay {
		return tx.Commit(ctx)
	}
	var current int64
	if err = tx.QueryRow(ctx, "SELECT revision FROM products WHERE id=$1 FOR UPDATE", productID).Scan(&current); err != nil {
		return err
	}
	if in.ExpectedRevision < 1 || current != in.ExpectedRevision {
		return conflict("version_conflict", "商品配置已变更，请重新加载")
	}

	var mode string
	if err = tx.QueryRow(ctx, "SELECT provision_mode FROM products WHERE id=$1", productID).Scan(&mode); err != nil {
		return err
	}
	if in.Enabled && (mode == "supplier" || mode == "nokvm") {
		return conflict("unsupported_supplier_upgrade", "该交付方式暂不支持配置升级")
	}
	if _, err = tx.Exec(ctx, "UPDATE products SET config_upgrade_enabled=$2,config_downgrade_enabled=$3 WHERE id=$1", productID, in.Enabled, in.AllowDowngrade); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE products SET revision=revision+1,updated_at=now() WHERE id=$1", productID); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actor, "product.configuration_upgrade_policy.updated", "product", productID, map[string]any{"configuration_id": productID}); err != nil {
		return err
	}
	receipt.ProductID = productID
	if err = SaveCreationReceipt(ctx, tx, actor, operation, key, hash, receipt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type ConfigurationChange struct {
	ChangeType           string           `json:"change_type"`
	SourceProductID      *int64           `json:"source_product_id"`
	TargetProductID      *int64           `json:"target_product_id"`
	TargetRevision       int64            `json:"target_product_revision"`
	TargetName           string           `json:"target_product_name"`
	ID                   int64            `json:"id"`
	ServiceID            int64            `json:"service_id"`
	AmountCents          int64            `json:"amount_cents"`
	CreditCents          int64            `json:"credit_cents"`
	OldRecurring         int64            `json:"old_recurring_cents"`
	NewRecurring         int64            `json:"new_recurring_cents"`
	ProductRevision      int64            `json:"product_revision"`
	ServiceVersion       int64            `json:"service_version"`
	BillingCycle         string           `json:"billing_cycle"`
	NewBillingCycle      string           `json:"new_billing_cycle"`
	DueAt                time.Time        `json:"due_at"`
	ExpiresAt            time.Time        `json:"expires_at"`
	Snapshot             []OptionSnapshot `json:"config_snapshot"`
	Status               string           `json:"status"`
	InvoiceID            *int64           `json:"invoice_id"`
	JobID                *int64           `json:"job_id"`
	Replayed             bool             `json:"replayed"`
	UpgradeBaseCents     int64            `json:"upgrade_base_cents"`
	UpgradeDiscountCents int64            `json:"upgrade_discount_cents"`
	UpgradePromoCode     string           `json:"upgrade_promo_code"`
}

func nullableUpgradeBase(change ConfigurationChange) *int64 {
	if change.UpgradePromoCode == "" {
		return nil
	}
	value := change.UpgradeBaseCents
	return &value
}

func requireNoConfigurationChange(ctx context.Context, tx pgx.Tx, serviceID int64) error {
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_configuration_changes WHERE service_id=$1 AND status IN ('unpaid','queued','waiting_manual'))`, serviceID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return conflict("configuration_change_pending", "服务已有配置变更，请先完成或取消变更")
	}
	return nil
}

func validServiceBillingCycle(cycle string) bool {
	return cycle == "monthly" || cycle == "quarterly" || cycle == "annually"
}

// QuoteBillingCycleChange reuses the configuration-change settlement path. The
// current option snapshot is repriced at the target cycle and the prorated
// difference is settled only after the customer confirms the quote.
func (s *Store) QuoteBillingCycleChange(ctx context.Context, userID, serviceID int64, in BillingCycleChangeInput) (ConfigurationChange, error) {
	var out ConfigurationChange
	if !validServiceBillingCycle(in.TargetBillingCycle) || in.ExpectedVersion < 1 || in.ExpectedRevision < 1 {
		return out, conflict("invalid_billing_cycle_change", "目标账期或版本无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var supplierMode bool
	if err = tx.QueryRow(ctx, "SELECT provision_mode='supplier' FROM services WHERE id=$1 AND user_id=$2", serviceID, userID).Scan(&supplierMode); err != nil {
		return out, err
	}
	if supplierMode {
		return out, conflict("unsupported_supplier_upgrade", "该上游适配器暂未开放账期变更")
	}
	if err = CatalogReadLock(ctx, tx); err != nil {
		return out, err
	}
	var owner, productID int64
	var status, cycle string
	var due *time.Time
	var optionIDs, quantities, snapshot []byte
	err = tx.QueryRow(ctx, `SELECT s.user_id,s.product_id,s.status,s.billing_cycle,s.version,s.next_due_at,
 coalesce(s.recurring_price_cents,oi.price_cents),coalesce(s.option_ids,oi.config_options,'[]'::jsonb),coalesce(s.option_quantities,oi.option_quantities,'{}'::jsonb),coalesce(s.config_snapshot,oi.config_snapshot,'[]'::jsonb)
 FROM services s LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE s.id=$1 AND s.user_id=$2 FOR UPDATE OF s`, serviceID, userID).
		Scan(&owner, &productID, &status, &cycle, &out.ServiceVersion, &due, &out.OldRecurring, &optionIDs, &quantities, &snapshot)
	if err != nil {
		return out, err
	}
	if status != "active" || due == nil || !due.After(time.Now()) || cycle == "once" || cycle == in.TargetBillingCycle {
		return out, conflict("billing_cycle_change_unavailable", "当前服务不能变更到该账期")
	}
	if in.ExpectedVersion != out.ServiceVersion {
		return out, conflict("version_conflict", "服务已变更，请重新加载")
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return out, err
	}
	if err = requireNoConfigurationChange(ctx, tx, serviceID); err != nil {
		return out, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND status IN ('pending','running','waiting_manual','waiting_verification'))
 OR EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=$1 AND status IN ('pending','approved'))
 OR EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE ii.service_id=$1 AND ii.item_type='renew' AND i.status='unpaid')`, serviceID).Scan(&pending); err != nil {
		return out, err
	}
	if pending {
		return out, conflict("service_operation_pending", "服务已有任务、取消申请或续费账单")
	}
	var revision int64
	var changeEnabled bool
	if err = tx.QueryRow(ctx, "SELECT revision,config_upgrade_enabled FROM products WHERE id=$1 AND active", productID).Scan(&revision, &changeEnabled); err != nil {
		return out, err
	}
	if !changeEnabled {
		return out, conflict("billing_cycle_change_disabled", "该商品未开放账期变更")
	}
	if revision != in.ExpectedRevision {
		return out, conflict("version_conflict", "商品配置已变更，请重新加载")
	}
	var base int64
	if err = tx.QueryRow(ctx, "SELECT price_cents FROM product_prices WHERE product_id=$1 AND billing_cycle=$2 AND active", productID, in.TargetBillingCycle).Scan(&base); err != nil {
		return out, conflict("billing_cycle_unavailable", "目标账期没有可用价格")
	}
	var ids []int64
	if err = json.Unmarshal(optionIDs, &ids); err != nil {
		return out, conflict("configuration_snapshot_missing", "服务配置快照需要管理员复核")
	}
	var qs map[int64]int64
	if err = json.Unmarshal(quantities, &qs); err != nil {
		return out, conflict("configuration_snapshot_missing", "服务配置数量快照需要管理员复核")
	}
	selection, err := ValidateProductOptionsForCycleWithQuantities(ctx, tx, productID, in.TargetBillingCycle, ids, qs)
	if err != nil {
		return out, err
	}
	newRecurring, _, err := OptionAmounts(base, 0, selection)
	if err != nil {
		return out, err
	}
	prorated, err := prorateServiceChange(ctx, tx, cycle, *due, newRecurring-out.OldRecurring)
	if err != nil {
		return out, err
	}
	out.AmountCents, out.CreditCents = max(prorated, 0), max(-prorated, 0)
	out.ProductRevision, out.ServiceID, out.DueAt, out.Status = revision, serviceID, *due, "quoted"
	out.ChangeType, out.BillingCycle, out.NewBillingCycle = "billing_cycle", cycle, in.TargetBillingCycle
	out.NewRecurring = newRecurring
	if err = json.Unmarshal(snapshot, &out.Snapshot); err != nil {
		return out, conflict("configuration_snapshot_missing", "服务配置快照需要管理员复核")
	}
	err = tx.QueryRow(ctx, `INSERT INTO service_configuration_changes(service_id,user_id,product_revision,service_version,billing_cycle,new_billing_cycle,due_at,old_recurring_cents,new_recurring_cents,amount_cents,credit_cents,old_snapshot,new_snapshot,option_ids,option_quantities,change_type)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12,$13,$14,'billing_cycle') RETURNING id,expires_at`, serviceID, owner, revision, out.ServiceVersion, cycle, in.TargetBillingCycle, due, out.OldRecurring, out.NewRecurring, out.AmountCents, out.CreditCents, snapshot, optionIDs, quantities).Scan(&out.ID, &out.ExpiresAt)
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// Use integer rational arithmetic and round the final prorated delta to cents.
func proratedConfigurationDelta(delta, remaining, duration int64) (int64, error) {
	if remaining < 0 || duration <= 0 {
		return 0, conflict("invalid_upgrade_period", "服务账期无效")
	}
	remaining = min(remaining, duration)
	n := new(big.Int).Mul(big.NewInt(delta), big.NewInt(remaining))
	sign := n.Sign()
	n.Abs(n)
	n.Add(n, big.NewInt(duration/2))
	n.Quo(n, big.NewInt(duration))
	if sign < 0 {
		n.Neg(n)
	}
	if !n.IsInt64() {
		return 0, conflict("invalid_upgrade_price", "变更金额超出范围")
	}
	return n.Int64(), nil
}

func (s *Store) QuoteConfigurationChange(ctx context.Context, userID, serviceID int64, in ConfigurationChangeInput) (ConfigurationChange, error) {
	var out ConfigurationChange
	return withTx(ctx, s, func(tx pgx.Tx) (ConfigurationChange, error) {
		var err error
		var deliveryMode string
		if err = tx.QueryRow(ctx, "SELECT provision_mode FROM services WHERE id=$1 AND user_id=$2", serviceID, userID).Scan(&deliveryMode); err != nil {
			return out, err
		}
		if deliveryMode == "supplier" {
			return out, conflict("unsupported_supplier_upgrade", "该上游适配器暂未开放规格或账期变更")
		}
		if deliveryMode == "nokvm" {
			return out, conflict("unsupported_supplier_upgrade", "NOKVM 资源升级尚未开放")
		}
		if err = CatalogReadLock(ctx, tx); err != nil {
			return out, err
		}
		var productID, owner int64
		var status, cycle string
		var due *time.Time
		var originalJSON, originalIDsJSON []byte
		err = tx.QueryRow(ctx, `SELECT s.user_id,s.product_id,s.status,s.billing_cycle,s.version,s.next_due_at,
 coalesce(s.recurring_price_cents,oi.price_cents),coalesce(s.config_snapshot,oi.config_snapshot,'[]'::jsonb),coalesce(s.option_ids,oi.config_options,'[]'::jsonb)
 FROM services s LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE s.id=$1 AND s.user_id=$2 FOR UPDATE OF s`, serviceID, userID).
			Scan(&owner, &productID, &status, &cycle, &out.ServiceVersion, &due, &out.OldRecurring, &originalJSON, &originalIDsJSON)
		if err != nil {
			return out, err
		}
		if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
			return out, err
		}
		if status != "active" || due == nil || !due.After(time.Now()) || cycle == "once" {
			return out, conflict("configuration_change_unavailable", "只有未到期的已启用周期服务可以变更配置")
		}
		if in.ExpectedVersion != out.ServiceVersion {
			return out, conflict("version_conflict", "服务已变更，请重新加载")
		}
		if err = requireNoConfigurationChange(ctx, tx, serviceID); err != nil {
			return out, err
		}
		var pending bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND status IN ('pending','running','waiting_manual','waiting_verification'))
 OR EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=$1 AND status IN ('pending','approved'))
 OR EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE ii.service_id=$1 AND ii.item_type='renew' AND i.status='unpaid')`, serviceID).Scan(&pending); err != nil {
			return out, err
		}
		if pending {
			return out, conflict("service_operation_pending", "服务已有任务、取消申请或续费账单")
		}
		var enabled, downgrade bool
		if err = tx.QueryRow(ctx, "SELECT revision,config_upgrade_enabled,config_downgrade_enabled FROM products WHERE id=$1", productID).Scan(&out.ProductRevision, &enabled, &downgrade); err != nil {
			return out, err
		}
		if !enabled {
			return out, conflict("configuration_upgrade_disabled", "该商品未开放配置变更")
		}
		if out.ProductRevision != in.ExpectedRevision {
			return out, conflict("version_conflict", "商品配置已变更，请重新加载")
		}
		selection, err := ValidateProductOptionsForCycleWithQuantities(ctx, tx, productID, cycle, in.OptionIDs, in.OptionQuantities)
		if err != nil {
			return out, err
		}
		var original []OptionSnapshot
		if err = json.Unmarshal(originalJSON, &original); err != nil {
			return out, err
		}
		before := map[int64]OptionSnapshot{}
		var originalIDs []int64
		if err = json.Unmarshal(originalIDsJSON, &originalIDs); err != nil {
			return out, err
		}
		if len(originalIDs) != len(original) {
			return out, conflict("configuration_snapshot_missing", "服务配置快照需要管理员复核")
		}
		for _, choice := range original {
			if choice.ID < 1 || choice.GroupID < 1 || before[choice.GroupID].ID != 0 || !containsInt64(originalIDs, choice.ID) {
				return out, conflict("configuration_snapshot_missing", "服务配置快照需要管理员复核")
			}
			before[choice.GroupID] = choice
		}
		// Keep the contracted price for unchanged groups, even if catalog prices changed.
		after := map[int64]OptionSnapshot{}
		out.Snapshot = []OptionSnapshot{}
		delta := int64(0)
		changed := false
		for _, choice := range selection.Snapshot {
			old := before[choice.GroupID]
			if old.ID == choice.ID && max(old.Quantity, 1) == choice.Quantity {
				choice = old
				choice.Quantity = max(choice.Quantity, 1)
			} else {
				changed = true
			}
			after[choice.GroupID] = choice
			out.Snapshot = append(out.Snapshot, choice)
		}
		groups := map[int64]bool{}
		for id := range before {
			groups[id] = true
		}
		for id := range after {
			groups[id] = true
		}
		for groupID := range groups {
			old, next := before[groupID], after[groupID]
			if old.ID == next.ID && max(old.Quantity, 1) == max(next.Quantity, 1) {
				continue
			}
			changed = true
			if !downgrade && old.ID != 0 && (next.ID == 0 || next.PriceDeltaCents < old.PriceDeltaCents || (old.ID == next.ID && next.Quantity < max(old.Quantity, 1))) {
				return out, conflict("configuration_downgrade_disabled", "该商品未开放降配")
			}
			var allowed bool
			if err = tx.QueryRow(ctx, `SELECT upgrade_enabled AND product_id=$2 FROM product_option_groups WHERE id=$1`, groupID, productID).Scan(&allowed); err != nil {
				return out, err
			}
			if !allowed {
				return out, conflict("configuration_upgrade_disabled", "所选配置组不允许变更")
			}
			for _, id := range []int64{old.ID, next.ID} {
				if id == 0 {
					continue
				}
				if err = tx.QueryRow(ctx, `SELECT upgrade_enabled AND (id<>$2 OR NOT hidden) FROM product_options WHERE id=$1 AND group_id=$3`, id, next.ID, groupID).Scan(&allowed); err != nil {
					return out, err
				}
				if !allowed {
					return out, conflict("configuration_upgrade_disabled", "所选配置项不允许变更")
				}
			}
			change := next.PriceDeltaCents - old.PriceDeltaCents
			if (change > 0 && delta > maxInt64-change) || (change < 0 && delta < minInt64-change) {
				return out, conflict("invalid_upgrade_price", "配置变更价格超出范围")
			}
			delta += change
		}
		if !changed {
			return out, conflict("configuration_unchanged", "配置未发生变更")
		}
		if (delta > 0 && out.OldRecurring > maxInt64-delta) || (delta < 0 && out.OldRecurring < -delta) {
			return out, conflict("invalid_upgrade_price", "变更后服务周期金额无效")
		}
		out.NewRecurring = out.OldRecurring + delta
		if delta < 0 && !downgrade {
			return out, conflict("configuration_downgrade_disabled", "该商品未开放降配")
		}
		prorated, err := prorateServiceChange(ctx, tx, cycle, *due, delta)
		if err != nil {
			return out, err
		}
		out.AmountCents, out.CreditCents = max(prorated, 0), max(-prorated, 0)
		out.DueAt, out.ServiceID, out.Status = *due, serviceID, "quoted"
		out.ChangeType = "configuration"
		newJSON, _ := json.Marshal(out.Snapshot)
		idsJSON, _ := json.Marshal(selection.IDs)
		quantitiesJSON, _ := json.Marshal(selection.Quantities)
		err = tx.QueryRow(ctx, `INSERT INTO service_configuration_changes(service_id,user_id,product_revision,service_version,billing_cycle,new_billing_cycle,due_at,old_recurring_cents,new_recurring_cents,amount_cents,credit_cents,old_snapshot,new_snapshot,option_ids,option_quantities)
	 VALUES($1,$2,$3,$4,$5,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id,expires_at`, serviceID, owner, out.ProductRevision, out.ServiceVersion, cycle, due, out.OldRecurring, out.NewRecurring, out.AmountCents, out.CreditCents, originalJSON, newJSON, idsJSON, quantitiesJSON).Scan(&out.ID, &out.ExpiresAt)
		if err != nil {
			return out, err
		}
		return out, nil
	})
}

const configurationChangeColumns = `id,service_id,amount_cents,credit_cents,old_recurring_cents,new_recurring_cents,product_revision,service_version,billing_cycle,new_billing_cycle,due_at,expires_at,new_snapshot,status,invoice_id,job_id,change_type,source_product_id,target_product_id,coalesce(target_product_revision,0),coalesce(target_product_name,''),upgrade_base_cents,upgrade_discount_cents,upgrade_promo_code`

func scanConfigurationChange(row pgx.Row) (ConfigurationChange, error) {
	var out ConfigurationChange
	var snapshot []byte
	err := row.Scan(&out.ID, &out.ServiceID, &out.AmountCents, &out.CreditCents, &out.OldRecurring, &out.NewRecurring, &out.ProductRevision, &out.ServiceVersion, &out.BillingCycle, &out.NewBillingCycle, &out.DueAt, &out.ExpiresAt, &snapshot, &out.Status, &out.InvoiceID, &out.JobID, &out.ChangeType, &out.SourceProductID, &out.TargetProductID, &out.TargetRevision, &out.TargetName, &out.UpgradeBaseCents, &out.UpgradeDiscountCents, &out.UpgradePromoCode)
	if err == nil {
		err = json.Unmarshal(snapshot, &out.Snapshot)
	}
	return out, err
}

func (s *Store) SubmitConfigurationChange(ctx context.Context, userID, serviceID, quoteID int64, key string) (ConfigurationChange, error) {
	var out ConfigurationChange
	if len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_upgrade_key", "幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = CatalogReadLock(ctx, tx); err != nil {
		return out, err
	}
	if err = lockOperationKey(ctx, tx, "configuration-change", fmt.Sprintf("%d:%s", userID, key)); err != nil {
		return out, err
	}
	out, err = scanConfigurationChange(tx.QueryRow(ctx, "SELECT "+configurationChangeColumns+" FROM service_configuration_changes WHERE user_id=$1 AND idempotency_key=$2", userID, key))
	if err == nil {
		if out.ID != quoteID || out.ServiceID != serviceID {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同配置变更")
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if err = lockOperationKey(ctx, tx, "configuration-change-service", fmt.Sprintf("%d:%d", userID, serviceID)); err != nil {
		return out, err
	}
	var version, productID int64
	var status string
	if err = tx.QueryRow(ctx, "SELECT version,product_id,status FROM services WHERE id=$1 AND user_id=$2 FOR UPDATE", serviceID, userID).Scan(&version, &productID, &status); err != nil {
		return out, err
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return out, err
	}
	out, err = scanConfigurationChange(tx.QueryRow(ctx, "SELECT "+configurationChangeColumns+" FROM service_configuration_changes WHERE id=$1 AND service_id=$2 AND user_id=$3 FOR UPDATE", quoteID, serviceID, userID))
	if err != nil {
		return out, err
	}
	if out.Status != "quoted" || !out.ExpiresAt.After(time.Now()) {
		return out, conflict("upgrade_quote_expired", "报价已提交或过期，请重新报价")
	}
	var revision int64
	if err = tx.QueryRow(ctx, "SELECT revision FROM products WHERE id=$1", productID).Scan(&revision); err != nil {
		return out, err
	}
	if version != out.ServiceVersion || revision != out.ProductRevision || status != "active" {
		return out, conflict("version_conflict", "服务或商品已变更，请重新报价")
	}
	if err = requireNoConfigurationChange(ctx, tx, serviceID); err != nil {
		return out, err
	}
	if out.UpgradePromoCode != "" {
		if out.ChangeType != "product" || out.TargetProductID == nil {
			return out, conflict("upgrade_promo_invalid", "升级优惠仅适用于商品升级")
		}
		promo, promoErr := QuoteUpgradePromo(ctx, tx, userID, out.UpgradePromoCode, out.UpgradeBaseCents, *out.TargetProductID, out.BillingCycle, true)
		if promoErr != nil {
			return out, promoErr
		}
		if promo.DiscountCents != out.UpgradeDiscountCents {
			return out, conflict("promo_changed", "升级优惠规则已变化，请重新报价")
		}
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND status IN ('pending','running','waiting_manual','waiting_verification'))
 OR EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=$1 AND status IN ('pending','approved'))
 OR EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE ii.service_id=$1 AND ii.item_type='renew' AND i.status='unpaid')`, serviceID).Scan(&pending); err != nil {
		return out, err
	}
	if pending || !out.DueAt.After(time.Now()) {
		return out, conflict("service_operation_pending", "服务状态已变化，请重新报价")
	}
	if err = reserveProductChange(ctx, tx, out, productID); err != nil {
		return out, err
	}
	description := "服务配置变更"
	if out.ChangeType == "product" {
		description = "商品升级：" + out.TargetName
	}
	var invoice int64
	if err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,idempotency_key) VALUES('U-'||replace(gen_random_uuid()::text,'-',''),$1,'unpaid',$2,$3) RETURNING id`, userID, out.AmountCents, key).Scan(&invoice); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id,upgrade_base_cents,upgrade_discount_cents,upgrade_promo_code) VALUES($1,$4,'upgrade',$2,$3,$5,$6,$7)`, invoice, out.AmountCents, serviceID, description, nullableUpgradeBase(out), out.UpgradeDiscountCents, out.UpgradePromoCode); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE service_configuration_changes SET status='unpaid',invoice_id=$2,idempotency_key=$3,updated_at=now() WHERE id=$1`, quoteID, invoice, key); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, userID, "service.configuration.submitted", "service", serviceID, map[string]any{"change_id": quoteID, "invoice_id": invoice, "amount_cents": out.AmountCents, "credit_cents": out.CreditCents}); err != nil {
		return out, err
	}
	out.InvoiceID, out.Status = &invoice, "unpaid"
	return out, tx.Commit(ctx)
}

func queueConfigurationChange(ctx context.Context, tx pgx.Tx, invoiceID int64) error {
	var changeID, serviceID, userID, base, discount int64
	var targetProduct pgtype.Int8
	var promoCode, cycle string
	if err := tx.QueryRow(ctx, `SELECT id,service_id,user_id,upgrade_base_cents,upgrade_discount_cents,upgrade_promo_code,target_product_id,billing_cycle FROM service_configuration_changes WHERE invoice_id=$1 AND status='unpaid'`, invoiceID).Scan(&changeID, &serviceID, &userID, &base, &discount, &promoCode, &targetProduct, &cycle); err != nil {
		return err
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT s.status='active' AND s.billing_cycle=c.billing_cycle AND s.next_due_at=c.due_at AND s.next_due_at>now() AND coalesce(s.recurring_price_cents,oi.price_cents)=c.old_recurring_cents
 FROM services s JOIN service_configuration_changes c ON c.service_id=s.id LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE c.id=$1 FOR UPDATE OF s`, changeID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return conflict("upgrade_service_changed", "服务状态已变化，不能支付此变更账单")
	}
	if err := requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return err
	}
	if promoCode != "" {
		if !targetProduct.Valid {
			return conflict("upgrade_promo_invalid", "升级优惠仅适用于商品升级")
		}
		promo, err := QuoteUpgradePromo(ctx, tx, userID, promoCode, base, targetProduct.Int64, cycle, true)
		if err != nil {
			return err
		}
		if promo.DiscountCents != discount {
			return conflict("promo_changed", "升级优惠规则已变化，请重新报价")
		}
		if _, err = tx.Exec(ctx, "UPDATE promo_codes SET used_count=used_count+1,updated_at=now() WHERE id=$1", promo.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO promo_upgrade_redemptions(promo_code_id,user_id,change_id,discount_cents) VALUES($1,$2,$3,$4)", promo.ID, userID, changeID, discount); err != nil {
			return err
		}
	}
	var jobID int64
	if err := tx.QueryRow(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key) VALUES($1,'upgrade',$2) RETURNING id`, serviceID, fmt.Sprintf("configuration-change-%d", changeID)).Scan(&jobID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE service_configuration_changes SET status='queued',job_id=$2,updated_at=now() WHERE id=$1`, changeID, jobID)
	return err
}

func completeConfigurationChange(ctx context.Context, tx pgx.Tx, job ProvisionJob) error {
	var id, userID, price, credit int64
	var kind, newCycle string
	var snapshot, ids, quantities []byte
	if err := tx.QueryRow(ctx, `SELECT c.id,c.user_id,c.new_recurring_cents,c.credit_cents,c.new_snapshot,c.option_ids,c.option_quantities,c.change_type,c.new_billing_cycle
 FROM service_configuration_changes c JOIN invoices i ON i.id=c.invoice_id WHERE c.job_id=$1 AND c.service_id=$2 AND c.status IN ('queued','waiting_manual') AND i.status='paid' FOR UPDATE OF c`, job.ID, job.ServiceID).Scan(&id, &userID, &price, &credit, &snapshot, &ids, &quantities, &kind, &newCycle); err != nil {
		return err
	}
	if kind == "product" {
		if err := completeProductChange(ctx, tx, id, job.ServiceID); err != nil {
			return err
		}
	}
	if credit > 0 {
		var balance int64
		if err := tx.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1 FOR NO KEY UPDATE", userID).Scan(&balance); err != nil {
			return err
		}
		if balance > maxInt64-credit {
			return conflict("balance_overflow", "余额超出上限")
		}
		balance += credit
		if _, err := tx.Exec(ctx, "UPDATE users SET balance_cents=$2,updated_at=now() WHERE id=$1", userID, balance); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note) VALUES($1,'adjustment',$2,$3,'configuration_change',$4,$5,'降配差额返还')`, userID, credit, balance, id, fmt.Sprintf("configuration-credit-%d", id)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE services SET billing_cycle=$6,recurring_price_cents=$2,config_snapshot=$3,option_ids=$4,option_quantities=$5,version=version+1,last_error='',updated_at=now() WHERE id=$1`, job.ServiceID, price, snapshot, ids, quantities, newCycle); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE service_configuration_changes SET status='completed',completed_at=now(),updated_at=now() WHERE id=$1`, id)
	return err
}

func lockConfigurationChangeBalance(ctx context.Context, tx pgx.Tx, jobID, serviceID int64) error {
	// Finance takes the balance lock before service locks. Preserve that order
	// when delivery may issue a downgrade credit.
	var id int64
	err := tx.QueryRow(ctx, `SELECT u.id FROM users u JOIN service_configuration_changes c ON c.user_id=u.id WHERE c.job_id=$1 AND c.service_id=$2 AND c.credit_cents>0 FOR NO KEY UPDATE OF u`, jobID, serviceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func (s *Store) CancelConfigurationChange(ctx context.Context, userID, serviceID, changeID int64) error {
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		// Serialize the quoted-to-unpaid transition before discovering its invoice.
		if err = lockOperationKey(ctx, tx, "configuration-change-service", fmt.Sprintf("%d:%d", userID, serviceID)); err != nil {
			return err
		}
		var invoice *int64
		if err = tx.QueryRow(ctx, "SELECT invoice_id FROM service_configuration_changes WHERE id=$1 AND service_id=$2 AND user_id=$3", changeID, serviceID, userID).Scan(&invoice); err != nil {
			return err
		}
		if invoice != nil {
			if _, err = tx.Exec(ctx, "SELECT id FROM invoices WHERE id=$1 FOR UPDATE", *invoice); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, "SELECT id FROM services WHERE id=$1 AND user_id=$2 FOR UPDATE", serviceID, userID); err != nil {
			return err
		}
		var status string
		if err = tx.QueryRow(ctx, "SELECT status FROM service_configuration_changes WHERE id=$1 FOR UPDATE", changeID).Scan(&status); err != nil {
			return err
		}
		if status == "cancelled" {
			return nil
		}
		if status != "unpaid" && status != "quoted" {
			return conflict("configuration_change_not_cancellable", "已付款或交付的变更不能直接取消")
		}
		if invoice != nil {
			if err = requireNoRealPaymentPending(ctx, tx, *invoice); err != nil {
				return err
			}
		}
		if err = releaseProductChangeStock(ctx, tx, changeID); err != nil {
			return err
		}
		if invoice != nil {
			if _, err = tx.Exec(ctx, "UPDATE invoices SET status='cancelled' WHERE id=$1 AND status='unpaid'", *invoice); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE service_configuration_changes SET status='cancelled',updated_at=now() WHERE id=$1", changeID); err != nil {
			return err
		}
		if err = Audit(ctx, tx, userID, "service.configuration.cancelled", "service", serviceID, map[string]any{"change_id": changeID}); err != nil {
			return err
		}
		return nil
	})
}

func (s *Store) RetryConfigurationChange(ctx context.Context, actorID, serviceID, changeID int64) error {
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
			return err
		}
		var allowed bool
		if err = tx.QueryRow(ctx, `SELECT coalesce(u.role='super_admin' OR (u.role='admin' AND r.active AND 'services.view'=ANY(r.permissions) AND 'services.manage'=ANY(r.permissions)),false)
 FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, actorID).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return conflict("permission_denied", "没有管理服务的权限")
		}
		if err = LockAdminResourceCustomer(ctx, tx, actorID, "service", serviceID); err != nil {
			return err
		}
		var state string
		if err = tx.QueryRow(ctx, "SELECT status FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&state); err != nil {
			return err
		}
		if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
			return err
		}
		if state != "active" {
			return conflict("configuration_change_unavailable", "服务状态不能重试配置变更")
		}
		var jobID int64
		var status string
		if err = tx.QueryRow(ctx, `SELECT j.id,j.status FROM service_configuration_changes c JOIN provision_jobs j ON j.id=c.job_id JOIN invoices i ON i.id=c.invoice_id WHERE c.id=$1 AND c.service_id=$2 AND c.status='queued' AND i.status='paid' FOR UPDATE OF c,j`, changeID, serviceID).Scan(&jobID, &status); err != nil {
			return err
		}
		if status != "failed" {
			return conflict("configuration_change_retry_unavailable", "只有失败的配置交付任务可以重试")
		}
		if _, err = tx.Exec(ctx, "UPDATE provision_jobs SET status='pending',locked_at=NULL,available_at=now(),completed_at=NULL,last_error='',updated_at=now() WHERE id=$1", jobID); err != nil {
			return err
		}
		if err = Audit(ctx, tx, actorID, "service.configuration.retried", "service", serviceID, map[string]any{"change_id": changeID, "job_id": jobID}); err != nil {
			return err
		}
		return nil
	})
}
