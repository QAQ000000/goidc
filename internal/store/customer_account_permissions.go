package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Customer writes lock the authoritative administrator before the original
// credential, matching password rotation. Keep this scoped to customer account
// mutations instead of changing the older helper used by other domains.
func lockCustomerAccountManager(ctx context.Context, tx pgx.Tx, actor int64) (super, group, sales bool, err error) {
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" {
		err = conflict("unauthorized", "该操作需要有效的原始会话或 API 密钥")
		return
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return
	}
	if err = RequireAdministratorPermission(ctx, tx, actor, "customers.manage"); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = conflict("unauthorized", "登录已失效，请重新登录")
		}
		return
	}
	err = tx.QueryRow(ctx, `SELECT u.role='super_admin',
 COALESCE(u.role='super_admin' OR (u.role='admin' AND r.active AND 'customer_groups.manage'=ANY(r.permissions)),false),u.sales_enabled
 FROM administrators u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1`, actor).Scan(&super, &group, &sales)
	if err == nil {
		err = recheckCustomerAccountCredential(ctx, tx, actor)
	}
	return
}

func recheckCustomerAccountCredential(ctx context.Context, tx pgx.Tx, actor int64) error {
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" {
		return conflict("unauthorized", "该操作需要有效的原始会话或 API 密钥")
	}
	if err := lockStorageCredential(ctx, tx, actor); err != nil {
		return err
	}
	// An unchanged row may have been locked across its natural expiry. A fresh
	// statement evaluates clock_timestamp after the wait rather than before it.
	return lockStorageCredential(ctx, tx, actor)
}

func commitCustomerAccountMutation(ctx context.Context, tx pgx.Tx, actor int64) error {
	if err := recheckCustomerAccountCredential(ctx, tx, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func LockCustomerManager(ctx context.Context, tx pgx.Tx, actor int64) (super, group, sales bool, err error) {
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return
	}
	if err = LockFileCredential(ctx, tx, actor); err != nil {
		return
	}
	var manage bool
	err = tx.QueryRow(ctx, `SELECT u.status='active' AND u.role='super_admin',
 COALESCE(u.status='active' AND (u.role='super_admin' OR (u.role='admin' AND r.active AND 'customers.manage'=ANY(r.permissions))),false),
 COALESCE(u.status='active' AND (u.role='super_admin' OR (u.role='admin' AND r.active AND 'customer_groups.manage'=ANY(r.permissions))),false),u.sales_enabled
 FROM administrators u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1`, actor).Scan(&super, &manage, &group, &sales)
	if err == nil && !manage {
		err = &Conflict{Code: "permission_denied", Message: "没有管理客户的权限"}
	}
	return
}

func (s *Store) CustomerAccountFieldDefinitions(ctx context.Context, actor int64, limit, offset int) (map[string]any, error) {
	var page map[string]any
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
			return err
		}
		if err := LockFileCredential(ctx, tx, actor); err != nil {
			return err
		}
		var allowed bool
		if err := tx.QueryRow(ctx, `SELECT u.status='active' AND (u.role='super_admin' OR (u.role='admin' AND a.active AND ('customers.view'=ANY(a.permissions) OR 'customers.manage'=ANY(a.permissions)))) FROM administrators u LEFT JOIN admin_roles a ON a.id=u.admin_role_id WHERE u.id=$1`, actor).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return conflict("permission_denied", "没有读取客户字段的权限")
		}
		var version int64
		if err := tx.QueryRow(ctx, "SELECT version FROM customer_field_catalogue WHERE id=1 FOR SHARE").Scan(&version); err != nil {
			return err
		}
		var err error
		page, err = ticketAccountPage(ctx, tx, `SELECT id,field_key,label,field_type,description,validation_pattern,choices,sort_order,admin_only,required,show_on_register,active,version FROM customer_custom_fields WHERE active`, "sort_order,id", nil, limit, offset)
		if err != nil {
			return err
		}
		page["definitions_version"] = version
		return nil
	})
	return page, err
}
