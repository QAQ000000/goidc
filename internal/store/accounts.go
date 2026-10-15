package store

import (
	"context"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

func (s *Store) ChangePassword(ctx context.Context, userID int64, currentTokenHash, currentPassword, newPassword string) error {
	if userID < 1 || len(currentTokenHash) != 64 || len(currentPassword) < 1 || len(currentPassword) > 72 || len(newPassword) < 8 || len(newPassword) > 72 {
		return conflict("invalid_password", "当前密码或新密码无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var hash string
	if err = tx.QueryRow(ctx, "SELECT password_hash FROM users WHERE id=$1 AND status='active' FOR UPDATE", userID).Scan(&hash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(currentPassword)) != nil {
		return conflict("invalid_current_password", "当前密码错误")
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE users SET password_hash=$2,password_reset_required=false,admin_version=admin_version+1,updated_at=now() WHERE id=$1", userID, string(newHash)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2", userID, currentTokenHash); err != nil {
		return err
	}
	// Long-lived API keys survive a password change unless they are revoked
	// here. A customer changing the password after a suspected leak must not
	// leave the leaked bearer credential usable, so every active key is revoked
	// in the same transaction; the owner re-issues keys explicitly.
	if _, err = tx.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL", userID); err != nil {
		return err
	}
	if err = Audit(ctx, tx, userID, "customer.password_changed", "user", userID, map[string]any{"sessions_revoked": true, "api_keys_revoked": true}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Bootstrap(ctx context.Context, username, email, password string) error {
	if username == "" || email == "" || len(password) < 8 || len(password) > 72 {
		return fmt.Errorf("bootstrap requires username, email and an 8-72 byte password")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108189)"); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM users WHERE role IN ('admin','super_admin')").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("administrator already exists; bootstrap will not overwrite it")
	}
	var id int64
	if err = tx.QueryRow(ctx, "INSERT INTO users(username,email,password_hash,display_name,role) VALUES($1,$2,$3,'系统管理员','super_admin') RETURNING id", username, email, string(hash)).Scan(&id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO carts(user_id) VALUES($1)", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, id, "admin.bootstrapped", "user", id, map[string]string{"username": username}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
