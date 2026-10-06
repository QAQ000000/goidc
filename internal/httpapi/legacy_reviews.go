package httpapi

import (
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) legacyImportRuns(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	s.list(w, r, `SELECT id,source,mode,status,started_at,completed_at,counts,
(SELECT count(*) FROM legacy_import_issues i WHERE i.run_id=legacy_import_runs.id) AS issue_count
FROM legacy_import_runs ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset)
}

func (s *Server) legacyReviewList(w http.ResponseWriter, r *http.Request) {
	entity, state := r.URL.Query().Get("entity"), r.URL.Query().Get("state")
	if (entity != "" && entity != "invoices" && entity != "services") || (state != "" && state != "pending" && state != "archive" && state != "release") {
		writeError(w, 400, "invalid_review_filter", "复核筛选条件无效")
		return
	}
	limit, offset := pagination(r)
	s.list(w, r, `WITH objects AS (
SELECT 'invoices'::text AS entity,id,legacy_source AS source,legacy_id,invoice_no AS name,user_id,status,amount_cents,import_review_required,import_review_version FROM invoices WHERE legacy_source<>''
UNION ALL
SELECT 'services'::text,id,legacy_source,legacy_id,name,user_id,status,coalesce(recurring_price_cents,(SELECT price_cents FROM order_items WHERE id=services.order_item_id),0),import_review_required,import_review_version FROM services WHERE legacy_source<>''
), reviewed AS (
SELECT o.*,u.display_name AS customer_name,coalesce(d.decision,'pending') AS review_state,d.created_at AS reviewed_at
FROM objects o JOIN users u ON u.id=o.user_id LEFT JOIN LATERAL
(SELECT decision,created_at FROM legacy_review_decisions WHERE entity=o.entity AND target_id=o.id ORDER BY version DESC LIMIT 1) d ON true)
SELECT * FROM reviewed WHERE ($1='' OR entity=$1) AND ($2='' OR review_state=$2) AND ($3='' OR source=$3)
ORDER BY (review_state='pending') DESC,source,entity,id LIMIT $4 OFFSET $5`, entity, state, r.URL.Query().Get("source"), limit, offset)
}

func (s *Server) legacyReviewDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := s.db.LegacyReviewDetail(r.Context(), r.PathValue("entity"), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}

func (s *Server) reviewLegacy(w http.ResponseWriter, r *http.Request) {
	if identityFrom(r).Role != "super_admin" {
		writeError(w, 403, "super_admin_required", "迁移复核需要超级管理员权限")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.LegacyReviewInput
	if !decode(w, r, &input) {
		return
	}
	out, err := s.db.ReviewLegacy(r.Context(), identityFrom(r).ID, r.PathValue("entity"), id, r.Header.Get("Idempotency-Key"), input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if out.Replayed {
		status = http.StatusOK
	}
	write(w, status, out)
}
