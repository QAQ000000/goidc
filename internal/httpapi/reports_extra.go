package httpapi

import (
	"net/http"

	"github.com/jackc/pgx/v5"
)

type salesReportItem struct {
	SalesOwnerID   int64  `json:"sales_owner_id"`
	SalesOwnerName string `json:"sales_owner_name"`
	OrderCount     int64  `json:"order_count"`
	IncomeCents    int64  `json:"income_cents"`
}

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
	rows, e := s.db.Pool.Query(r.Context(), `
WITH cash AS (
 SELECT i.user_id,i.amount_cents,i.paid_at received_at FROM invoices i WHERE (i.status='paid' OR (i.status='refunded' AND EXISTS(SELECT 1 FROM external_refunds er WHERE er.invoice_id=i.id AND er.status='confirmed'))) AND i.invoice_kind<>'topup' AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments c WHERE c.invoice_id=i.id)
 UNION ALL SELECT r.user_id,r.amount_cents,r.received_at FROM credit_repayments r
), customer_orders AS (
 SELECT user_id,count(*)::bigint AS order_count FROM orders
 WHERE created_at >= $1 AND created_at < $2 AND admin_customer_allowed($3,user_id) GROUP BY user_id
), customer_income AS (
 SELECT user_id,sum(amount_cents)::bigint AS income_cents FROM cash
 WHERE received_at >= $1 AND received_at < $2 AND admin_customer_allowed($3,user_id) GROUP BY user_id
)
SELECT coalesce(u.sales_owner_id,0) AS sales_owner_id,
       coalesce(o.display_name,'未分配') AS sales_owner_name,
       coalesce(sum(c.order_count),0)::bigint AS order_count,
       coalesce(sum(i.income_cents),0)::bigint AS income_cents
FROM customer_orders c FULL JOIN customer_income i ON i.user_id=c.user_id
JOIN users u ON u.id=coalesce(c.user_id,i.user_id)
LEFT JOIN users o ON o.id=u.sales_owner_id
GROUP BY 1,2 ORDER BY income_cents DESC,sales_owner_id`, p.Start, p.End, identityFrom(r).ID)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	items, e := pgx.CollectRows(rows, pgx.RowToStructByName[salesReportItem])
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if items == nil {
		items = []salesReportItem{}
	}
	write(w, 200, map[string]any{"year": p.Year, "items": items})
}
func (s *Server) ticketReport(w http.ResponseWriter, r *http.Request) {
	p, e := parseReportPeriod(r, true)
	if e != nil {
		s.reportInputFailure(w, r, e)
		return
	}
	rows, e := s.db.Pool.Query(r.Context(), `SELECT to_char(d.day,'YYYY-MM-DD') AS date,count(t.id)::bigint AS ticket_count,count(t.id) FILTER(WHERE t.status IN ('open','pending'))::bigint AS open_count FROM generate_series(($1::timestamptz AT TIME ZONE 'Asia/Shanghai')::date,(($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date-interval '1 day')::date,interval '1 day') d(day) LEFT JOIN tickets t ON (t.created_at AT TIME ZONE 'Asia/Shanghai')::date=d.day AND t.created_at >= $1 AND t.created_at < $2 AND t.merged_ticket_id IS NULL AND admin_ticket_department_allowed($3,t.department_id) WHERE $4::boolean GROUP BY d.day ORDER BY d.day`, p.Start, p.End, identityFrom(r).ID, identityFrom(r).can("tickets.view"))
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
	write(w, 200, map[string]any{"year": p.Year, "month": p.Month, "items": items})
}
