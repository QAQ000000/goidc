package store

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// Field order and JSON tags retain fingerprints of the former HTTP inputs.
type CustomerGroupCreateInput struct {
	Name                string `json:"name"`
	Color               string `json:"color"`
	DiscountPercent     int    `json:"discount_percent"`
	SuspendDeleteExempt bool   `json:"suspend_delete_exempt"`
	SeparateInvoices    bool   `json:"separate_invoices"`
}
type CustomerGroupPatchInput struct {
	Name                *string `json:"name"`
	Color               *string `json:"color"`
	DiscountPercent     *int    `json:"discount_percent"`
	SuspendDeleteExempt *bool   `json:"suspend_delete_exempt"`
	SeparateInvoices    *bool   `json:"separate_invoices"`
	ExpectedVersion     int64   `json:"expected_version"`
}
type CustomerGroupDeleteInput struct {
	ExpectedVersion int64 `json:"expected_version"`
}

func customerGroupAdminReceipt(ctx context.Context, tx pgx.Tx, actor int64, operation, key string, payload any) (string, map[string]any, bool, error) {
	out := map[string]any{}
	hash, replay, err := CreationReceipt(ctx, tx, actor, operation, key, payload, &out)
	if err == nil && replay {
		out["replayed"] = true
	}
	return hash, out, replay, err
}

func (s *Store) beginCustomerGroupAdmin(ctx context.Context, actor int64) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	// This must precede credential, receipt and group row locks. The statement
	// trigger alone would acquire it after existing SELECT ... FOR UPDATE calls.
	if err = GroupPricingWriteLock(ctx, tx); err == nil {
		err = CheckAdminCreationPermission(ctx, tx, actor, "customer_groups")
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (s *Store) CreateAdminCustomerGroup(ctx context.Context, actor int64, key string, in CustomerGroupCreateInput) (map[string]any, bool, error) {
	tx, err := s.beginCustomerGroupAdmin(ctx, actor)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	const operation = "customer_group.create"
	hash, out, replay, err := customerGroupAdminReceipt(ctx, tx, actor, operation, key, in)
	if err != nil {
		return nil, false, err
	}
	if replay {
		return out, true, tx.Commit(ctx)
	}
	var id, version int64
	if err = tx.QueryRow(ctx, "INSERT INTO customer_groups(name,color,discount_percent,suspend_delete_exempt,separate_invoices) VALUES($1,$2,$3,$4,$5) RETURNING id,version", in.Name, in.Color, in.DiscountPercent, in.SuspendDeleteExempt, in.SeparateInvoices).Scan(&id, &version); err != nil {
		return nil, false, err
	}
	out = map[string]any{"id": id, "version": version}
	if err = Audit(ctx, tx, actor, "customer_group.created", "customer_group", id, map[string]any{"name": in.Name}); err != nil {
		return nil, false, err
	}
	if err = SaveCreationReceipt(ctx, tx, actor, operation, key, hash, out); err != nil {
		return nil, false, err
	}
	return out, false, tx.Commit(ctx)
}

func (s *Store) UpdateAdminCustomerGroup(ctx context.Context, actor, id int64, key string, in CustomerGroupPatchInput) (map[string]any, error) {
	tx, err := s.beginCustomerGroupAdmin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	operation := "customer_group.update:" + strconv.FormatInt(id, 10)
	hash, out, replay, err := customerGroupAdminReceipt(ctx, tx, actor, operation, key, struct {
		ID    int64
		Input any
	}{id, in})
	if err != nil {
		return nil, err
	}
	if replay {
		return out, tx.Commit(ctx)
	}
	var current int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_groups WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		if err == pgx.ErrNoRows {
			return nil, &CustomerPricingResourceNotFound{CustomerGroup: true}
		}
		return nil, err
	}
	if current != in.ExpectedVersion {
		return nil, conflict("version_conflict", "客户分组已变更，请刷新后重试")
	}
	var updated, version int64
	if err = tx.QueryRow(ctx, "UPDATE customer_groups SET name=COALESCE($2,name),color=COALESCE($3,color),discount_percent=COALESCE($4,discount_percent),suspend_delete_exempt=COALESCE($5,suspend_delete_exempt),separate_invoices=COALESCE($6,separate_invoices),version=version+1,updated_at=now() WHERE id=$1 RETURNING id,version", id, in.Name, in.Color, in.DiscountPercent, in.SuspendDeleteExempt, in.SeparateInvoices).Scan(&updated, &version); err != nil {
		return nil, err
	}
	out = map[string]any{"id": updated, "version": version}
	if err = Audit(ctx, tx, actor, "customer_group.updated", "customer_group", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
		return nil, err
	}
	if err = SaveCreationReceipt(ctx, tx, actor, operation, key, hash, out); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) DeleteAdminCustomerGroup(ctx context.Context, actor, id int64, key string, in CustomerGroupDeleteInput) (map[string]any, error) {
	tx, err := s.beginCustomerGroupAdmin(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	operation := "customer_group.delete:" + strconv.FormatInt(id, 10)
	hash, out, replay, err := customerGroupAdminReceipt(ctx, tx, actor, operation, key, struct {
		ID    int64
		Input any
	}{id, in})
	if err != nil {
		return nil, err
	}
	if replay {
		return out, tx.Commit(ctx)
	}
	var current int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_groups WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		if err == pgx.ErrNoRows {
			return nil, &CustomerPricingResourceNotFound{CustomerGroup: true}
		}
		return nil, err
	}
	if current != in.ExpectedVersion {
		return nil, conflict("version_conflict", "客户分组已变更，请刷新后重试")
	}
	var deleted int64
	if err = tx.QueryRow(ctx, "DELETE FROM customer_groups WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM customer_group_members WHERE group_id=$1) RETURNING id", id).Scan(&deleted); err != nil {
		if err == pgx.ErrNoRows {
			return nil, conflict("group_in_use_or_missing", "分组不存在或仍有客户使用")
		}
		return nil, err
	}
	out = map[string]any{"id": deleted, "deleted": true}
	if err = Audit(ctx, tx, actor, "customer_group.deleted", "customer_group", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
		return nil, err
	}
	if err = SaveCreationReceipt(ctx, tx, actor, operation, key, hash, out); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
