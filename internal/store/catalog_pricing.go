package store

import (
	"context"

	"github.com/codex/zjmfgo/internal/supplier"
)

type ProductPricingInput struct {
	BillingCycle     string `json:"billing_cycle"`
	PriceCents       int64  `json:"price_cents"`
	SetupFeeCents    int64  `json:"setup_fee_cents"`
	Active           *bool  `json:"active"`
	Default          bool   `json:"default"`
	ExpectedRevision *int64 `json:"expected_revision"`
}

type ProductPricingResult struct {
	ID            int64  `json:"id"`
	ProductID     int64  `json:"product_id"`
	BillingCycle  string `json:"billing_cycle"`
	PriceCents    int64  `json:"price_cents"`
	SetupFeeCents int64  `json:"setup_fee_cents"`
	Active        bool   `json:"active"`
	Default       bool   `json:"default"`
}

func (s *Store) SaveProductPricing(ctx context.Context, actor, id int64, in ProductPricingInput) (ProductPricingResult, error) {
	out := ProductPricingResult{ProductID: id, BillingCycle: in.BillingCycle, PriceCents: in.PriceCents, SetupFeeCents: in.SetupFeeCents, Active: true}
	if !supplier.IsBillingCycle(in.BillingCycle) {
		return out, &AccountBusinessError{400, "invalid_cycle", "账期无效"}
	}
	if in.PriceCents < 0 || in.PriceCents > 100000000 || in.SetupFeeCents < 0 || in.SetupFeeCents > 100000000 {
		return out, &AccountBusinessError{400, "invalid_pricing", "价格或初装费无效"}
	}
	if in.Active != nil {
		out.Active = *in.Active
	}
	if in.Default && !out.Active {
		return out, &AccountBusinessError{400, "invalid_pricing", "默认账期不能停用"}
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var revision int64
	var defaultCycle, provisionMode string
	if err = tx.QueryRow(ctx, "SELECT revision,billing_cycle,provision_mode FROM products WHERE id=$1 FOR UPDATE", id).Scan(&revision, &defaultCycle, &provisionMode); err != nil {
		return out, err
	}
	if provisionMode != "supplier" && in.BillingCycle != "monthly" && in.BillingCycle != "quarterly" && in.BillingCycle != "annually" && in.BillingCycle != "once" {
		return out, &AccountBusinessError{400, "invalid_cycle", "该商品不支持此账期"}
	}
	if in.ExpectedRevision != nil && *in.ExpectedRevision != revision {
		return out, conflict("version_conflict", "商品已变更，请重新加载")
	}
	if !out.Active && defaultCycle == in.BillingCycle {
		return out, &AccountBusinessError{400, "invalid_pricing", "默认账期不能停用，请先切换默认账期"}
	}
	if out.Active {
		if err = RequireSupplierProductCycle(ctx, tx, id, in.BillingCycle); err != nil {
			return out, err
		}
	}
	err = tx.QueryRow(ctx, `INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents,active)
VALUES($1,$2,$3,$4,$5)
ON CONFLICT(product_id,billing_cycle) DO UPDATE SET price_cents=EXCLUDED.price_cents,setup_fee_cents=EXCLUDED.setup_fee_cents,active=EXCLUDED.active,updated_at=now()
RETURNING id`, id, in.BillingCycle, in.PriceCents, in.SetupFeeCents, out.Active).Scan(&out.ID)
	if err != nil {
		return out, err
	}
	out.Default = in.Default || in.BillingCycle == defaultCycle
	if out.Default {
		_, err = tx.Exec(ctx, "UPDATE products SET billing_cycle=$2,price_cents=$3,setup_fee_cents=$4,revision=revision+1,updated_at=now() WHERE id=$1", id, in.BillingCycle, in.PriceCents, in.SetupFeeCents)
	} else {
		_, err = tx.Exec(ctx, "UPDATE products SET revision=revision+1,updated_at=now() WHERE id=$1", id)
	}
	if err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "product.pricing.updated", "product", id, in); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
