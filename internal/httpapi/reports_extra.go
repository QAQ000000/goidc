package httpapi

import (
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

func (s *Server) salesReport(w http.ResponseWriter, r *http.Request) {
	p, e := parseReportPeriod(r, false)
	if e != nil {
		s.reportInputFailure(w, r, e)
		return
	}
	rows, e := s.db.Pool.Query(r.Context(), `SELECT coalesce(u.sales_owner_id,0) AS sales_owner_id,coalesce(o.display_name,'未分配') AS sales_owner_name,count(DISTINCT ord.id)::bigint AS order_count,coalesce(sum(i.amount_cents),0)::bigint AS income_cents FROM orders ord JOIN users u ON u.id=ord.user_id LEFT JOIN users o ON o.id=u.sales_owner_id LEFT JOIN invoices i ON i.order_id=ord.id AND i.status='paid' AND i.paid_at>=$1 AND i.paid_at<$2 WHERE ord.created_at>=$1 AND ord.created_at<$2 AND admin_customer_allowed($3,u.id) GROUP BY 1,2 ORDER BY 4 DESC,1 LIMIT 100`, p.Start, p.End, identityFrom(r).ID)
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
	write(w, 200, map[string]any{"year": p.Year, "items": items})
}
func (s *Server) ticketReport(w http.ResponseWriter, r *http.Request) {
	p, e := parseReportPeriod(r, true)
	if e != nil {
		s.reportInputFailure(w, r, e)
		return
	}
	rows, e := s.db.Pool.Query(r.Context(), `SELECT to_char(d.day,'YYYY-MM-DD') AS date,count(t.id)::bigint AS ticket_count,count(t.id) FILTER(WHERE t.status IN ('open','pending'))::bigint AS open_count FROM generate_series($1::date,($2::date-interval '1 day')::date,interval '1 day') d(day) LEFT JOIN tickets t ON (t.created_at AT TIME ZONE 'Asia/Shanghai')::date=d.day AND admin_ticket_department_allowed($3,t.department_id) WHERE $4::boolean GROUP BY d.day ORDER BY d.day`, p.Start, p.End, identityFrom(r).ID, identityFrom(r).can("tickets.view"))
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
	write(w, 200, map[string]any{"year": p.Year, "month": p.Month, "items": items})
}

var _ = time.UTC
