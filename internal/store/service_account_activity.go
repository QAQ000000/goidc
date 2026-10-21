package store

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

var ServiceActivityAuditActions = []string{
	"service.action.queued", "service.manual.completed", "service.nokvm.pending", "service.nokvm.running",
	"service.nokvm.failed", "service.nokvm.waiting_verification", "service.nokvm.completed", "service.nokvm.verified",
	"service.renewal.created", "service.auto_renew.updated", "service.configuration.submitted", "service.configuration.cancelled",
	"service.pricing.updated", "service.renewal.fixed", "service.term.correction", "service.term.compensation",
	"service.status.corrected",
	"service.configuration.retried", "service.billing_cycle.completed", "service.simulation.completed",
	"service.cancellation.requested", "service.cancellation.withdrawn", "service.cancellation.approved", "service.cancellation.rejected",
	"supplier.lifecycle.manual_requested", "supplier.lifecycle.manual_verified", "supplier.lifecycle.manual_cancelled",
}

var ServiceActivityJobActions = []string{
	"create", "suspend", "resume", "terminate", "sync", "upgrade", "reset_password", "reinstall",
	"power_on", "power_off", "reboot", "force_off", "force_reboot", "rescue_system", "exit_rescue_system",
	"snapshot_create", "snapshot_delete", "snapshot_restore", "backup_create", "backup_delete", "backup_restore",
	"nat_acl_create", "nat_acl_delete", "nat_web_create", "nat_web_delete", "cdrom_mount", "cdrom_unmount",
}

type ServiceActivityAccountFilter struct {
	ServiceID, Actor         int64
	Admin                    bool
	Action, Category, Status string
	Limit, Offset            int
}

func (s *Store) ServiceAccountActivity(ctx context.Context, in ServiceActivityAccountFilter) (map[string]any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var ownerID int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM services WHERE id=$1 AND ($2::boolean OR user_id=$3) FOR SHARE", in.ServiceID, in.Admin, in.Actor).Scan(&ownerID); err != nil {
		return nil, err
	}
	var scopeErr error
	if in.Admin {
		scopeErr = LockAdminCustomer(ctx, tx, in.Actor, ownerID)
	}
	// Service and scope locks may wait. Bind the read to the live credential,
	// authoritative account and role before returning private activity.
	if err = LockFileCredential(ctx, tx, in.Actor); err != nil {
		return nil, err
	}
	if err = ticketFileCredentialCurrent(ctx, tx, in.Actor); err != nil {
		return nil, err
	}
	if in.Admin {
		if err = RequireAdministratorPermission(ctx, tx, in.Actor, "services.view"); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, conflict("unauthorized", "登录已失效，请重新登录")
			}
			return nil, err
		}
	} else {
		var customer int64
		if err = tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 AND status='active' FOR SHARE", in.Actor).Scan(&customer); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, conflict("unauthorized", "登录已失效，请重新登录")
			}
			return nil, err
		}
		if credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential); ok && credential.APIKey {
			var enabled bool
			if err = tx.QueryRow(ctx, "SELECT enabled FROM resource_api_settings WHERE id=1").Scan(&enabled); err != nil {
				return nil, err
			}
			if !enabled {
				return nil, ticketAccountError(403, "api_access_disabled", "客户 API 接口暂未开放")
			}
		}
	}
	if scopeErr != nil {
		return nil, scopeErr
	}
	query := `WITH timeline AS (
SELECT 'audit'::text AS source,
       'audit-'||a.id::text AS id,
       a.id AS audit_id,
       NULL::bigint AS job_id,
       CASE WHEN a.action='service.action.queued' THEN a.payload->>'action' ELSE a.action END AS action,
       CASE
         WHEN a.action LIKE 'service.cancellation.%' THEN 'cancellation'
         WHEN a.action IN ('service.renewal.created','service.auto_renew.updated','service.pricing.updated','service.renewal.fixed','service.term.correction','service.term.compensation') THEN 'billing'
         WHEN a.action LIKE 'service.configuration.%' OR a.action='service.billing_cycle.completed' THEN 'configuration'
         WHEN a.action='service.action.queued' OR a.action='service.manual.completed' OR a.action LIKE 'service.nokvm.%' OR a.action LIKE 'supplier.lifecycle.%' THEN 'operation'
         ELSE 'lifecycle'
       END AS category,
       CASE
         WHEN a.action='service.action.queued' THEN 'queued'
         WHEN a.action='supplier.lifecycle.manual_requested' THEN 'waiting_manual'
         WHEN a.action='supplier.lifecycle.manual_verified' THEN CASE a.payload->>'outcome' WHEN 'delivered' THEN 'verified' WHEN 'not_applied' THEN 'failed' ELSE 'recorded' END
         WHEN a.action='supplier.lifecycle.manual_cancelled' THEN 'cancelled'
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
         'action',CASE WHEN a.action IN ('service.action.queued','supplier.lifecycle.manual_requested','supplier.lifecycle.manual_verified','supplier.lifecycle.manual_cancelled') AND a.payload->>'action'=ANY($5::text[]) THEN a.payload->>'action' END,
         'outcome',CASE WHEN a.action='supplier.lifecycle.manual_verified' AND a.payload->>'outcome' IN ('delivered','not_applied') THEN a.payload->>'outcome' END
       )) AS metadata,
       NULL::integer AS attempts,
       a.id AS sequence
FROM audit_events a
LEFT JOIN account_identities u ON u.id=a.actor_id
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
LEFT JOIN account_identities u ON u.id=q.actor_id
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
	rows, err := tx.Query(ctx, query, strconv.FormatInt(in.ServiceID, 10), in.Actor, in.Admin, ServiceActivityAuditActions, ServiceActivityJobActions, in.Action, in.Category, in.Status, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	result, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	result["service_id"], result["limit"], result["offset"] = in.ServiceID, in.Limit, in.Offset
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
