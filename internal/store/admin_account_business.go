package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func (s *Store) AdminRoleDirectory(ctx context.Context) ([]map[string]any, error) {
	return collectMaps(s.Pool.Query(ctx, `SELECT r.id,r.name,r.description,r.active,r.permissions,r.version,r.created_at,r.updated_at,
 (SELECT count(*) FROM administrators u WHERE u.admin_role_id=r.id) AS member_count FROM admin_roles r ORDER BY r.id`))
}

const adminStaffProjection = `u.id,u.username,u.email,u.display_name,u.role,u.status,u.admin_role_id,r.name AS admin_role_name,
 r.active AS admin_role_active,u.admin_version,u.mfa_enabled,u.created_at,u.customer_scope,u.customer_ownerless,u.sales_enabled,u.updated_at,u.ticket_scope,u.ticket_unassigned,
 ARRAY(SELECT department_id FROM admin_ticket_departments WHERE user_id=u.id ORDER BY department_id) AS ticket_department_ids`

func (s *Store) AdminStaffDirectory(ctx context.Context, limit, offset int, query string) ([]map[string]any, error) {
	return collectMaps(s.Pool.Query(ctx, `SELECT `+adminStaffProjection+` FROM administrators u LEFT JOIN admin_roles r ON r.id=u.admin_role_id
 WHERE u.role IN ('admin','super_admin') AND ($3='' OR u.username ILIKE '%'||$3||'%' OR u.display_name ILIKE '%'||$3||'%')
 ORDER BY u.id LIMIT $1 OFFSET $2`, limit, offset, query))
}

func (s *Store) AdminStaffDetail(ctx context.Context, id int64) (map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+adminStaffProjection+` FROM administrators u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
}

func (s *Store) SalesOwnerDirectory(ctx context.Context) ([]map[string]any, error) {
	return collectMaps(s.Pool.Query(ctx, `SELECT u.id,u.username,u.display_name FROM administrators u JOIN admin_roles ar ON ar.id=u.admin_role_id
 WHERE u.role='admin' AND u.status='active' AND u.sales_enabled AND ar.active AND 'customers.view'=ANY(ar.permissions) ORDER BY u.display_name,u.id`))
}

func (s *Store) SalesReportOwnerOptions(ctx context.Context, actor int64) ([]map[string]any, error) {
	return collectMaps(s.Pool.Query(ctx, `SELECT DISTINCT coalesce(c.sales_owner_id,0) id,coalesce(u.display_name,'未分配') name
 FROM users c LEFT JOIN administrators u ON u.id=c.sales_owner_id
 WHERE c.role='customer' AND admin_customer_allowed($1,c.id) ORDER BY 2,1`, actor))
}

func (s *Store) AssignCustomerSalesOwner(ctx context.Context, actor, id, expectedVersion int64, owner *int64) (map[string]any, error) {
	return s.AssignCustomerSalesOwnerWithKey(ctx, actor, id, expectedVersion, owner, "")
}
