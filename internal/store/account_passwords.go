package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// The concrete account row must be locked before invalidating password-derived
// credentials. Only a verified personal password change may retain its session;
// administrator and recovery resets invalidate every session. MFA enrollment
// and unused recovery codes remain intact for the next authenticated login.
func invalidatePasswordCredentials(ctx context.Context, tx pgx.Tx, owner int64, retainedSession string) error {
	if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1 AND ($2='' OR token_hash<>$2)", owner, retainedSession); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM mfa_challenges WHERE user_id=$1", owner); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL", owner)
	return err
}

func (s *Store) AdminResetCustomerPassword(ctx context.Context, actor, customer int64, password string) error {
	if actor < 1 || customer < 1 || len(password) < 8 || len(password) > 72 {
		return conflict("invalid_password", "密码长度必须为 8 到 72 个字符")
	}
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" {
		return conflict("unauthorized", "登录已失效，请重新登录")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		// Take the assignment lock before the customer row, matching sales
		// transfers. Revalidate the administrator only after these waits.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(hashtextextended('customer-sales:'||$1::bigint::text,0))", customer); err != nil {
			return err
		}
		var owner int64
		if err := tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 AND role='customer' FOR UPDATE", customer).Scan(&owner); err != nil {
			return err
		}
		if err := RequireAdministratorPermission(ctx, tx, actor, "customers.manage"); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return conflict("unauthorized", "登录已失效，请重新登录")
			}
			return err
		}
		if err := lockStorageCredential(ctx, tx, actor); err != nil {
			return err
		}
		if err := LockAdminCustomer(ctx, tx, actor, customer); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE users SET password_hash=$2,password_reset_required=false,admin_version=admin_version+1,updated_at=now() WHERE id=$1", customer, string(hash)); err != nil {
			return err
		}
		if err := invalidatePasswordCredentials(ctx, tx, customer, ""); err != nil {
			return err
		}
		return Audit(ctx, tx, actor, "customer.password_reset", "user", customer, map[string]any{
			"reason": "admin_reset", "sessions_revoked": true, "api_keys_revoked": true, "mfa_challenges_revoked": true,
		})
	})
}
