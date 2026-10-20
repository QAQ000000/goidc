package httpapi

import (
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type salesReportItem = store.SalesReportItem

type ticketReportItem struct {
	Date        string `json:"date"`
	TicketCount int64  `json:"ticket_count"`
	OpenCount   int64  `json:"open_count"`
}

func (s *Server) salesReport(w http.ResponseWriter, r *http.Request) {
	p, e := parseReportPeriod(r, false)
	if e != nil {
		s.reportInputFailure(w, r, e)
		return
	}
	items, e := s.db.SalesSummary(r.Context(), p.Start, p.End, identityFrom(r).ID)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if items == nil {
		items = []salesReportItem{}
	}
	writeReport(w, r, map[string]any{"year": p.Year, "items": items})
}
func (s *Server) ticketReport(w http.ResponseWriter, r *http.Request) {
	p, e := parseReportPeriod(r, true)
	if e != nil {
		s.reportInputFailure(w, r, e)
		return
	}
	if !identityFrom(r).can("tickets.view") {
		writeError(w, http.StatusForbidden, "permission_denied", "没有查看工单统计的权限")
		return
	}
	rows, e := s.db.Pool.Query(r.Context(), `
SELECT to_char(d.day,'YYYY-MM-DD') AS date,
       count(t.id)::bigint AS ticket_count,
       count(t.id) FILTER(WHERE t.merged_ticket_id IS NULL AND t.status IN ('open','pending'))::bigint AS open_count
FROM generate_series(($1::timestamptz AT TIME ZONE 'Asia/Shanghai')::date,
                     (($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date-interval '1 day')::date,
                     interval '1 day') d(day)
LEFT JOIN tickets t ON (t.created_at AT TIME ZONE 'Asia/Shanghai')::date=d.day
 AND t.created_at >= $1 AND t.created_at < $2
 AND admin_ticket_department_allowed($3,t.department_id)
GROUP BY d.day ORDER BY d.day`, p.Start, p.End, identityFrom(r).ID)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	items, e := pgx.CollectRows(rows, pgx.RowToStructByName[ticketReportItem])
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if items == nil {
		items = []ticketReportItem{}
	}
	writeReport(w, r, map[string]any{"year": p.Year, "month": p.Month, "items": items})
}
