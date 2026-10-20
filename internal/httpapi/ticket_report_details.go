package httpapi

import (
	"net/http"
	"strconv"
	"time"
)

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
	limit, offset := pagination(r)
	result, err := s.db.TicketHandlerReport(r.Context(), identityFrom(r).ID, start, end, handler, sort, direction, limit, offset)
	if err != nil {
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
