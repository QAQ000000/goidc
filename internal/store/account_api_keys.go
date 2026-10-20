package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type AccountAPIKeyInput struct {
	Name, Prefix, SecretHash string
	ExpiresAt                *time.Time
}

// Creating a long-lived credential must serialize with password/factor changes
// and recheck the exact browser session after any account-lock wait.
func (s *Store) CreateAccountAPIKey(ctx context.Context, owner int64, sessionHash string, input AccountAPIKeyInput) (id int64, err error) {
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		table, err := authenticationTable(ctx, tx, owner)
		if err != nil {
			return err
		}
		var role string
		if err = tx.QueryRow(ctx, "SELECT role FROM "+table+" WHERE id=$1 AND status='active' FOR UPDATE", owner).Scan(&role); err != nil {
			return err
		}
		if err = lockAccountSession(ctx, tx, owner, sessionHash); err != nil {
			return err
		}
		if role == "customer" {
			if err = s.CheckCustomerAPIKeyEligibilityTx(ctx, tx, owner); err != nil {
				return err
			}
		}
		if err = tx.QueryRow(ctx, "INSERT INTO api_keys(user_id,name,prefix,secret_hash,expires_at) VALUES($1,$2,$3,$4,$5) RETURNING id", owner, input.Name, input.Prefix, input.SecretHash, input.ExpiresAt).Scan(&id); err != nil {
			return err
		}
		return Audit(ctx, tx, owner, "api_key.created", "api_key", id, map[string]any{"name": input.Name, "prefix": input.Prefix, "expires_at": input.ExpiresAt})
	})
	return
}
