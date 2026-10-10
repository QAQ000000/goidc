package httpapi

import (
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

func schedulerFilter(r *http.Request) (string, []any, bool) {
	where := []string{"TRUE"}
	args := []any{}
	if key := r.URL.Query().Get("task"); key != "" {
		switch key {
		case "affiliate_settlement", "service_expiry_notifications", "billing_notifications", "supplier_sync_schedule":
		default:
			return "", nil, false
		}
		args = append(args, key)
		where = append(where, fmt.Sprintf("task_key=$%d", len(args)))
	}
	if status := r.URL.Query().Get("status"); status != "" {
		switch status {
		case "running", "succeeded", "failed", "interrupted":
		default:
			return "", nil, false
		}
		args = append(args, status)
		where = append(where, fmt.Sprintf("status=$%d", len(args)))
	}
	for _, key := range []string{"from", "to"} {
		if raw := r.URL.Query().Get(key); raw != "" {
			d, e := time.ParseInLocation("2006-01-02", raw, time.FixedZone("Asia/Shanghai", 8*60*60))
			if e != nil {
				return "", nil, false
			}
			op := ">="
			if key == "to" {
				d = d.AddDate(0, 0, 1)
				op = "<"
			}
			args = append(args, d)
			where = append(where, fmt.Sprintf("started_at %s $%d", op, len(args)))
		}
	}
	return strings.Join(where, " AND "), args, true
}
func (s *Server) adminJobExecutionLog(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := serviceActivityPagination(r)
	if !ok {
		writeError(w, 400, "invalid_pagination", "分页参数无效")
		return
	}
	where, args, ok := schedulerFilter(r)
	if !ok {
		writeError(w, 400, "invalid_scheduler_filter", "日志筛选参数无效")
		return
	}
	var total int
	if e := s.db.Pool.QueryRow(r.Context(), "SELECT count(*) FROM scheduler_runs WHERE "+where, args...).Scan(&total); e != nil {
		s.fail(w, r, e)
		return
	}
	args = append(args, limit, offset)
	rows, e := s.db.Pool.Query(r.Context(), "SELECT id,task_key,started_at,finished_at,duration_ms,status,affected_count,error_code FROM scheduler_runs WHERE "+where+fmt.Sprintf(" ORDER BY started_at DESC,id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	items, e := pgx.CollectRows(rows, pgx.RowToMap)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}
func (s *Server) adminSchedulerTrend(w http.ResponseWriter, r *http.Request) {
	where, args, ok := schedulerFilter(r)
	if !ok {
		writeError(w, 400, "invalid_scheduler_filter", "日志筛选参数无效")
		return
	}
	rows, e := s.db.Pool.Query(r.Context(), "SELECT to_char(started_at AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD') AS \"day\",count(*) total,count(*) FILTER(WHERE status='succeeded') succeeded,count(*) FILTER(WHERE status='failed') failed,count(*) FILTER(WHERE status='running') running,count(*) FILTER(WHERE status='interrupted') interrupted FROM scheduler_runs WHERE "+where+" AND started_at>=now()-interval '31 days' GROUP BY 1 ORDER BY 1", args...)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	items, e := pgx.CollectRows(rows, pgx.RowToMap)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, 200, map[string]any{"items": items, "timezone": "Asia/Shanghai", "window_days": 31})
}
