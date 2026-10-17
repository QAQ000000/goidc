package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const adminJobsCTE = `WITH jobs AS (
 SELECT 'provision'::text source,j.id,s.id service_id,u.id customer_id,u.display_name customer,p.name product,
 CASE WHEN j.action IN ('create','suspend','resume','terminate','sync','upgrade','reinstall','reset_password','reboot','power_on','power_off') THEN j.action ELSE 'operation' END action,
 '服务操作任务'::text description,j.status,j.attempts,j.available_at,j.created_at,j.updated_at,(j.last_error<>'') has_error
 FROM provision_jobs j JOIN services s ON s.id=j.service_id JOIN users u ON u.id=s.user_id JOIN products p ON p.id=s.product_id
 WHERE $2::boolean AND admin_customer_allowed($1,u.id)
 UNION ALL
 SELECT 'email',j.id,s.id,u.id,coalesce(u.display_name,'系统任务'),coalesce(p.name,'—'),'notification','邮件投递任务',j.status,j.attempts,j.available_at,j.created_at,coalesce(j.sent_at,j.locked_at,j.created_at),(j.last_error<>'')
 FROM email_outbox j LEFT JOIN LATERAL (SELECT user_id,service_id FROM notification_delivery_events WHERE email_outbox_id=j.id ORDER BY id LIMIT 1) e ON true
 LEFT JOIN users u ON u.id=e.user_id LEFT JOIN services s ON s.id=e.service_id LEFT JOIN products p ON p.id=s.product_id
 WHERE $3::boolean AND ((u.id IS NOT NULL AND admin_customer_allowed($1,u.id)) OR (u.id IS NULL AND $4::boolean))
 UNION ALL
 SELECT 'sms',j.id,s.id,u.id,coalesce(u.display_name,'系统任务'),coalesce(p.name,'—'),'notification','短信投递任务',j.status,j.attempts,j.available_at,j.created_at,coalesce(j.sent_at,j.locked_at,j.created_at),(j.last_error<>'')
 FROM sms_outbox j LEFT JOIN LATERAL (SELECT user_id,service_id FROM notification_delivery_events WHERE sms_outbox_id=j.id ORDER BY id LIMIT 1) e ON true
 LEFT JOIN users u ON u.id=e.user_id LEFT JOIN services s ON s.id=e.service_id LEFT JOIN products p ON p.id=s.product_id
 WHERE $3::boolean AND ((u.id IS NOT NULL AND admin_customer_allowed($1,u.id)) OR (u.id IS NULL AND $4::boolean))
) `

func jobScope(r *http.Request) []any {
	i := identityFrom(r)
	return []any{i.ID, i.can("services.view"), i.can("notifications.view"), i.Role == "super_admin"}
}

func (s *Server) adminJobs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Pool.Query(r.Context(), adminJobsCTE+`SELECT source,
 count(*)::bigint AS total,
 count(*) FILTER (WHERE status IN ('pending','queued'))::bigint AS pending,
 count(*) FILTER (WHERE status IN ('running','sending'))::bigint AS running,
 count(*) FILTER (WHERE status IN ('failed','dead_letter'))::bigint AS failed,
 count(*) FILTER (WHERE status IN ('waiting_manual','waiting_verification'))::bigint AS waiting
 FROM jobs GROUP BY source ORDER BY source`, jobScope(r)...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) adminJobQueue(w http.ResponseWriter, r *http.Request) {
	limit, offset, valid := serviceActivityPagination(r)
	if !valid {
		writeError(w, 400, "invalid_pagination", "分页参数无效")
		return
	}
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source != "" && source != "provision" && source != "email" && source != "sms" {
		writeError(w, http.StatusBadRequest, "invalid_job_source", "任务来源无效")
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !validJobStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid_job_status", "任务状态无效")
		return
	}
	args := jobScope(r)
	where := []string{"TRUE"}
	if source != "" {
		args = append(args, source)
		where = append(where, fmt.Sprintf("source=$%d", len(args)))
	}
	if status != "" {
		args = append(args, status)
		where = append(where, fmt.Sprintf("status=$%d", len(args)))
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		args = append(args, "%"+q+"%")
		where = append(where, fmt.Sprintf("(customer ILIKE $%d OR product ILIKE $%d OR action ILIKE $%d OR description ILIKE $%d)", len(args), len(args), len(args), len(args)))
	}
	for _, key := range []string{"from", "to"} {
		if value := r.URL.Query().Get(key); value != "" {
			d, err := time.Parse("2006-01-02", value)
			if err != nil {
				writeError(w, 400, "invalid_job_date", "日期无效")
				return
			}
			op := ">="
			if key == "to" {
				d = d.AddDate(0, 0, 1)
				op = "<"
			}
			args = append(args, d)
			where = append(where, fmt.Sprintf("updated_at %s $%d", op, len(args)))
		}
	}

	whereSQL := strings.Join(where, " AND ")
	s.writeListPage(w, r, adminJobsCTE+"SELECT count(*) FROM jobs WHERE "+whereSQL,
		adminJobsCTE+"SELECT source,id,service_id,customer_id,customer,product,action,description,status,attempts,available_at,created_at,updated_at,has_error FROM jobs WHERE "+whereSQL+" ORDER BY created_at DESC,id DESC", args, limit, offset)
}

func validJobStatus(value string) bool {
	switch value {
	case "pending", "queued", "running", "sending", "waiting_manual", "waiting_verification", "succeeded", "sent", "failed", "dead_letter", "cancelled", "expired":
		return true
	default:
		return false
	}
}
