package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// AdministratorAccountHistory exposes preserved commercial facts for audit.
// Its own authorization and read-only transaction intentionally do not reuse or
// relax any customer ownership check used by ordinary business operations.
func (s *Store) AdministratorAccountHistory(ctx context.Context, actor, account int64, sessionHash string, limit, offset int) (map[string]any, error) {
	if account < 1 || limit < 1 || limit > 100 || offset < 0 {
		return nil, &AccountBusinessError{400, "invalid_account_history", "历史账户查询参数无效"}
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrators a JOIN sessions s ON s.user_id=a.id
 WHERE a.id=$1 AND a.status='active' AND a.role='super_admin' AND s.token_hash=$2 AND s.expires_at>clock_timestamp())`, actor, sessionHash).Scan(&allowed); err != nil {
		return nil, err
	}
	if !allowed {
		return nil, &AccountBusinessError{403, "super_admin_required", "历史管理员业务审计需要有效的超级管理员会话"}
	}
	rows, err := tx.Query(ctx, `SELECT id,username,display_name,role,status,balance_cents,created_at FROM administrators WHERE id=$1`, account)
	if err != nil {
		return nil, err
	}
	profile, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"account": profile, "read_only": true}
	for _, source := range []struct{ name, projection string }{
		{"orders", `id,order_no,status,review_status,total_cents,currency,created_at,updated_at,deleted_at`},
		{"invoices", `id,invoice_no,order_id,invoice_kind,status,amount_cents,due_at,paid_at,created_at,updated_at`},
		{"services", `id,order_id,product_id,name,domain,billing_cycle,status,next_due_at,created_at,updated_at`},
		{"balance_ledger", `id,entry_type,delta_cents,balance_after,reference_type,reference_id,created_at`},
	} {
		var total int64
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM "+source.name+" WHERE user_id=$1", account).Scan(&total); err != nil {
			return nil, err
		}
		items, err := collectPurchaseReadRows(tx.Query(ctx, "SELECT "+source.projection+" FROM "+source.name+" WHERE user_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3", account, limit, offset))
		if err != nil {
			return nil, err
		}
		name := source.name
		if name == "balance_ledger" {
			name = "ledger"
		}
		result[name] = map[string]any{"total": total, "items": items, "limit": limit, "offset": offset}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
