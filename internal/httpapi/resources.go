package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) resourcesPage(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := "%" + strings.TrimSpace(r.URL.Query().Get("q")) + "%"
	ip := "%" + strings.TrimSpace(r.URL.Query().Get("ip")) + "%"
	supplier := strings.TrimSpace(r.URL.Query().Get("supplier_id"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if supplier != "" {
		if _, err := parsePositiveID(supplier); err != nil {
			writeError(w, 400, "invalid_supplier", "供应商筛选无效")
			return
		}
	}
	// import_review_required is the services boolean flag, never a status CHECK
	// value; the filter below maps it to that flag instead of an empty result.
	if status != "" {
		valid := map[string]bool{"pending_payment": true, "queued": true, "provisioning": true, "active": true, "suspended": true, "terminated": true, "failed": true, "cancelled": true, "import_review_required": true}
		if !valid[status] {
			writeError(w, 400, "invalid_status", "资源状态无效")
			return
		}
	}
	var total int64
	var items json.RawMessage
	err := s.db.Pool.QueryRow(r.Context(), `WITH filtered AS (
 SELECT s.id,s.name,s.domain,s.status,s.username,s.upstream_id,jsonb_strip_nulls(jsonb_build_object('ips',s.resource_info->'ips','dedicated_ip',s.resource_info->'dedicated_ip','assigned_ips',s.resource_info->'assigned_ips','name',s.resource_info->'name','power',s.resource_info->'power')) resource_info,s.user_id,u.display_name customer_name,
  a.id supplier_id,a.name supplier_name
 FROM services s JOIN users u ON u.id=s.user_id
 LEFT JOIN supplier_accounts a ON a.id::text=s.delivery_snapshot->>'supplier_id'
 WHERE admin_customer_allowed($7,s.user_id) AND (s.name ILIKE $1 OR s.domain ILIKE $1 OR u.display_name ILIKE $1 OR s.upstream_id ILIKE $1)
 AND (coalesce(s.resource_info->>'dedicated_ip','') ILIKE $2 OR coalesce(s.resource_info->>'assigned_ips','') ILIKE $2 OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(s.resource_info->'ips')='array' THEN s.resource_info->'ips' ELSE '[]'::jsonb END) ip WHERE ip ILIKE $2) OR $2='%%') AND ($3='' OR a.id::text=$3)
 AND (CASE WHEN $4='import_review_required' THEN s.import_review_required ELSE ($4='' OR s.status=$4) END)
	) SELECT (SELECT count(*) FROM filtered),coalesce((SELECT jsonb_agg(row_to_json(page)) FROM (SELECT * FROM filtered ORDER BY id DESC LIMIT $5 OFFSET $6) page),'[]'::jsonb)`, q, ip, supplier, status, limit, offset, identityFrom(r).ID).Scan(&total, &items)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var suppliers json.RawMessage
	if err = s.db.Pool.QueryRow(r.Context(), `SELECT coalesce(jsonb_agg(jsonb_build_object('id',id,'name',name) ORDER BY name,id),'[]'::jsonb) FROM supplier_accounts a WHERE EXISTS(SELECT 1 FROM services s WHERE s.delivery_snapshot->>'supplier_id'=a.id::text AND admin_customer_allowed($1,s.user_id))`, identityFrom(r).ID).Scan(&suppliers); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"total": total, "items": items, "suppliers": suppliers, "limit": limit, "offset": offset})
}
