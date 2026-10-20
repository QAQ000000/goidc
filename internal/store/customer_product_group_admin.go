package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Field order and tags preserve the existing settings-operation fingerprints.
type CustomerProductGroupInput struct {
	Name            string  `json:"name"`
	ProductIDs      []int64 `json:"product_ids"`
	ExpectedVersion int64   `json:"expected_version"`
}

type CustomerProductGroupDeleteInput struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type CustomerProductDiscountInput struct {
	CustomerGroupID int64  `json:"customer_group_id"`
	ProductGroupID  int64  `json:"product_group_id"`
	DiscountType    string `json:"discount_type"`
	DiscountPercent int    `json:"discount_percent"`
	FixedCents      int64  `json:"fixed_discount_cents"`
	ExpectedVersion int64  `json:"expected_version"`
}

type CustomerPricingResourceNotFound struct{ CustomerGroup bool }

func (e *CustomerPricingResourceNotFound) Error() string {
	if e.CustomerGroup {
		return "客户分组不存在"
	}
	return "商品分组不存在"
}

// Accounts/roles precede pricing; credentials, receipts and business rows follow
// pricing. Statement triggers remain a backstop for non-API writers.
func (s *Store) beginCustomerPricingMutation(ctx context.Context, actor int64, operation, key, hash string) (pgx.Tx, map[string]any, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, false, err
	}
	err = lockAdminPricingMutation(ctx, tx, actor, "customer_groups")
	if err != nil {
		tx.Rollback(ctx)
		return nil, nil, false, err
	}
	var out map[string]any
	replayed, err := ReplayOperation(ctx, tx, actor, "admin:"+operation, key, hash, &out)
	if err != nil {
		tx.Rollback(ctx)
		return nil, nil, false, err
	}
	if replayed {
		out["replayed"] = true
	}
	return tx, out, replayed, nil
}

func (s *Store) CreateCustomerProductGroup(ctx context.Context, actor int64, key string, in CustomerProductGroupInput) (map[string]any, bool, error) {
	const operation = "customer_product_group.create"
	hash := adminOrderHash(in)
	tx, result, replayed, err := s.beginCustomerPricingMutation(ctx, actor, operation, key, hash)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	if replayed {
		return result, true, commitAdminPricingMutation(ctx, tx, actor)
	}
	var id, version int64
	if err = tx.QueryRow(ctx, "INSERT INTO customer_product_groups(name) VALUES($1) RETURNING id,version", in.Name).Scan(&id, &version); err != nil {
		return nil, false, err
	}
	if err = ensureCustomerProductGroupProducts(ctx, tx, id, in.ProductIDs); err != nil {
		return nil, false, err
	}
	result = map[string]any{"id": id, "version": version}
	if err = Audit(ctx, tx, actor, "customer_product_group.created", "customer_product_group", id, map[string]any{"name": in.Name, "product_ids": in.ProductIDs}); err != nil {
		return nil, false, err
	}
	if err = SaveOperation(ctx, tx, actor, "admin:"+operation, key, hash, result); err != nil {
		return nil, false, err
	}
	return result, false, commitAdminPricingMutation(ctx, tx, actor)
}

func (s *Store) UpdateCustomerProductGroup(ctx context.Context, actor, id int64, key string, in CustomerProductGroupInput) (map[string]any, error) {
	hash := adminOrderHash(struct {
		ID    int64
		Input CustomerProductGroupInput
	}{id, in})
	operation := fmt.Sprintf("customer_product_group.update:%d", id)
	tx, result, replayed, err := s.beginCustomerPricingMutation(ctx, actor, operation, key, hash)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if replayed {
		return result, commitAdminPricingMutation(ctx, tx, actor)
	}
	var current int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_product_groups WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		if err == pgx.ErrNoRows {
			return nil, &CustomerPricingResourceNotFound{}
		}
		return nil, err
	}
	if current != in.ExpectedVersion {
		return nil, conflict("version_conflict", "商品分组已变更，请刷新后重试")
	}
	if _, err = UpdateVersioned(ctx, tx, "customer_product_groups", `name=$1`, id, in.ExpectedVersion, in.Name); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM customer_product_group_products WHERE group_id=$1", id); err != nil {
		return nil, err
	}
	if err = ensureCustomerProductGroupProducts(ctx, tx, id, in.ProductIDs); err != nil {
		return nil, err
	}
	result = map[string]any{"id": id, "version": current + 1}
	if err = Audit(ctx, tx, actor, "customer_product_group.updated", "customer_product_group", id, map[string]any{"expected_version": in.ExpectedVersion, "product_ids": in.ProductIDs}); err != nil {
		return nil, err
	}
	if err = SaveOperation(ctx, tx, actor, "admin:"+operation, key, hash, result); err != nil {
		return nil, err
	}
	return result, commitAdminPricingMutation(ctx, tx, actor)
}

func (s *Store) DeleteCustomerProductGroup(ctx context.Context, actor, id int64, key string, in CustomerProductGroupDeleteInput) (map[string]any, error) {
	hash := adminOrderHash(struct {
		ID    int64
		Input CustomerProductGroupDeleteInput
	}{id, in})
	operation := fmt.Sprintf("customer_product_group.delete:%d", id)
	tx, result, replayed, err := s.beginCustomerPricingMutation(ctx, actor, operation, key, hash)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if replayed {
		return result, commitAdminPricingMutation(ctx, tx, actor)
	}
	var current int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_product_groups WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		if err == pgx.ErrNoRows {
			return nil, &CustomerPricingResourceNotFound{}
		}
		return nil, err
	}
	if current != in.ExpectedVersion {
		return nil, conflict("version_conflict", "商品分组已变更，请刷新后重试")
	}
	var deleted int64
	if err = tx.QueryRow(ctx, "DELETE FROM customer_product_groups WHERE id=$1 RETURNING id", id).Scan(&deleted); err != nil {
		return nil, err
	}
	result = map[string]any{"id": deleted, "deleted": true}
	if err = Audit(ctx, tx, actor, "customer_product_group.deleted", "customer_product_group", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
		return nil, err
	}
	if err = SaveOperation(ctx, tx, actor, "admin:"+operation, key, hash, result); err != nil {
		return nil, err
	}
	return result, commitAdminPricingMutation(ctx, tx, actor)
}

func ensureCustomerProductGroupProducts(ctx context.Context, tx pgx.Tx, groupID int64, productIDs []int64) error {
	if len(productIDs) == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM products WHERE id=ANY($1)", productIDs).Scan(&count); err != nil {
		return err
	}
	if count != len(productIDs) {
		return conflict("invalid_product", "商品分组包含不存在的商品")
	}
	for _, productID := range productIDs {
		if _, err := tx.Exec(ctx, "INSERT INTO customer_product_group_products(group_id,product_id) VALUES($1,$2)", groupID, productID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveCustomerProductDiscount(ctx context.Context, actor int64, key string, in CustomerProductDiscountInput) (map[string]any, error) {
	const operation = "customer_product_discount.save"
	hash := adminOrderHash(in)
	tx, result, replayed, err := s.beginCustomerPricingMutation(ctx, actor, operation, key, hash)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if replayed {
		return result, commitAdminPricingMutation(ctx, tx, actor)
	}
	var exists int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM customer_groups WHERE id=$1", in.CustomerGroupID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, &CustomerPricingResourceNotFound{CustomerGroup: true}
	}
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM customer_product_groups WHERE id=$1", in.ProductGroupID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, &CustomerPricingResourceNotFound{}
	}
	var id, current int64
	err = tx.QueryRow(ctx, "SELECT id,version FROM customer_product_group_discounts WHERE customer_group_id=$1 AND product_group_id=$2 FOR UPDATE", in.CustomerGroupID, in.ProductGroupID).Scan(&id, &current)
	hasExisting := err == nil
	if err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	if (hasExisting && current != in.ExpectedVersion) || (!hasExisting && in.ExpectedVersion != 0) {
		return nil, conflict("version_conflict", "折扣已变更，请刷新后重试")
	}
	if in.DiscountType == "none" {
		if hasExisting {
			if _, err = tx.Exec(ctx, "DELETE FROM customer_product_group_discounts WHERE id=$1", id); err != nil {
				return nil, err
			}
		}
		result = map[string]any{"id": id, "customer_group_id": in.CustomerGroupID, "product_group_id": in.ProductGroupID, "discount_type": "none", "version": 0, "deleted": hasExisting}
	} else if hasExisting {
		next := current + 1
		if _, err = tx.Exec(ctx, `UPDATE customer_product_group_discounts SET discount_type=$2,discount_percent=$3,fixed_discount_cents=$4,version=$5,updated_at=now() WHERE id=$1`, id, in.DiscountType, in.DiscountPercent, in.FixedCents, next); err != nil {
			return nil, err
		}
		result = map[string]any{"id": id, "customer_group_id": in.CustomerGroupID, "product_group_id": in.ProductGroupID, "discount_type": in.DiscountType, "version": next}
	} else {
		if err = tx.QueryRow(ctx, `INSERT INTO customer_product_group_discounts(customer_group_id,product_group_id,discount_type,discount_percent,fixed_discount_cents) VALUES($1,$2,$3,$4,$5) RETURNING id,version`, in.CustomerGroupID, in.ProductGroupID, in.DiscountType, in.DiscountPercent, in.FixedCents).Scan(&id, &current); err != nil {
			return nil, err
		}
		result = map[string]any{"id": id, "customer_group_id": in.CustomerGroupID, "product_group_id": in.ProductGroupID, "discount_type": in.DiscountType, "version": current}
	}
	if err = Audit(ctx, tx, actor, "customer_product_discount.saved", "customer_product_group_discount", id, map[string]any{"customer_group_id": in.CustomerGroupID, "product_group_id": in.ProductGroupID, "discount_type": in.DiscountType, "expected_version": in.ExpectedVersion}); err != nil {
		return nil, err
	}
	if err = SaveOperation(ctx, tx, actor, "admin:"+operation, key, hash, result); err != nil {
		return nil, err
	}
	return result, commitAdminPricingMutation(ctx, tx, actor)
}
