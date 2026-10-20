package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func customerListFilterID(w http.ResponseWriter, r *http.Request, key string) (*int64, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" || raw == "all" {
		return nil, true
	}
	if raw == "none" {
		value := int64(0)
		return &value, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		writeError(w, 400, "invalid_filter", "筛选条件无效")
		return nil, false
	}
	return &value, true
}

func (s *Server) customersPage(w http.ResponseWriter, r *http.Request) {
	group, ok := customerListFilterID(w, r, "group_id")
	if !ok {
		return
	}
	owner, ok := customerListFilterID(w, r, "sales_owner_id")
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "active" && status != "disabled" {
		writeError(w, 400, "invalid_status", "客户状态无效")
		return
	}
	limit, offset := pagination(r)
	page, err := s.db.AdminCustomerDirectoryPage(r.Context(), identityFrom(r).ID, strings.TrimSpace(r.URL.Query().Get("q")), group, owner, status, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, page)
}

func (s *Server) adminCertificationsPage(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != "unverified" && status != "pending" && status != "approved" && status != "rejected" {
		writeError(w, 400, "invalid_certification_status", "认证状态无效")
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("certification_type"))
	if kind != "" && kind != "individual" && kind != "company" {
		writeError(w, 400, "invalid_certification_type", "认证类型无效")
		return
	}
	provider := strings.TrimSpace(r.URL.Query().Get("provider_id"))
	if len(provider) > 80 {
		writeError(w, 400, "invalid_certification_provider", "认证接口无效")
		return
	}
	limit, offset := pagination(r)
	query := `SELECT p.id,p.user_id,u.username,u.display_name,u.email,p.certification_type,p.status,p.provider_id,p.rejection_reason,p.version,p.updated_at,
 (SELECT max(id) FROM certification_submissions x WHERE x.profile_id=p.id) AS submission_id
 FROM certification_profiles p JOIN users u ON u.id=p.user_id
 WHERE ($1='' OR p.status=$1) AND ($2='' OR p.certification_type=$2) AND ($3='' OR p.provider_id=$3)
 AND (u.username ILIKE $4 OR u.display_name ILIKE $4 OR u.email ILIKE $4 OR p.id::text ILIKE $4)
 AND admin_customer_allowed($5,u.id)`
	s.adminListPage(w, r, query, "updated_at DESC,id DESC", limit, offset, status, kind, provider, "%"+strings.TrimSpace(r.URL.Query().Get("q"))+"%", identityFrom(r).ID)
}

// One statement keeps the total and page on the same database snapshot.
func (s *Server) adminListPage(w http.ResponseWriter, r *http.Request, query, order string, limit, offset int, args ...any) {
	limitArg, offsetArg := strconv.Itoa(len(args)+1), strconv.Itoa(len(args)+2)
	rows, err := s.db.Pool.Query(r.Context(), `WITH filtered AS (`+query+`) SELECT (SELECT count(*) FROM filtered) AS total,
 coalesce((SELECT jsonb_agg(row_to_json(page)) FROM (SELECT * FROM filtered ORDER BY `+order+` LIMIT $`+limitArg+` OFFSET $`+offsetArg+`) page),'[]'::jsonb) AS items`, append(args, limit, offset)...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result["limit"], result["offset"] = limit, offset
	write(w, 200, result)
}
