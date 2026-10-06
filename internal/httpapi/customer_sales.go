package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) salesOwners(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, `SELECT u.id,u.username,u.display_name FROM users u JOIN admin_roles ar ON ar.id=u.admin_role_id
 WHERE u.role='admin' AND u.status='active' AND u.sales_enabled AND ar.active AND 'customers.view'=ANY(ar.permissions) ORDER BY u.display_name,u.id`)
}

func (s *Server) assignCustomerSalesOwner(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Owner           json.RawMessage `json:"sales_owner_id"`
		ExpectedVersion int64           `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	var owner *int64
	if len(input.Owner) == 0 || json.Unmarshal(input.Owner, &owner) != nil || input.ExpectedVersion < 1 || (owner != nil && *owner < 1) {
		writeError(w, 400, "invalid_sales_assignment", "销售归属或版本无效")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('customer-sales:'||$1::bigint::text,0))", id); err != nil {
		s.fail(w, r, err)
		return
	}
	var previous *int64
	var version int64
	if err = tx.QueryRow(r.Context(), "SELECT sales_owner_id,sales_version FROM users WHERE id=$1 AND role='customer' FOR UPDATE", id).Scan(&previous, &version); err != nil {
		s.fail(w, r, err)
		return
	}
	if version != input.ExpectedVersion {
		writeError(w, 409, "version_conflict", "客户归属已变更，请重新加载")
		return
	}
	if owner != nil {
		var ownerID int64
		if err = tx.QueryRow(r.Context(), `SELECT u.id FROM users u JOIN admin_roles ar ON ar.id=u.admin_role_id WHERE u.id=$1 AND u.role='admin' AND u.status='active' AND u.sales_enabled AND ar.active AND 'customers.view'=ANY(ar.permissions) FOR SHARE OF u,ar`, *owner).Scan(&ownerID); err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, 409, "invalid_sales_owner", "请选择已启用的销售管理员及客户读取角色")
			} else {
				s.fail(w, r, err)
			}
			return
		}
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET sales_owner_id=$2,sales_version=sales_version+1,updated_at=now() WHERE id=$1", id, owner); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "customer.sales_assigned", "user", id, map[string]any{"previous_owner_id": previous, "sales_owner_id": owner}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"customer_id": id, "sales_owner_id": owner, "sales_version": version + 1})
}
