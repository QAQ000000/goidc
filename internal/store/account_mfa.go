package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

func (s *Store) AuthenticationMFAEnabled(ctx context.Context, id int64) (bool, error) {
	table, err := authenticationTable(ctx, s.Pool, id)
	if err != nil {
		return false, err
	}
	var enabled bool
	err = s.Pool.QueryRow(ctx, "SELECT mfa_enabled FROM "+table+" WHERE id=$1", id).Scan(&enabled)
	return enabled, err
}

// Enrollment stages the secret; credentials are revoked only when the factor
// becomes effective at confirmation, never for an unfinished enrollment.
func (s *Store) EnrollAuthenticationMFA(ctx context.Context, id int64, sessionHash string, encrypted []byte, recoveryHashes []string) (denial string, err error) {
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		table, err := authenticationTable(ctx, tx, id)
		if err != nil {
			return err
		}
		var enabled, allowed bool
		var role string
		if err = tx.QueryRow(ctx, "SELECT mfa_enabled,role FROM "+table+" WHERE id=$1 AND status='active' FOR UPDATE", id).Scan(&enabled, &role); err != nil {
			return err
		}
		if err = lockAccountSession(ctx, tx, id, sessionHash); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, "SELECT CASE WHEN $1='customer' THEN customer_enabled ELSE admin_enabled END FROM mfa_settings WHERE id=1 FOR SHARE", role).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			denial = "mfa_enrollment_disabled"
			return nil
		}
		if enabled {
			denial = "mfa_already_enabled"
			return nil
		}
		if _, err = tx.Exec(ctx, "UPDATE "+table+" SET mfa_enabled=false,mfa_secret_enc=$2,mfa_confirmed_at=NULL,updated_at=now() WHERE id=$1", id, encrypted); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM mfa_recovery_codes WHERE user_id=$1", id); err != nil {
			return err
		}
		for _, hash := range recoveryHashes {
			if _, err = tx.Exec(ctx, "INSERT INTO mfa_recovery_codes(code_hash,user_id) VALUES($1,$2)", hash, id); err != nil {
				return err
			}
		}
		return nil
	})
	return
}

func (s *Store) ConfirmAuthenticationMFA(ctx context.Context, id int64, sessionHash string, verify func([]byte) bool) (denial string, err error) {
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		table, err := authenticationTable(ctx, tx, id)
		if err != nil {
			return err
		}
		var enabled, allowed bool
		var role string
		var encrypted []byte
		if err = tx.QueryRow(ctx, "SELECT mfa_enabled,mfa_secret_enc,role FROM "+table+" WHERE id=$1 AND status='active' FOR UPDATE", id).Scan(&enabled, &encrypted, &role); err != nil {
			return err
		}
		if err = lockAccountSession(ctx, tx, id, sessionHash); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, "SELECT CASE WHEN $1='customer' THEN customer_enabled ELSE admin_enabled END FROM mfa_settings WHERE id=1 FOR SHARE", role).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			denial = "mfa_enrollment_disabled"
			return nil
		}
		if enabled || len(encrypted) == 0 {
			denial = "mfa_not_pending"
			return nil
		}
		if !verify(encrypted) {
			denial = "invalid_mfa"
			return nil
		}
		if _, err = tx.Exec(ctx, "UPDATE "+table+" SET mfa_enabled=true,mfa_confirmed_at=now(),updated_at=now() WHERE id=$1", id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL", id)
		return err
	})
	return
}

func (s *Store) DisableAuthenticationMFA(ctx context.Context, id int64, password, recoveryHash, currentSessionHash string, verify func([]byte) bool) (valid bool, err error) {
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		table, err := authenticationTable(ctx, tx, id)
		if err != nil {
			return err
		}
		var enabled bool
		var encrypted []byte
		var hash string
		if err = tx.QueryRow(ctx, "SELECT password_hash,mfa_enabled,mfa_secret_enc FROM "+table+" WHERE id=$1 AND status='active' FOR UPDATE", id).Scan(&hash, &enabled, &encrypted); err != nil {
			return err
		}
		if err = lockAccountSession(ctx, tx, id, currentSessionHash); err != nil {
			return err
		}
		if !enabled || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
			return nil
		}
		valid = verify(encrypted)
		if !valid && recoveryHash != "" {
			var usedHash string
			err = tx.QueryRow(ctx, "UPDATE mfa_recovery_codes SET used_at=now() WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL RETURNING code_hash", id, recoveryHash).Scan(&usedHash)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			valid = err == nil
		}
		if !valid {
			return nil
		}
		if _, err = tx.Exec(ctx, "UPDATE "+table+" SET mfa_enabled=false,mfa_secret_enc=NULL,mfa_confirmed_at=NULL,updated_at=now() WHERE id=$1", id); err != nil {
			return err
		}
		// Disabling already requires the password and the existing factor. Keep
		// API keys and the verified current session, matching the existing policy.
		if currentSessionHash != "" {
			_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2", id, currentSessionHash)
		}
		return err
	})
	return
}
