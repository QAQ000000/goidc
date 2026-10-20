package store

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// RequireAdministratorPermission locks the authoritative administrator and role
// before a transaction proceeds. A principal or customer row grants no access.
func RequireAdministratorPermission(ctx context.Context, tx pgx.Tx, actor int64, permission string) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, "SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE", actor).Scan(&role, &roleID); err != nil {
		return err
	}
	if role == "super_admin" {
		return nil
	}
	var allowed bool
	if role == "admin" && roleID != nil {
		if err := tx.QueryRow(ctx, "SELECT active AND $2=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE", *roleID, permission).Scan(&allowed); err != nil {
			return err
		}
	}
	if !allowed {
		return conflict("permission_denied", "管理员权限已失效")
	}
	return nil
}

func LockCreditAdministrator(ctx context.Context, tx pgx.Tx, actor, customer int64, session string) error {
	if err := RequireAdministratorPermission(ctx, tx, actor, "credit.manage"); err != nil {
		return err
	}
	var user int64
	if err := tx.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE", session, actor).Scan(&user); err != nil {
		return conflict("unauthorized", "会话失效")
	}
	return LockAdminCustomer(ctx, tx, actor, customer)
}
