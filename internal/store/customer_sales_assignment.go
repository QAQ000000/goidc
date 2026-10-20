package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func requireSalesAssignmentActor(ctx context.Context, tx pgx.Tx, actor int64) error {
	var role, status string
	if err := tx.QueryRow(ctx, "SELECT role,status FROM administrators WHERE id=$1 FOR SHARE", actor).Scan(&role, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("unauthorized", "登录已失效，请重新登录")
		}
		return err
	}
	if status != "active" {
		return conflict("unauthorized", "登录已失效，请重新登录")
	}
	if role != "super_admin" {
		return conflict("permission_denied", "仅超级管理员可调整销售归属")
	}
	return nil
}

func requireSalesAssignmentAuthority(ctx context.Context, tx pgx.Tx, actor int64) error {
	// Concrete accounts precede credentials, as personal password changes lock
	// the administrator before revoking its other sessions. Never allow an
	// unbound Store caller or an API key to inherit the browser-only capability.
	if err := requireSalesAssignmentActor(ctx, tx, actor); err != nil {
		return err
	}
	if err := lockStorageCredential(ctx, tx, actor); err != nil {
		return err
	}
	// A locking SELECT may have waited on an unchanged session row across its
	// natural expiry. A new statement is needed after that wait.
	return lockStorageCredential(ctx, tx, actor)
}

// AssignCustomerSalesOwnerWithKey preserves the legacy CAS request when key is
// empty. Supplying a key binds a durable unified receipt to the complete original
// request; replay can recover an unknown COMMIT without assigning or auditing
// twice. The receipt reports its historical version, not the current assignment.
func (s *Store) AssignCustomerSalesOwnerWithKey(ctx context.Context, actor, id, expectedVersion int64, owner *int64, key string) (map[string]any, error) {
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" || credential.APIKey {
		return nil, conflict("unauthorized", "该操作需要有效的后台浏览器会话")
	}
	if actor < 1 || id < 1 || expectedVersion < 1 || (owner != nil && *owner < 1) {
		return nil, conflict("invalid_sales_assignment", "销售归属或版本无效")
	}
	if key != "" && (len(key) < 8 || len(key) > 128 || strings.TrimSpace(key) != key) {
		return nil, conflict("invalid_idempotency_key", "幂等键长度必须在 8 到 128 之间")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Staff/role writers take the management lock exclusively. Lock the actor
	// before customer assignment, without a credential: legacy customer profile
	// updates can own a credential while waiting for assignment, and password
	// rotation owns the actor before invalidating that credential. Waiting for
	// the actor while owning assignment would close that three-way lock cycle.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return nil, err
	}
	if err = requireSalesAssignmentActor(ctx, tx, actor); err != nil {
		return nil, err
	}
	if owner != nil {
		// The proposed owner can independently edit this customer and rotate
		// their password. Lock that concrete admin before customer-sales too,
		// otherwise the same credential -> assignment -> account cycle exists
		// with a different administrator. Eligibility remains a new-write check
		// below: disabled or retired owners must not invalidate saved receipts.
		var candidate int64
		if err = tx.QueryRow(ctx, "SELECT id FROM administrators WHERE id=$1 AND role='admin' FOR SHARE", *owner).Scan(&candidate); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('customer-sales:'||$1::bigint::text,0))", id); err != nil {
		return nil, err
	}
	operation := "customer.sales_owner:" + strconv.FormatInt(id, 10)
	if key != "" {
		// Acquire the unified key lock before credentials. The subsequent
		// CreationReceipt re-enters it after fresh authorization.
		if err = lockOperationKey(ctx, tx, "mutation", fmt.Sprintf("%d:admin:%s:%s", actor, operation, key)); err != nil {
			return nil, err
		}
	}
	var previous *int64
	var version int64
	if err = tx.QueryRow(ctx, "SELECT sales_owner_id,sales_version FROM users WHERE id=$1 AND role='customer' FOR UPDATE", id).Scan(&previous, &version); err != nil {
		return nil, err
	}
	if err = requireSalesAssignmentAuthority(ctx, tx, actor); err != nil {
		return nil, err
	}
	payload := struct {
		CustomerID      int64  `json:"customer_id"`
		SalesOwnerID    *int64 `json:"sales_owner_id"`
		ExpectedVersion int64  `json:"expected_version"`
	}{id, owner, expectedVersion}
	out := map[string]any{}
	hash, replayed, err := CreationReceipt(ctx, tx, actor, operation, key, payload, &out)
	if err != nil {
		return nil, err
	}
	if replayed {
		if err = requireSalesAssignmentAuthority(ctx, tx, actor); err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		out["replayed"] = true
		return out, nil
	}
	if version != expectedVersion {
		return nil, conflict("version_conflict", "客户归属已变更，请重新加载")
	}
	if owner != nil {
		var ownerID int64
		if err = tx.QueryRow(ctx, `SELECT u.id FROM administrators u JOIN admin_roles ar ON ar.id=u.admin_role_id WHERE u.id=$1 AND u.role='admin' AND u.status='active' AND u.sales_enabled AND ar.active AND 'customers.view'=ANY(ar.permissions) FOR SHARE OF u,ar`, *owner).Scan(&ownerID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, conflict("invalid_sales_owner", "请选择已启用的销售管理员及客户读取角色")
			}
			return nil, err
		}
	}
	// Owner eligibility can wait independently of the actor's session. Recheck
	// it before writing, and again after audit/receipt work before committing.
	if err = requireSalesAssignmentAuthority(ctx, tx, actor); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "UPDATE users SET sales_owner_id=$2,sales_version=sales_version+1,updated_at=now() WHERE id=$1", id, owner); err != nil {
		return nil, err
	}
	if err = Audit(ctx, tx, actor, "customer.sales_assigned", "user", id, map[string]any{"previous_owner_id": previous, "sales_owner_id": owner}); err != nil {
		return nil, err
	}
	out = map[string]any{"customer_id": id, "sales_owner_id": owner, "sales_version": version + 1}
	if key != "" {
		out["replayed"] = false
	}
	if err = SaveCreationReceipt(ctx, tx, actor, operation, key, hash, out); err != nil {
		return nil, err
	}
	if err = requireSalesAssignmentAuthority(ctx, tx, actor); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
