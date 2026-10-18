package httpapi

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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

type salesDetailFilter struct {
	Start, End                                             time.Time
	Owner, Customer, Product                               *int64
	Q, CustomerName, ProductName, Kind, Invoice, Condition string
	Limit, Offset                                          int
}

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

// All receipt amounts use the same current-customer scope. The tier turnover
// deliberately uses visible purchase receipts, before optional search filters;
// it is a labelled estimate, never a replacement for a confirmation snapshot.
const salesDetailCTE = `WITH invoices_scope AS (
 SELECT i.*,c.display_name customer_name,c.sales_owner_id,u.display_name sales_owner_name,coalesce(o.status IN ('cancelled','refunded'),false) order_invalid,
 g.id rule_group_id,g.name rule_group_name,g.version rule_version,g.purchase_rate_bps,g.renewal_rate_bps,g.upgrade_rate_bps,g.include_renewal,g.include_upgrade
 FROM invoices i JOIN users c ON c.id=i.user_id LEFT JOIN users u ON u.id=c.sales_owner_id LEFT JOIN orders o ON o.id=i.order_id AND o.user_id=i.user_id
 LEFT JOIN sales_commission_groups g ON g.id=u.sales_commission_group_id AND g.active AND u.sales_enabled AND u.status='active'
 WHERE i.invoice_kind<>'topup' AND i.status IN ('paid','refunded') AND i.paid_at IS NOT NULL AND admin_customer_allowed($1,i.user_id)
), raw_lines AS (
 SELECT i.id invoice_id,'order:'||oi.id line_key,oi.id line_id,oi.product_id,oi.product_name,'purchase'::text kind,oi.total_cents gross
 FROM invoices_scope i JOIN orders o ON o.id=i.order_id AND o.user_id=i.user_id JOIN order_items oi ON oi.order_id=o.id
 UNION ALL
 SELECT i.id,'item:'||ii.id,ii.id,coalesce(s.product_id,0),coalesce(p.name,ii.description),
 CASE ii.item_type WHEN 'service' THEN 'purchase' WHEN 'renew' THEN 'renew' WHEN 'upgrade' THEN 'upgrade' ELSE 'other' END,greatest(ii.amount_cents,0)
 FROM invoices_scope i JOIN invoice_items ii ON ii.invoice_id=i.id LEFT JOIN services s ON s.id=ii.service_id AND s.user_id=i.user_id LEFT JOIN products p ON p.id=s.product_id
 WHERE i.order_id IS NULL
 UNION ALL
 SELECT i.id,'unattributed:'||i.id,i.id,0,'未归属业务账单','other',i.amount_cents FROM invoices_scope i
 WHERE i.order_id IS NULL AND NOT EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id)
 OR i.order_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM order_items oi WHERE oi.order_id=i.order_id)
), source_lines AS (
 SELECT l.* FROM raw_lines l JOIN invoices_scope i ON i.id=l.invoice_id
 WHERE i.amount_cents=0 OR EXISTS(SELECT 1 FROM raw_lines p WHERE p.invoice_id=l.invoice_id AND p.gross>0)
 UNION ALL
 SELECT i.id,'unattributed:'||i.id,i.id,0,'未归属业务账单','other',i.amount_cents FROM invoices_scope i
 WHERE i.amount_cents>0 AND NOT EXISTS(SELECT 1 FROM raw_lines p WHERE p.invoice_id=i.id AND p.gross>0)
), weighted AS (
 SELECT l.*,sum(gross) OVER(PARTITION BY invoice_id ORDER BY line_id) cumulative,sum(gross) OVER(PARTITION BY invoice_id) gross_total FROM source_lines l
), receipts AS (
 SELECT i.id invoice_id,i.amount_cents,i.paid_at received_at,false credit FROM invoices_scope i WHERE NOT EXISTS(SELECT 1 FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id)
 UNION ALL SELECT cr.invoice_id,cr.amount_cents,cr.received_at,true FROM credit_repayments cr JOIN invoices_scope i ON i.id=cr.invoice_id AND i.user_id=cr.user_id
), amounts AS (
 SELECT i.*,w.line_key,w.line_id,w.product_id,w.product_name,w.kind,w.gross,w.cumulative,w.gross_total,r.received_at,r.credit,
 coalesce(div(least(r.amount_cents::numeric,w.gross_total)*w.cumulative,nullif(w.gross_total,0))-div(least(r.amount_cents::numeric,w.gross_total)*(w.cumulative-w.gross),nullif(w.gross_total,0)),0)::bigint amount
 FROM invoices_scope i JOIN weighted w ON w.invoice_id=i.id JOIN receipts r ON r.invoice_id=i.id
), turnover AS (
 SELECT sales_owner_id,to_char(received_at AT TIME ZONE 'Asia/Shanghai','YYYY-MM') month_key,sum(amount) turnover_cents FROM amounts WHERE kind='purchase' AND status='paid' AND NOT order_invalid GROUP BY 1,2
), priced AS (
 SELECT a.*,coalesce(t.extra_rate_bps,0) tier_extra_rate_bps,t.id tier_id,t.version tier_version,
 CASE WHEN a.rule_group_id IS NULL OR a.kind='other' OR a.product_id=0 THEN NULL
 WHEN a.status<>'paid' OR a.order_invalid THEN 0
 WHEN a.kind='renew' AND NOT a.include_renewal OR a.kind='upgrade' AND NOT a.include_upgrade THEN 0
 ELSE least(10000,CASE a.kind WHEN 'purchase' THEN a.purchase_rate_bps WHEN 'renew' THEN a.renewal_rate_bps WHEN 'upgrade' THEN a.upgrade_rate_bps END+coalesce(t.extra_rate_bps,0)) END effective_rate_bps,
 si.status confirmation_status,si.commission_cents confirmed_original_cents,si.sales_owner_id confirmed_sales_owner_id,si.snapshot-'monthly_turnover_cents' confirmation_snapshot,
 CASE WHEN si.id IS NOT NULL THEN coalesce(div(si.commission_cents::numeric*a.cumulative,nullif(a.gross_total,0))-div(si.commission_cents::numeric*(a.cumulative-a.gross),nullif(a.gross_total,0)),0)::bigint END confirmed_line_original_cents
 FROM amounts a LEFT JOIN turnover v ON v.sales_owner_id=a.sales_owner_id AND v.month_key=to_char(a.received_at AT TIME ZONE 'Asia/Shanghai','YYYY-MM')
 LEFT JOIN LATERAL(SELECT id,version,extra_rate_bps FROM sales_commission_tiers WHERE active AND threshold_cents<=v.turnover_cents ORDER BY threshold_cents DESC,id DESC LIMIT 1)t ON true
 LEFT JOIN sales_settlement_items si ON si.invoice_id=a.id
), events AS (
 SELECT *,round(amount::numeric*effective_rate_bps/10000)::bigint estimated_commission_cents FROM priced
 WHERE ($2::bigint IS NULL OR coalesce(sales_owner_id,0)=$2)
 AND ($3='' OR customer_name ILIKE '%'||$3||'%' OR product_name ILIKE '%'||$3||'%' OR invoice_no ILIKE '%'||$3||'%')
 AND ($4='' OR customer_name ILIKE '%'||$4||'%') AND ($5='' OR product_name ILIKE '%'||$5||'%') AND ($6='' OR kind=$6) AND ($7='' OR invoice_no ILIKE '%'||$7||'%')
 AND ($8::bigint IS NULL OR user_id=$8) AND ($9::bigint IS NULL OR product_id=$9)
 AND ($10='' OR $10='configured' AND effective_rate_bps IS NOT NULL OR $10='unconfigured' AND effective_rate_bps IS NULL OR $10='confirmed' AND confirmation_status='confirmed' OR $10='reversed' AND confirmation_status='reversed' OR $10='unconfirmed' AND confirmation_status IS NULL OR $10='credit' AND credit)
), selected AS (SELECT * FROM events WHERE received_at >=$11 AND received_at<$12), line_rows AS (
 SELECT id invoice_id,line_key,min(line_id) line_id,product_id,max(product_name) product_name,kind,sum(amount)::bigint amount_cents,sum(estimated_commission_cents)::bigint estimated_commission_cents,
 max(rule_group_id) rule_group_id,max(rule_group_name) rule_group_name,max(rule_version) rule_version,max(effective_rate_bps) effective_rate_bps,max(tier_extra_rate_bps) tier_extra_rate_bps,
 coalesce(jsonb_agg(DISTINCT jsonb_build_object('group_id',rule_group_id,'group_name',rule_group_name,'version',rule_version,'rate_bps',effective_rate_bps,'tier_id',tier_id,'tier_version',tier_version,'extra_rate_bps',tier_extra_rate_bps)), '[]') rules,
 max(received_at) received_at,min(received_at) first_received_at,bool_or(credit) credit,
 max(confirmed_line_original_cents) confirmed_original_cents,max(CASE WHEN confirmation_status='confirmed' THEN confirmed_line_original_cents WHEN confirmation_status='reversed' THEN 0 END) confirmed_commission_cents
 FROM selected GROUP BY id,line_key,product_id,kind
), invoice_rows AS (
 SELECT i.id invoice_id,i.invoice_no,i.user_id customer_id,i.customer_name,coalesce(i.sales_owner_id,0) sales_owner_id,coalesce(i.sales_owner_name,'未分配') sales_owner_name,i.paid_at,
 max(l.received_at) received_at,sum(l.amount_cents)::bigint amount_cents,sum(l.estimated_commission_cents)::bigint estimated_commission_cents,
 count(*) FILTER(WHERE l.estimated_commission_cents IS NULL) unconfigured_lines,
 si.commission_cents confirmed_invoice_original_cents,sum(l.confirmed_original_cents)::bigint confirmed_original_cents,sum(l.confirmed_commission_cents)::bigint confirmed_commission_cents,
 coalesce(si.status,'unconfirmed') confirmation_status,si.sales_owner_id confirmed_sales_owner_id,si.snapshot-'monthly_turnover_cents' confirmation_snapshot,
 jsonb_agg(to_jsonb(l)-'invoice_id' ORDER BY l.line_key) items
 FROM line_rows l JOIN invoices_scope i ON i.id=l.invoice_id LEFT JOIN sales_settlement_items si ON si.invoice_id=i.id GROUP BY i.id,i.invoice_no,i.user_id,i.customer_name,i.sales_owner_id,i.sales_owner_name,i.paid_at,si.id
), ranges AS (
 SELECT 'selected' name,$11::timestamptz start_at,$12::timestamptz end_at
 UNION ALL SELECT 'comparison',$11::timestamptz-($12::timestamptz-$11::timestamptz),$11
 UNION ALL SELECT 'today',date_trunc('day',$15::timestamptz AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai',(date_trunc('day',$15::timestamptz AT TIME ZONE 'Asia/Shanghai')+interval '1 day') AT TIME ZONE 'Asia/Shanghai'
 UNION ALL SELECT 'week',date_trunc('week',$15::timestamptz AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai',(date_trunc('day',$15::timestamptz AT TIME ZONE 'Asia/Shanghai')+interval '1 day') AT TIME ZONE 'Asia/Shanghai'
 UNION ALL SELECT 'month',date_trunc('month',$15::timestamptz AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai',(date_trunc('month',$15::timestamptz AT TIME ZONE 'Asia/Shanghai')+interval '1 month') AT TIME ZONE 'Asia/Shanghai'
 UNION ALL SELECT 'last_month',(date_trunc('month',$15::timestamptz AT TIME ZONE 'Asia/Shanghai')-interval '1 month') AT TIME ZONE 'Asia/Shanghai',date_trunc('month',$15::timestamptz AT TIME ZONE 'Asia/Shanghai') AT TIME ZONE 'Asia/Shanghai'
), range_lines AS (
 SELECT r.name,e.id,e.line_key,sum(e.amount)::bigint amount,sum(e.estimated_commission_cents)::bigint estimate,
 bool_or(e.estimated_commission_cents IS NULL) unconfigured,
 max(CASE WHEN e.confirmation_status='confirmed' THEN e.confirmed_line_original_cents WHEN e.confirmation_status='reversed' THEN 0 END) confirmed
 FROM ranges r JOIN events e ON e.received_at>=r.start_at AND e.received_at<r.end_at GROUP BY r.name,e.id,e.line_key
), range_invoices AS (
 SELECT name,id,sum(amount)::bigint amount,sum(estimate)::bigint estimate,bool_or(unconfigured) unconfigured,sum(confirmed)::bigint confirmed FROM range_lines GROUP BY name,id
), statistics AS (
 SELECT r.name,r.start_at,r.end_at,count(i.id) invoice_count,coalesce(sum(i.amount),0)::bigint amount_cents,coalesce(sum(i.estimate),0)::bigint estimated_commission_cents,
 count(i.id) FILTER(WHERE i.unconfigured) unconfigured_invoices,coalesce(sum(i.confirmed),0)::bigint confirmed_commission_cents
 FROM ranges r LEFT JOIN range_invoices i ON i.name=r.name GROUP BY r.name,r.start_at,r.end_at
)
`

func (s *Server) salesReportDetails(w http.ResponseWriter, r *http.Request) {
	f, e := parseSalesDetailFilter(r)
	if s.reportInputFailure(w, r, e) {
		return
	}
	if format := r.URL.Query().Get("format"); format != "" && format != "json" && format != "csv" {
		writeError(w, 400, "invalid_report_format", "报表格式只支持 JSON 或 CSV")
		return
	}
	now := time.Now()
	unit := "day"
	if f.End.Sub(f.Start) > 93*24*time.Hour {
		unit = "month"
	}
	args := []any{identityFrom(r).ID, f.Owner, f.Q, f.CustomerName, f.ProductName, f.Kind, f.Invoice, f.Customer, f.Product, f.Condition, f.Start, f.End, f.Limit, f.Offset, now}
	query := salesDetailCTE + fmt.Sprintf(`SELECT jsonb_build_object(
 'items',coalesce((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.received_at DESC,p.invoice_id DESC) FROM (SELECT * FROM invoice_rows ORDER BY received_at DESC,invoice_id DESC LIMIT $13 OFFSET $14)p),'[]'::jsonb),
 'total',(SELECT count(*) FROM invoice_rows),'limit',$13::int,'offset',$14::int,
 'statistics',(SELECT jsonb_object_agg(name,to_jsonb(st)-'name') FROM statistics st),
 'trend',coalesce((SELECT jsonb_agg(jsonb_build_object('date',to_char(d.bucket_date,'YYYY-MM-DD'),'amount_cents',coalesce(v.amount,0),'estimated_commission_cents',coalesce(v.estimate,0)) ORDER BY d.bucket_date)
 FROM generate_series(date_trunc('%[1]s',$11::timestamptz AT TIME ZONE 'Asia/Shanghai'),date_trunc('%[1]s',($12::timestamptz-interval '1 second') AT TIME ZONE 'Asia/Shanghai'),interval '1 %[1]s')d(bucket_date)
 LEFT JOIN (SELECT date_trunc('%[1]s',received_at AT TIME ZONE 'Asia/Shanghai') bucket_date,sum(amount)::bigint amount,sum(estimated_commission_cents)::bigint estimate FROM selected GROUP BY 1)v ON v.bucket_date=d.bucket_date),'[]'::jsonb),
 'trend_unit','%[1]s','computed_at',$15::timestamptz,'attribution_basis','current_customer_owner_estimate_and_confirmation_snapshot',
 'turnover_basis','visible_purchase_receipts_by_shanghai_month','confirmation_basis','snapshot_of_selected_invoices_not_new_confirmations',
 'from',to_char($11::timestamptz AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD'),'to',to_char(($12::timestamptz-interval '1 second') AT TIME ZONE 'Asia/Shanghai','YYYY-MM-DD'))`, unit)
	var raw json.RawMessage
	if e = s.db.Pool.QueryRow(r.Context(), query, args...).Scan(&raw); e != nil {
		s.fail(w, r, e)
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		s.writeSalesDetailCSV(w, r, raw)
		return
	}
	write(w, 200, raw)
}

func (s *Server) writeSalesDetailCSV(w http.ResponseWriter, r *http.Request, raw json.RawMessage) {
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if e := d.Decode(&payload); e != nil {
		s.fail(w, r, e)
		return
	}
	var content bytes.Buffer
	content.WriteString("\xef\xbb\xbf")
	writer := csv.NewWriter(&content)
	if e := writer.Write([]string{"客户", "销售", "账单编号", "支付时间", "到账时间", "产品", "类型", "金额（分）", "预计提成（分）", "子项历史有效确认提成（分）", "确认状态", "规则版本"}); e != nil {
		s.fail(w, r, e)
		return
	}
	for _, invoice := range payload.Items {
		for _, value := range invoice["items"].([]any) {
			line := value.(map[string]any)
			estimate := "未配置"
			if line["estimated_commission_cents"] != nil {
				estimate = reportCSVCell(line["estimated_commission_cents"])
			}
			cells := []string{reportCSVCell(invoice["customer_name"]), reportCSVCell(invoice["sales_owner_name"]), reportCSVCell(invoice["invoice_no"]), reportCSVCell(invoice["paid_at"]), reportCSVCell(line["received_at"]), reportCSVCell(line["product_name"]), reportCSVCell(line["kind"]), reportCSVCell(line["amount_cents"]), estimate, reportCSVCell(line["confirmed_commission_cents"]), reportCSVCell(invoice["confirmation_status"]), reportCSVCell(line["rule_version"])}
			if e := writer.Write(cells); e != nil {
				s.fail(w, r, e)
				return
			}
		}
	}
	writer.Flush()
	if e := writer.Error(); e != nil {
		s.fail(w, r, e)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="report-sales-details.csv"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	_, _ = w.Write(content.Bytes())
}
