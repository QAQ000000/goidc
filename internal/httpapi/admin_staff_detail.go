package httpapi

import (
	"net/http"

	"github.com/jackc/pgx/v5"
)

func (s *Server) adminStaffDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT u.id,u.username,u.email,u.display_name,u.role,u.status,
u.admin_role_id,r.name AS admin_role_name,r.active AS admin_role_active,u.admin_version,
u.mfa_enabled,u.created_at,u.updated_at,u.customer_scope,u.customer_ownerless,u.sales_enabled,
u.ticket_scope,u.ticket_unassigned,
ARRAY(SELECT department_id FROM admin_ticket_departments WHERE user_id=u.id ORDER BY department_id) AS ticket_department_ids
FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id
WHERE u.id=$1 AND u.role IN ('admin','super_admin')`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, item)
}
