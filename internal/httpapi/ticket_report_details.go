package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const ticketHandlerReportSQL = `WITH ticket_totals AS (
 SELECT coalesce(t.assignee_id,0) handler_id,count(*)::bigint ticket_count,
 count(*) FILTER(WHERE t.merged_ticket_id IS NULL AND t.status IN ('open','pending'))::bigint open_count,
 count(*) FILTER(WHERE t.status='closed')::bigint closed_count
 FROM tickets t WHERE t.created_at >= $1 AND t.created_at < $2 AND admin_ticket_department_allowed($3,t.department_id)
 AND ($4::bigint IS NULL OR coalesce(t.assignee_id,0)=$4) GROUP BY 1
), public_messages AS (
 SELECT r.author_id handler_id,r.created_at,tr.stars FROM ticket_replies r JOIN tickets t ON t.id=r.ticket_id LEFT JOIN ticket_ratings tr ON tr.reply_id=r.id
 WHERE r.author_kind='staff' AND admin_ticket_department_allowed($3,t.department_id)
 UNION ALL
 SELECT t.created_by,t.created_at,tr.stars FROM tickets t LEFT JOIN ticket_ratings tr ON tr.initial_ticket_id=t.id
 WHERE t.initial_author_kind='staff' AND t.created_by IS NOT NULL AND admin_ticket_department_allowed($3,t.department_id)
), reply_totals AS (
 SELECT handler_id,count(*)::bigint reply_count,count(stars)::bigint rated_reply_count,coalesce(sum(stars),0)::bigint total_score,
 count(*) FILTER(WHERE stars=1)::bigint one_star_count,count(*) FILTER(WHERE stars=2)::bigint two_star_count,
 count(*) FILTER(WHERE stars=3)::bigint three_star_count,count(*) FILTER(WHERE stars=4)::bigint four_star_count,count(*) FILTER(WHERE stars=5)::bigint five_star_count
 FROM public_messages WHERE created_at >= $1 AND created_at < $2 AND ($4::bigint IS NULL OR handler_id=$4) GROUP BY 1
), result AS (
 SELECT coalesce(t.handler_id,r.handler_id) handler_id,coalesce(u.display_name,'未分派') handler_name,
 coalesce(t.ticket_count,0)::bigint ticket_count,coalesce(t.open_count,0)::bigint open_count,coalesce(t.closed_count,0)::bigint closed_count,
 coalesce(r.reply_count,0)::bigint reply_count,coalesce(r.rated_reply_count,0)::bigint rated_reply_count,coalesce(r.total_score,0)::bigint total_score,
 coalesce(r.one_star_count,0)::bigint one_star_count,coalesce(r.two_star_count,0)::bigint two_star_count,coalesce(r.three_star_count,0)::bigint three_star_count,coalesce(r.four_star_count,0)::bigint four_star_count,coalesce(r.five_star_count,0)::bigint five_star_count
 FROM ticket_totals t FULL JOIN reply_totals r ON r.handler_id=t.handler_id LEFT JOIN users u ON u.id=coalesce(t.handler_id,r.handler_id)
) `

func (s *Server) ticketHandlerReport(w http.ResponseWriter, r *http.Request) {
	if !identityFrom(r).can("tickets.view") {
		writeError(w, 403, "permission_denied", "没有查看工单统计的权限")
		return
	}
	p, err := parseReportPeriod(r, true)
	if s.reportInputFailure(w, r, err) {
		return
	}
	start, end := p.Start, p.End
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if from != "" || to != "" {
		loc := time.FixedZone("Asia/Shanghai", 8*60*60)
		a, e1 := time.ParseInLocation("2006-01-02", from, loc)
		b, e2 := time.ParseInLocation("2006-01-02", to, loc)
		if e1 != nil || e2 != nil || a.After(b) || b.Sub(a) > 366*24*time.Hour {
			writeError(w, 400, "invalid_report_period", "日期区间需完整且不超过 367 天")
			return
		}
		start, end = a, b.AddDate(0, 0, 1)
	}
	var handler *int64
	if value := r.URL.Query().Get("assignee_id"); value != "" {
		id, e := strconv.ParseInt(value, 10, 64)
		if e != nil || id < 0 {
			writeError(w, 400, "invalid_handler", "处理人筛选无效")
			return
		}
		handler = &id
	}
	sort := r.URL.Query().Get("sort")
	if sort == "" {
		sort = "ticket_count"
	}
	allowed := map[string]bool{"handler_id": true, "handler_name": true, "ticket_count": true, "open_count": true, "closed_count": true, "reply_count": true, "rated_reply_count": true, "total_score": true, "one_star_count": true, "two_star_count": true, "three_star_count": true, "four_star_count": true, "five_star_count": true}
	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "desc"
	}
	if !allowed[sort] || (direction != "asc" && direction != "desc") {
		writeError(w, 400, "invalid_report_sort", "排序字段或方向无效")
		return
	}
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	limit, offset := pagination(r)
	result, err := listPage(r.Context(), tx, ticketHandlerReportSQL+"SELECT count(*) FROM result", ticketHandlerReportSQL+"SELECT * FROM result ORDER BY "+sort+" "+direction+",handler_id", []any{start, end, identityFrom(r).ID, handler}, limit, offset, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	optionRows, err := tx.Query(r.Context(), ticketHandlerReportSQL+"SELECT handler_id,handler_name FROM result ORDER BY handler_name,handler_id", start, end, identityFrom(r).ID, nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	options, err := pgx.CollectRows(optionRows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if options == nil {
		options = []map[string]any{}
	}
	result["handlers"] = options
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	result["from"] = start.Format("2006-01-02")
	result["to"] = end.AddDate(0, 0, -1).Format("2006-01-02")
	result["sort"] = sort
	result["direction"] = direction
	result["rating_period_basis"] = "public_message_created_at"
	result["ticket_assignment_basis"] = "current_assignee"
	writeReport(w, r, result)
}
