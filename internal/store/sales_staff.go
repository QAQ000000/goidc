package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

func (s *Store) DeleteSalesCommissionGroup(ctx context.Context, actor, id, expectedVersion int64, operation, key, hash string) (map[string]any, error) {
	tx, beginErr := s.Pool.Begin(ctx)
	if beginErr != nil {
		return nil, beginErr
	}
	defer tx.Rollback(ctx)

	var err error
	if replay, replayed, e := loadAccountBusinessReplay(ctx, tx, actor, operation, key, hash); e != nil {
		return nil, e
	} else if replayed {

		return replay, tx.Commit(ctx)
	}
	var current int64
	if err = tx.QueryRow(ctx, "SELECT version FROM sales_commission_groups WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &AccountBusinessError{404, "not_found", "提成组不存在"}
		}
		return nil, err
	}
	if current != expectedVersion {
		return nil, &AccountBusinessError{409, "version_conflict", "提成组已变更，请重新加载"}
	}
	if _, err = tx.Exec(ctx, "UPDATE administrators SET sales_commission_group_id=NULL WHERE sales_commission_group_id=$1", id); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM sales_commission_groups WHERE id=$1", id); err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "deleted": true}
	if err = Audit(ctx, tx, actor, "sales.commission_group.deleted", "sales_commission_group", id, map[string]any{"expected_version": expectedVersion}); err != nil {
		return nil, err
	}
	if err = SaveCreationReceipt(ctx, tx, actor, operation, key, hash, result); err != nil {
		return nil, err
	}

	return result, tx.Commit(ctx)
}

type SalesStaffInput struct {
	SalesEnabled           *bool  `json:"sales_enabled"`
	CustomerScope          string `json:"customer_scope"`
	CustomerOwnerless      *bool  `json:"customer_ownerless"`
	SalesCommissionGroupID *int64 `json:"sales_commission_group_id"`
	ExpectedVersion        int64  `json:"expected_version"`
}

func accountBool(v bool) *bool { return &v }
func (s *Store) UpdateSalesStaff(ctx context.Context, actor, id int64, in SalesStaffInput, operation, key, hash string) (map[string]any, error) {
	tx, beginErr := s.Pool.Begin(ctx)
	if beginErr != nil {
		return nil, beginErr
	}
	defer tx.Rollback(ctx)

	var err error
	if replay, replayed, e := loadAccountBusinessReplay(ctx, tx, actor, operation, key, hash); e != nil {
		return nil, e
	} else if replayed {

		return replay, tx.Commit(ctx)
	}
	var current int64
	var role string
	if err = tx.QueryRow(ctx, "SELECT admin_version,role FROM administrators WHERE id=$1 AND role IN ('admin','super_admin') FOR UPDATE", id).Scan(&current, &role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &AccountBusinessError{404, "not_found", "销售人员不存在"}
		}
		return nil, err
	}
	if current != in.ExpectedVersion {
		return nil, &AccountBusinessError{409, "version_conflict", "销售人员资料已变更，请重新加载"}
	}
	if in.SalesCommissionGroupID != nil {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sales_commission_groups WHERE id=$1)", *in.SalesCommissionGroupID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, &AccountBusinessError{400, "invalid_sales_staff", "提成组不存在"}
		}
	}
	if in.SalesEnabled == nil {
		in.SalesEnabled = accountBool(false)
	}
	if in.CustomerOwnerless == nil {
		in.CustomerOwnerless = accountBool(false)
	}
	if in.CustomerScope == "" {
		in.CustomerScope = "owned"
	}
	var version int64
	err = tx.QueryRow(ctx, `UPDATE administrators SET sales_enabled=$1,customer_scope=$2,customer_ownerless=$3,sales_commission_group_id=$4,admin_version=admin_version+1,updated_at=now()
WHERE id=$5 RETURNING admin_version`, *in.SalesEnabled, in.CustomerScope, *in.CustomerOwnerless, in.SalesCommissionGroupID, id).Scan(&version)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "version": version}
	if err = Audit(ctx, tx, actor, "sales.staff_updated", "administrator", id, map[string]any{"sales_enabled": *in.SalesEnabled, "customer_scope": in.CustomerScope, "customer_ownerless": *in.CustomerOwnerless, "sales_commission_group_id": in.SalesCommissionGroupID, "previous_version": current, "role": role}); err != nil {
		return nil, err
	}
	if err = SaveCreationReceipt(ctx, tx, actor, operation, key, hash, result); err != nil {
		return nil, err
	}

	return result, tx.Commit(ctx)
}

func (s *Store) SalesStaffPage(ctx context.Context, q string, limit, offset int) (map[string]any, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var total int64
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM administrators WHERE role IN ('admin','super_admin') AND ($1='' OR username ILIKE '%'||$1||'%' OR display_name ILIKE '%'||$1||'%' OR email ILIKE '%'||$1||'%')`, q).Scan(&total); err != nil {
		return nil, err
	}
	items, err := collectMaps(tx.Query(ctx, `SELECT u.id,u.display_name,u.email,u.username,u.role,u.status,u.sales_enabled,u.customer_scope,u.customer_ownerless,u.sales_commission_group_id,u.admin_version,u.updated_at,COALESCE(g.name,'') AS sales_commission_group_name
 FROM administrators u LEFT JOIN sales_commission_groups g ON g.id=u.sales_commission_group_id WHERE u.role IN ('admin','super_admin') AND ($1='' OR u.username ILIKE '%'||$1||'%' OR u.display_name ILIKE '%'||$1||'%' OR u.email ILIKE '%'||$1||'%') ORDER BY u.id LIMIT $2 OFFSET $3`, q, limit, offset))
	if err != nil {
		return nil, err
	}
	return map[string]any{"total": total, "items": items, "limit": limit, "offset": offset}, tx.Commit(ctx)
}
