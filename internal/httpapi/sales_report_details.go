package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) registerSalesReportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/reports/sales/details", s.auth(s.salesReportDetails, true))
	mux.HandleFunc("GET /api/v1/admin/reports/sales/options", s.auth(s.salesReportOptions, true))
}

func salesReportRoutePermissions(m map[string]string) {
	m["GET /api/v1/admin/reports/sales/details"] = "reports.view"
	m["GET /api/v1/admin/reports/sales/options"] = "reports.view"
}

func (s *Server) salesReportOptions(w http.ResponseWriter, r *http.Request) {
	// Staff names come from visible customers, never the unrestricted staff directory.
	rows, err := s.db.Pool.Query(r.Context(), `SELECT DISTINCT coalesce(c.sales_owner_id,0) id,coalesce(u.display_name,'未分配') name
 FROM users c LEFT JOIN users u ON u.id=c.sales_owner_id
 WHERE c.role='customer' AND admin_customer_allowed($1,c.id) ORDER BY 2,1`, identityFrom(r).ID)
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
	write(w, 200, map[string]any{"staff": items, "scope": "current_customer_scope", "commission": "estimate_and_confirmation_separate"})
}

type salesDetailFilter = store.SalesReportDetailFilter

func parseSalesDetailFilter(r *http.Request) (salesDetailFilter, error) {
	p, e := parseReportPeriod(r, true)
	if e != nil {
		return salesDetailFilter{}, e
	}
	f := salesDetailFilter{Start: p.Start, End: p.End}
	f.Limit, f.Offset = pagination(r)
	q := r.URL.Query()
	for _, field := range []struct {
		name string
		dst  *string
	}{{"q", &f.Q}, {"customer", &f.CustomerName}, {"product", &f.ProductName}, {"invoice", &f.Invoice}} {
		*field.dst = strings.TrimSpace(q.Get(field.name))
		if len(*field.dst) > 120 {
			return f, &reportInputError{"搜索条件过长"}
		}
	}
	for _, field := range []struct {
		name string
		dst  **int64
		zero bool
	}{{"sales_owner_id", &f.Owner, true}, {"customer_id", &f.Customer, false}, {"product_id", &f.Product, false}} {
		if raw := q.Get(field.name); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 0 || (!field.zero && n == 0) {
				return f, &reportInputError{"报表编号筛选无效"}
			}
			*field.dst = &n
		}
	}
	f.Kind = q.Get("kind")
	switch f.Kind {
	case "", "purchase", "renew", "upgrade", "other":
	default:
		return f, &reportInputError{"业务类型无效"}
	}
	f.Condition = q.Get("condition")
	switch f.Condition {
	case "", "configured", "unconfigured", "confirmed", "reversed", "unconfirmed", "credit":
	default:
		return f, &reportInputError{"提成条件无效"}
	}
	zone := time.FixedZone("Asia/Shanghai", 28800)
	if preset := q.Get("preset"); preset != "" {
		now := time.Now().In(zone)
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone)
		switch preset {
		case "today":
			f.Start = today
			f.End = today.AddDate(0, 0, 1)
		case "week":
			f.Start = today.AddDate(0, 0, -(int(today.Weekday())+6)%7)
			f.End = today.AddDate(0, 0, 1)
		case "month":
			f.Start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, zone)
			f.End = f.Start.AddDate(0, 1, 0)
		case "last_month":
			f.End = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, zone)
			f.Start = f.End.AddDate(0, -1, 0)
		case "three_months":
			f.End = today.AddDate(0, 0, 1)
			f.Start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, zone).AddDate(0, -2, 0)
		case "all":
			f.Start = time.Date(2000, 1, 1, 0, 0, 0, 0, zone)
			f.End = today.AddDate(0, 0, 1)
		default:
			return f, &reportInputError{"时间条件无效"}
		}
	}
	if raw := q.Get("from"); raw != "" {
		f.Start, e = time.ParseInLocation("2006-01-02", raw, zone)
		if e != nil {
			return f, &reportInputError{"开始日期无效"}
		}
	}
	if raw := q.Get("to"); raw != "" {
		f.End, e = time.ParseInLocation("2006-01-02", raw, zone)
		if e != nil {
			return f, &reportInputError{"结束日期无效"}
		}
		f.End = f.End.AddDate(0, 0, 1)
	}
	if !f.Start.Before(f.End) || f.Start.Year() < 2000 || f.End.After(time.Date(time.Now().In(zone).Year()+2, 1, 1, 0, 0, 0, 0, zone)) {
		return f, &reportInputError{"查询日期范围无效"}
	}
	return f, nil
}

func (s *Server) salesReportDetails(w http.ResponseWriter, r *http.Request) {
	f, e := parseSalesDetailFilter(r)
	if s.reportInputFailure(w, r, e) {
		return
	}
	if !reportJSONFormat(w, r) {
		return
	}
	raw, err := s.db.SalesReportDetails(r.Context(), identityFrom(r).ID, f, time.Now())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, raw)
}
