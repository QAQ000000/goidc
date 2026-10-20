package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The original ledger owns the customer/key domain. This is deliberately not an
// actor receipt: another currently authorized administrator can verify the same
// customer's posted credit, but no note or historical actor is disclosed.
func lockAdminBalanceIntent(ctx context.Context, tx pgx.Tx, actor, customer int64, key string) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return err
	}
	if err := LockAdminCustomer(ctx, tx, actor, customer); err != nil {
		return err
	}
	return lockOperationKey(ctx, tx, "topup", fmt.Sprintf("%d:%s", customer, key))
}

// Call after financial/settings/key waits. Account changes lock the concrete
// administrator before credentials; following that order avoids a password
// reset holding the administrator while waiting for our session/API-key lock.
func requireAdminBalanceAuthority(ctx context.Context, tx pgx.Tx, actor, customer int64) error {
	if err := RequireAdministratorPermission(ctx, tx, actor, "balance.manage"); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("unauthorized", "登录已失效，请重新登录")
		}
		return err
	}
	if err := lockStorageCredential(ctx, tx, actor); err != nil {
		return err
	}
	// Re-read current scope after both authority locks; middleware and an
	// earlier locking SELECT are not evidence of access after a wait.
	if err := LockAdminCustomer(ctx, tx, actor, customer); err != nil {
		return err
	}
	// The first credential SELECT can itself wait. A session that simply
	// expires while its unchanged row is locked needs a fresh statement.
	return lockStorageCredential(ctx, tx, actor)
}

// AdminBalanceOperation only reads a committed manual top-up ledger fact. A
// missing fact is unconfirmed, including a request that never reached this
// server; it neither reserves an intention nor authorizes a new request key.
func (s *Store) AdminBalanceOperation(ctx context.Context, actor, customer int64, key string) (map[string]any, error) {
	if actor < 1 || customer < 1 || len(key) < 8 || len(key) > 128 {
		return nil, conflict("invalid_idempotency_key", "幂等键长度必须在 8 到 128 之间")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockAdminBalanceIntent(ctx, tx, actor, customer, key); err != nil {
		return nil, err
	}
	var id, amount, balance int64
	var createdAt time.Time
	err = tx.QueryRow(ctx, `SELECT id,delta_cents,balance_after,created_at FROM balance_ledger
 WHERE user_id=$1 AND entry_type='topup' AND reference_type='admin_topup' AND idempotency_key=$2`, customer, key).Scan(&id, &amount, &balance, &createdAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	out := map[string]any{"state": "unconfirmed", "user_id": customer}
	if err == nil {
		out = map[string]any{"state": "confirmed", "user_id": customer, "amount_cents": amount, "balance_cents": balance, "ledger_id": id, "created_at": createdAt}
	}
	if err = requireAdminBalanceAuthority(ctx, tx, actor, customer); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
