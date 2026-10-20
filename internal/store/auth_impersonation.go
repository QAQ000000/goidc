package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type impersonationBinding struct {
	ActorID    int64
	CustomerID int64
	AdminHash  string
}
type impersonationQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// The indexed entity key is the exact customer-session hash. Two independently
// logged-in accounts never become an impersonation merely by sharing a browser.
func readImpersonationBinding(ctx context.Context, q impersonationQuerier, customerHash string) (impersonationBinding, error) {
	var b impersonationBinding
	err := q.QueryRow(ctx, `SELECT actor_id,(payload->>'customer_id')::bigint,payload->>'admin_session_hash'
 FROM audit_events WHERE entity_type='impersonation_session' AND entity_id=$1 AND action='auth.impersonation_bound'
 ORDER BY created_at DESC,id DESC LIMIT 1`, customerHash).Scan(&b.ActorID, &b.CustomerID, &b.AdminHash)
	return b, err
}
func impersonationBindingAllowed(ctx context.Context, q impersonationQuerier, b impersonationBinding) error {
	var allowed bool
	err := q.QueryRow(ctx, `SELECT coalesce((a.role='super_admin' OR (a.role='admin' AND ar.active AND 'customers.manage'=ANY(ar.permissions)))
 AND admin_customer_owner_allowed(a.id,c.sales_owner_id),false)
 FROM sessions s JOIN administrators a ON a.id=s.user_id AND a.status='active'
 LEFT JOIN admin_roles ar ON ar.id=a.admin_role_id
 JOIN users c ON c.id=$3 AND c.role='customer' AND c.status='active'
 WHERE s.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp()`, b.AdminHash, b.ActorID, b.CustomerID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return pgx.ErrNoRows
	}
	return nil
}
func lockImpersonationAdministrator(ctx context.Context, tx pgx.Tx, actorID int64, hash string) error {
	var id int64
	if err := tx.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE", hash, actorID).Scan(&id); err != nil {
		return err
	}
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, "SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE", actorID).Scan(&role, &roleID); err != nil {
		return err
	}
	if role == "super_admin" {
		return nil
	}
	if role != "admin" || roleID == nil {
		return pgx.ErrNoRows
	}
	var allowed bool
	if err := tx.QueryRow(ctx, "SELECT active AND 'customers.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE", *roleID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return pgx.ErrNoRows
	}
	return nil
}
func lockImpersonationBinding(ctx context.Context, tx pgx.Tx, customerHash string, customerID int64) error {
	b, err := readImpersonationBinding(ctx, tx, customerHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.CustomerID != customerID {
		return pgx.ErrNoRows
	}
	if err = lockImpersonationAdministrator(ctx, tx, b.ActorID, b.AdminHash); err != nil {
		return err
	}
	if err = LockAdminCustomer(ctx, tx, b.ActorID, customerID); err != nil {
		return err
	}
	var id int64
	return tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 AND role='customer' AND status='active' FOR SHARE", customerID).Scan(&id)
}

func (s *Store) StartCustomerImpersonation(ctx context.Context, actorID, customerID int64, adminHash, customerHash, currentCustomerHash string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockImpersonationAdministrator(ctx, tx, actorID, adminHash); err != nil {
		return err
	}
	if err = LockAdminCustomer(ctx, tx, actorID, customerID); err != nil {
		return err
	}
	var active bool
	if err = tx.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1 AND role='customer' FOR SHARE", customerID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return conflict("customer_disabled", "停用客户不能切换登录")
	}
	if currentCustomerHash != "" {
		binding, bindingErr := readImpersonationBinding(ctx, tx, currentCustomerHash)
		if bindingErr == nil {
			var currentID int64
			bindingErr = tx.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp()", currentCustomerHash, binding.CustomerID).Scan(&currentID)
			if bindingErr == nil {
				bindingErr = impersonationBindingAllowed(ctx, tx, binding)
			}
			if bindingErr == nil {
				return conflict("already_impersonating", "当前已经以客户身份登录")
			}
		}
		if bindingErr != nil && !errors.Is(bindingErr, pgx.ErrNoRows) {
			return bindingErr
		}
	}

	if _, err = tx.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '2 hours')", customerHash, customerID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"admin_session_hash": adminHash, "customer_id": customerID})
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES($1,'auth.impersonation_bound','impersonation_session',$2,$3)", actorID, customerHash, payload); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actorID, "auth.impersonation_started", "user", customerID, map[string]any{"customer_id": customerID, "expires_in_seconds": int64(2 * time.Hour / time.Second)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) CustomerImpersonationStatus(ctx context.Context, customerID int64, customerHash, adminHash string) (string, bool, error) {
	b, err := readImpersonationBinding(ctx, s.Pool, customerHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if b.CustomerID != customerID || b.AdminHash != adminHash {
		return "", false, nil
	}
	if err = impersonationBindingAllowed(ctx, s.Pool, b); errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var name string
	err = s.Pool.QueryRow(ctx, "SELECT display_name FROM administrators WHERE id=$1", b.ActorID).Scan(&name)
	return name, err == nil, err
}
func (s *Store) EndCustomerImpersonation(ctx context.Context, customerHash, adminHash string) (int64, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	b, err := readImpersonationBinding(ctx, tx, customerHash)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && b.AdminHash != adminHash) {
		return 0, conflict("not_impersonating", "当前不是客户身份登录")
	}
	if err != nil {
		return 0, err
	}
	// Cleanup remains possible after actor expiry or revocation, but possession of
	// both credentials and their immutable binding is still required.
	deleted, err := tx.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1 AND user_id=$2", customerHash, b.CustomerID)
	if err != nil {
		return 0, err
	}
	if deleted.RowsAffected() != 1 {
		return 0, conflict("not_impersonating", "客户身份登录已结束")
	}

	if err = Audit(ctx, tx, b.ActorID, "auth.impersonation_ended", "user", b.CustomerID, map[string]any{"customer_id": b.CustomerID}); err != nil {
		return 0, err
	}
	return b.ActorID, tx.Commit(ctx)
}
