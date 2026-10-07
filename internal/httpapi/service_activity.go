package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

var serviceActivityAuditActions = []string{
	"service.action.queued", "service.manual.completed", "service.nokvm.pending", "service.nokvm.running",
	"service.nokvm.failed", "service.nokvm.waiting_verification", "service.nokvm.completed", "service.nokvm.verified",
	"service.renewal.created", "service.auto_renew.updated", "service.configuration.submitted", "service.configuration.cancelled",
	"service.configuration.retried", "service.billing_cycle.completed", "service.simulation.completed",
	"service.cancellation.requested", "service.cancellation.withdrawn", "service.cancellation.approved", "service.cancellation.rejected",
}

var serviceActivityJobActions = []string{
	"create", "suspend", "resume", "terminate", "sync", "upgrade", "reset_password", "reinstall",
	"power_on", "power_off", "reboot", "force_off", "force_reboot", "rescue_system", "exit_rescue_system",
	"snapshot_create", "snapshot_delete", "snapshot_restore", "backup_create", "backup_delete", "backup_restore",
	"nat_acl_create", "nat_acl_delete", "nat_web_create", "nat_web_delete", "cdrom_mount", "cdrom_unmount",
}

var serviceActivityActions = func() map[string]bool {
	m := make(map[string]bool, len(serviceActivityAuditActions)+len(serviceActivityJobActions))
	for _, action := range serviceActivityAuditActions {
		m[action] = true
	}
	for _, action := range serviceActivityJobActions {
		m[action] = true
	}
	return m
}()

var serviceActivityCategories = map[string]bool{
	"lifecycle": true, "operation": true, "configuration": true, "billing": true, "cancellation": true,
}

var serviceActivityStatuses = map[string]bool{
	"pending": true, "running": true, "queued": true, "waiting_manual": true, "waiting_verification": true,
	"succeeded": true, "failed": true, "cancelled": true, "completed": true, "verified": true,
	"recorded": true, "requested": true, "withdrawn": true, "approved": true, "rejected": true,
}

func (s *Server) serviceActivity(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, offset, valid := serviceActivityPagination(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_pagination", "分页参数无效")
		return
	}
	action, valid := serviceActivityFilter(r, "action", serviceActivityActions)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_activity_action", "服务历史动作无效")
		return
	}
	category, valid := serviceActivityFilter(r, "category", serviceActivityCategories)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_activity_category", "服务历史分类无效")
		return
	}
	status, valid := serviceActivityFilter(r, "status", serviceActivityStatuses)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_activity_status", "服务历史状态无效")
		return
	}

	admin := adminRoute(r)
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var ownerID int64
	if err = tx.QueryRow(r.Context(), "SELECT user_id FROM services WHERE id=$1 AND ($2::boolean OR user_id=$3) FOR SHARE", id, admin, identityFrom(r).ID).Scan(&ownerID); err != nil {
		s.fail(w, r, err)
		return
	}
	if admin {
		err = store.LockAdminCustomer(r.Context(), tx, identityFrom(r).ID, ownerID)
	}
	// A scope or service lock may have waited while the credential was revoked.
	current := false
	s.auth(func(_ http.ResponseWriter, fresh *http.Request) { r, current = fresh, true }, admin)(w, r)
	if !current {
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}

	query := `WITH timeline AS (
SELECT 'audit'::text AS source,
       'audit-'||a.id::text AS id,
       a.id AS audit_id,
       NULL::bigint AS job_id,
       CASE WHEN a.action='service.action.queued' THEN a.payload->>'action' ELSE a.action END AS action,
       CASE
         WHEN a.action LIKE 'service.cancellation.%' THEN 'cancellation'
         WHEN a.action IN ('service.renewal.created','service.auto_renew.updated') THEN 'billing'
         WHEN a.action LIKE 'service.configuration.%' OR a.action='service.billing_cycle.completed' THEN 'configuration'
         WHEN a.action='service.action.queued' OR a.action='service.manual.completed' OR a.action LIKE 'service.nokvm.%' THEN 'operation'
         ELSE 'lifecycle'
       END AS category,
       CASE
         WHEN a.action='service.action.queued' THEN 'queued'
         WHEN a.action='service.nokvm.verified' THEN CASE a.payload->>'resolution' WHEN 'applied' THEN 'verified' WHEN 'not_applied' THEN 'failed' ELSE 'recorded' END
         WHEN a.action='service.manual.completed' OR a.action IN ('service.nokvm.completed','service.simulation.completed','service.billing_cycle.completed') THEN 'completed'
         WHEN a.action LIKE 'service.nokvm.%' THEN split_part(a.action,'.',3)
         WHEN a.action LIKE 'service.cancellation.%' THEN split_part(a.action,'.',3)
         ELSE 'recorded'
       END AS status,
		CASE WHEN a.actor_id IS NULL OR a.actor_id=0 THEN 'system'
		     WHEN a.actor_id=$2 AND NOT $3::boolean THEN 'you'
		     WHEN u.role IN ('admin','super_admin') THEN 'admin'
		     ELSE 'system' END AS actor_kind,
       CASE WHEN $3::boolean AND u.role IN ('admin','super_admin') THEN NULLIF(u.display_name,'') ELSE NULL END AS actor_display_name,
       a.created_at AS occurred_at,
       jsonb_strip_nulls(jsonb_build_object(
         'audit_id',a.id,
         'job_id',aj.id,
         'action',CASE WHEN a.action='service.action.queued' AND a.payload->>'action'=ANY($5::text[]) THEN a.payload->>'action' END
       )) AS metadata,
       NULL::integer AS attempts,
       a.id AS sequence
FROM audit_events a
LEFT JOIN users u ON u.id=a.actor_id
LEFT JOIN provision_jobs aj ON aj.service_id=$1::bigint AND aj.id::text=a.payload->>'job_id'
WHERE a.entity_type='service' AND a.entity_id=$1::text AND a.action=ANY($4::text[])
  AND (a.action<>'service.action.queued' OR a.payload->>'action'=ANY($5::text[]))
  AND NOT (a.action='service.action.queued' AND aj.id IS NOT NULL)
UNION ALL
SELECT 'job'::text AS source,
       'job-'||j.id::text AS id,
       NULL::bigint AS audit_id,
       j.id AS job_id,
       j.action,
       CASE
         WHEN j.action='upgrade' THEN 'configuration'
         WHEN j.action IN ('create','suspend','resume','terminate','sync') THEN 'lifecycle'
         ELSE 'operation'
       END AS category,
       j.status,
		CASE WHEN q.actor_id IS NULL OR q.actor_id=0 THEN 'system'
		     WHEN q.actor_id=$2 AND NOT $3::boolean THEN 'you'
		     WHEN u.role IN ('admin','super_admin') THEN 'admin'
		     ELSE 'system' END AS actor_kind,
       CASE WHEN $3::boolean AND u.role IN ('admin','super_admin') THEN NULLIF(u.display_name,'') ELSE NULL END AS actor_display_name,
       COALESCE(j.completed_at,j.updated_at,j.created_at) AS occurred_at,
       jsonb_build_object('job_id',j.id) AS metadata,
       j.attempts,
       j.id AS sequence
FROM provision_jobs j
LEFT JOIN LATERAL (
  SELECT a.actor_id FROM audit_events a
  WHERE a.entity_type='service' AND a.entity_id=$1::text AND a.action='service.action.queued'
    AND a.payload->>'job_id'=j.id::text
  ORDER BY a.id DESC LIMIT 1
) q ON true
LEFT JOIN users u ON u.id=q.actor_id
	WHERE j.service_id=$1::bigint AND j.action=ANY($5::text[])
), filtered AS (
SELECT * FROM timeline
WHERE ($6='' OR action=$6) AND ($7='' OR category=$7) AND ($8='' OR status=$8)
), page AS (
	SELECT source,id,audit_id,job_id,action,category,status,actor_kind,actor_display_name,occurred_at,metadata,attempts,sequence
	FROM filtered
ORDER BY occurred_at DESC,source DESC,sequence DESC
LIMIT $9 OFFSET $10
)
SELECT (SELECT count(*) FROM filtered) AS total,
	COALESCE((SELECT jsonb_agg(to_jsonb(page)-'sequence' ORDER BY occurred_at DESC,source DESC,sequence DESC) FROM page),'[]'::jsonb) AS items`
	rows, err := tx.Query(r.Context(), query, strconv.FormatInt(id, 10), identityFrom(r).ID, admin, serviceActivityAuditActions, serviceActivityJobActions, action, category, status, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result["service_id"], result["limit"], result["offset"] = id, limit, offset
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	write(w, http.StatusOK, result)
}

func serviceActivityFilter(r *http.Request, key string, allowed map[string]bool) (string, bool) {
	values, exists := r.URL.Query()[key]
	if !exists {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	return value, allowed[value]
}

func serviceActivityPagination(r *http.Request) (int, int, bool) {
	limit, offset := 25, 0
	if raw, ok := r.URL.Query()["limit"]; ok {
		if len(raw) != 1 || raw[0] == "" {
			return 0, 0, false
		}
		parsed, err := strconv.Atoi(raw[0])
		if err != nil || parsed < 1 || parsed > 100 {
			return 0, 0, false
		}
		limit = parsed
	}
	if raw, ok := r.URL.Query()["offset"]; ok {
		if len(raw) != 1 || raw[0] == "" {
			return 0, 0, false
		}
		parsed, err := strconv.Atoi(raw[0])
		if err != nil || parsed < 0 || parsed > 1000000 {
			return 0, 0, false
		}
		offset = parsed
	}
	return limit, offset, true
}
