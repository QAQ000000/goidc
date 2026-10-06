package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type reportPeriod struct {
	Year  int
	Month int
	Start time.Time
	End   time.Time
	Days  int
}

func parseReportPeriod(r *http.Request, monthRequired bool) (reportPeriod, error) {
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	year := now.Year()
	if value := r.URL.Query().Get("year"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 2000 || parsed > now.Year()+1 {
			return reportPeriod{}, &reportInputError{"报表年份无效"}
		}
		year = parsed
	}
	month := 0
	if value := r.URL.Query().Get("month"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 12 {
			return reportPeriod{}, &reportInputError{"报表月份无效"}
		}
		month = parsed
	} else if monthRequired {
		month = int(now.Month())
	}
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	start := time.Date(year, 1, 1, 0, 0, 0, 0, location)
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, location)
	days := 0
	if month > 0 {
		start = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, location)
		end = start.AddDate(0, 1, 0)
		days = end.Add(-time.Nanosecond).Day()
	}
	return reportPeriod{Year: year, Month: month, Start: start, End: end, Days: days}, nil
}

type reportInputError struct{ message string }

func (e *reportInputError) Error() string { return e.message }

func (s *Server) reportInputFailure(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if input, ok := err.(*reportInputError); ok {
		writeError(w, http.StatusBadRequest, "invalid_report_period", input.message)
	} else {
		s.fail(w, r, err)
	}
	return true
}

type incomeMonth struct {
	Month        string `json:"month"`
	IncomeCents  int64  `json:"income_cents"`
	InvoiceCount int64  `json:"invoice_count"`
}

func (s *Server) incomeReport(w http.ResponseWriter, r *http.Request) {
	period, err := parseReportPeriod(r, false)
	if s.reportInputFailure(w, r, err) {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `
SELECT to_char(date_trunc('month', paid_at AT TIME ZONE 'Asia/Shanghai'), 'YYYY-MM'),
       coalesce(sum(amount_cents),0)::bigint, count(*)::bigint
FROM invoices
WHERE status='paid' AND paid_at >= $1 AND paid_at < $2 AND admin_customer_allowed($3,user_id)
GROUP BY 1 ORDER BY 1 DESC`, period.Start, period.End, identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	months, err := pgx.CollectRows(rows, pgx.RowToStructByPos[incomeMonth])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if months == nil {
		months = []incomeMonth{}
	}
	var income int64
	var invoiceCount int64
	for _, item := range months {
		income += item.IncomeCents
		invoiceCount += item.InvoiceCount
	}
	write(w, http.StatusOK, map[string]any{
		"year":   period.Year,
		"months": months,
		"total":  map[string]any{"income_cents": income, "invoice_count": invoiceCount},
	})
}

type dailyClientReport struct {
	Date                 string `json:"date"`
	NewClients           int64  `json:"new_clients"`
	NewOrders            int64  `json:"new_orders"`
	PaidOrders           int64  `json:"paid_orders"`
	NewTickets           int64  `json:"new_tickets"`
	TicketReplies        int64  `json:"ticket_replies"`
	CancellationRequests int64  `json:"cancellation_requests"`
}

func (s *Server) newClientsReport(w http.ResponseWriter, r *http.Request) {
	period, err := parseReportPeriod(r, true)
	if s.reportInputFailure(w, r, err) {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `
WITH days AS (
  SELECT generate_series($1::date, ($2::date - interval '1 day')::date, interval '1 day')::date AS day
), clients AS (
  SELECT (created_at AT TIME ZONE 'Asia/Shanghai')::date AS day, count(*)::bigint AS count
  FROM users WHERE role='customer' AND status='active' AND created_at >= $1 AND created_at < $2 AND admin_customer_allowed($3,id) GROUP BY 1
), order_totals AS (
  SELECT (created_at AT TIME ZONE 'Asia/Shanghai')::date AS day,
         count(*)::bigint AS new_orders,
         count(*) FILTER (WHERE status IN ('paid','active'))::bigint AS paid_orders
  FROM orders WHERE created_at >= $1 AND created_at < $2 AND admin_customer_allowed($3,user_id) GROUP BY 1
), ticket_totals AS (
  SELECT (created_at AT TIME ZONE 'Asia/Shanghai')::date AS day, count(*)::bigint AS count
  FROM tickets WHERE created_at >= $1 AND created_at < $2 AND $4::boolean AND admin_ticket_department_allowed($3,department_id) GROUP BY 1
), replies AS (
  SELECT (r.created_at AT TIME ZONE 'Asia/Shanghai')::date AS day, count(*)::bigint AS count
  FROM ticket_replies r JOIN tickets t ON t.id=r.ticket_id WHERE r.created_at >= $1 AND r.created_at < $2 AND $4::boolean AND admin_ticket_department_allowed($3,t.department_id) GROUP BY 1
), cancellations AS (
  SELECT (created_at AT TIME ZONE 'Asia/Shanghai')::date AS day, count(*)::bigint AS count
  FROM service_cancellation_requests WHERE created_at >= $1 AND created_at < $2 AND admin_customer_allowed($3,user_id) GROUP BY 1
)
SELECT to_char(days.day,'YYYY-MM-DD'), coalesce(clients.count,0), coalesce(order_totals.new_orders,0),
       coalesce(order_totals.paid_orders,0), coalesce(ticket_totals.count,0), coalesce(replies.count,0), coalesce(cancellations.count,0)
FROM days
LEFT JOIN clients USING(day)
LEFT JOIN order_totals USING(day)
LEFT JOIN ticket_totals USING(day)
LEFT JOIN replies USING(day)
LEFT JOIN cancellations USING(day)
ORDER BY days.day`, period.Start, period.End, identityFrom(r).ID, identityFrom(r).can("tickets.view"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dailyClientReport, error) {
		var item dailyClientReport
		err := row.Scan(&item.Date, &item.NewClients, &item.NewOrders, &item.PaidOrders, &item.NewTickets, &item.TicketReplies, &item.CancellationRequests)
		return item, err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if result == nil {
		result = []dailyClientReport{}
	}
	write(w, http.StatusOK, map[string]any{"year": period.Year, "month": period.Month, "days": result})
}

type customerRanking struct {
	UserID       int64  `json:"user_id"`
	DisplayName  string `json:"display_name"`
	IncomeCents  int64  `json:"income_cents"`
	ExpenseCents int64  `json:"expense_cents"`
	NetCents     int64  `json:"net_cents"`
}

func (s *Server) customerRankingReport(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Pool.Query(r.Context(), `
SELECT u.id,u.display_name,
       coalesce(sum(CASE WHEN l.delta_cents > 0 THEN l.delta_cents ELSE 0 END),0)::bigint AS income_cents,
       coalesce(sum(CASE WHEN l.delta_cents < 0 THEN -l.delta_cents ELSE 0 END),0)::bigint AS expense_cents,
       coalesce(sum(l.delta_cents),0)::bigint AS net_cents
FROM users u LEFT JOIN balance_ledger l ON l.user_id=u.id
WHERE u.role='customer' AND u.status='active' AND admin_customer_allowed($1,u.id)
GROUP BY u.id,u.display_name
ORDER BY net_cents DESC, u.id
LIMIT 10`, identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByPos[customerRanking])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if result == nil {
		result = []customerRanking{}
	}
	write(w, http.StatusOK, result)
}

type productIncome struct {
	ProductID           int64  `json:"product_id"`
	Name                string `json:"name"`
	NewOrderAmountCents int64  `json:"new_order_amount_cents"`
	NewOrderCount       int64  `json:"new_order_count"`
	RenewalAmountCents  int64  `json:"renewal_amount_cents"`
	RenewalOrderCount   int64  `json:"renewal_order_count"`
	TotalAmountCents    int64  `json:"total_amount_cents"`
}

func (s *Server) productIncomeReport(w http.ResponseWriter, r *http.Request) {
	period, err := parseReportPeriod(r, true)
	if s.reportInputFailure(w, r, err) {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `
WITH new_sales AS (
  SELECT oi.product_id, max(oi.product_name) AS name, sum(oi.total_cents)::bigint AS amount,
         count(DISTINCT o.id)::bigint AS order_count
  FROM order_items oi JOIN orders o ON o.id=oi.order_id JOIN invoices i ON i.order_id=o.id
  WHERE i.status='paid' AND i.paid_at >= $1 AND i.paid_at < $2 AND admin_customer_allowed($3,i.user_id)
  GROUP BY oi.product_id
), renewals AS (
  SELECT s.product_id, max(p.name) AS name, sum(ii.amount_cents)::bigint AS amount,
         count(DISTINCT i.id)::bigint AS order_count
  FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id JOIN services s ON s.id=ii.service_id
       JOIN products p ON p.id=s.product_id
  WHERE ii.item_type='renew' AND i.status='paid' AND i.paid_at >= $1 AND i.paid_at < $2 AND admin_customer_allowed($3,i.user_id)
  GROUP BY s.product_id
)
SELECT coalesce(n.product_id,r.product_id), coalesce(n.name,r.name), coalesce(n.amount,0), coalesce(n.order_count,0),
       coalesce(r.amount,0), coalesce(r.order_count,0), coalesce(n.amount,0)+coalesce(r.amount,0)
FROM new_sales n FULL JOIN renewals r ON r.product_id=n.product_id
ORDER BY 7 DESC,1`, period.Start, period.End, identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByPos[productIncome])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if result == nil {
		result = []productIncome{}
	}
	write(w, http.StatusOK, map[string]any{"year": period.Year, "month": period.Month, "products": result})
}
