package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Lock all authoritative principals before pricing, without a credential.
// Customer creation may already own a credential while it waits for pricing;
// password changes own the account before revoking that credential. Taking an
// account only after pricing would complete that three-transaction lock cycle.
// RBAC writers use the exclusive counterpart and cannot revoke grants halfway
// through a mutation. No business row or receipt is locked before pricing.
func lockAdminPricingMutation(ctx context.Context, tx pgx.Tx, actor int64, domain string) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return err
	}
	if err := lockAdminCreationPrincipal(ctx, tx, actor, domain); err != nil {
		return err
	}
	if err := GroupPricingWriteLock(ctx, tx); err != nil {
		return err
	}
	return recheckAdminPricingCredential(ctx, tx, actor)
}

func recheckAdminPricingCredential(ctx context.Context, tx pgx.Tx, actor int64) error {
	if err := lockStorageCredential(ctx, tx, actor); err != nil {
		return err
	}
	// A locking SELECT may have waited beyond an otherwise unchanged row's
	// natural expiry. A fresh statement evaluates clock_timestamp after the wait.
	return lockStorageCredential(ctx, tx, actor)
}

// Audits and durable receipts may wait too. Expiry must roll back the whole
// mutation, or prevent returning an old receipt, rather than confirm stale auth.
func commitAdminPricingMutation(ctx context.Context, tx pgx.Tx, actor int64) error {
	if err := recheckAdminPricingCredential(ctx, tx, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
