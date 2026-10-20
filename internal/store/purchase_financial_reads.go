package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type purchaseFinancialQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type ProductIncomeRow struct {
	ProductID           int64  `json:"product_id"`
	Name                string `json:"name"`
	NewOrderAmountCents int64  `json:"new_order_amount_cents"`
	NewOrderCount       int64  `json:"new_order_count"`
	RenewalAmountCents  int64  `json:"renewal_amount_cents"`
	RenewalOrderCount   int64  `json:"renewal_order_count"`
	TotalAmountCents    int64  `json:"total_amount_cents"`
	NewQuantity         int64  `json:"new_quantity"`
	RenewalQuantity     int64  `json:"renewal_quantity"`
}

func productIncomeRowsFrom(ctx context.Context, db purchaseFinancialQuerier, start, end time.Time, actor int64) ([]ProductIncomeRow, error) {
	// Complete line pricing is applied before any downstream product/category filter.
	rows, err := db.Query(ctx, `
WITH settled_orders AS (
  SELECT o.id,i.id invoice_id,i.amount_cents
  FROM orders o JOIN invoices i ON i.order_id=o.id
  WHERE (i.status='paid' OR (i.status='refunded' AND EXISTS(SELECT 1 FROM external_refunds er WHERE er.invoice_id=i.id AND er.status='confirmed'))) AND i.invoice_kind<>'topup' AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments c WHERE c.invoice_id=i.id) AND i.paid_at >= $1 AND i.paid_at < $2 AND admin_customer_allowed($3,i.user_id)
), new_sales AS (
  SELECT oi.product_id,max(oi.product_name) AS name,
         sum(purchase_invoice_item_net(o.invoice_id,oi.id))::bigint AS amount,
         count(DISTINCT oi.order_id)::bigint AS order_count, sum(oi.quantity)::bigint AS quantity
  FROM order_items oi JOIN settled_orders o ON o.id=oi.order_id
  GROUP BY oi.product_id
), renewals AS (
  SELECT s.product_id, max(p.name) AS name, sum(ii.amount_cents)::bigint AS amount,
         count(DISTINCT i.id)::bigint AS order_count,count(DISTINCT ii.id)::bigint AS quantity
  FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id JOIN services s ON s.id=ii.service_id
       JOIN products p ON p.id=s.product_id
  WHERE ii.item_type='renew' AND (i.status='paid' OR (i.status='refunded' AND EXISTS(SELECT 1 FROM external_refunds er WHERE er.invoice_id=i.id AND er.status='confirmed'))) AND i.invoice_kind<>'topup' AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments c WHERE c.invoice_id=i.id) AND i.paid_at >= $1 AND i.paid_at < $2 AND admin_customer_allowed($3,i.user_id)
  GROUP BY s.product_id
), credit_order_invoices AS (
 SELECT i.id,i.order_id,i.user_id,i.amount_cents
 FROM invoices i JOIN orders o ON o.id=i.order_id AND o.user_id=i.user_id
 WHERE i.invoice_kind='automatic' AND i.status IN ('paid','refunded') AND i.paid_at IS NOT NULL
 AND admin_customer_allowed($3,i.user_id) AND EXISTS(SELECT 1 FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id)
), credit_order_weights AS (
 SELECT i.id invoice_id,i.order_id,i.amount_cents,oi.id item_id,oi.product_id,oi.product_name,oi.quantity,
 coalesce(p.net_cents,oi.total_cents)::numeric weight,
 sum(coalesce(p.net_cents,oi.total_cents)) OVER(PARTITION BY i.id ORDER BY oi.id) cumulative,
 sum(coalesce(p.net_cents,oi.total_cents)) OVER(PARTITION BY i.id) weight_total,
 bool_or(p.order_item_id IS NOT NULL) OVER(PARTITION BY i.id) frozen
 FROM credit_order_invoices i JOIN order_items oi ON oi.order_id=i.order_id
 LEFT JOIN order_item_pricing p ON p.order_item_id=oi.id
), credit_order_receipts AS (
 SELECT cr.invoice_id,cr.amount_cents,cr.received_at,
 sum(cr.amount_cents) OVER(PARTITION BY cr.invoice_id ORDER BY cr.received_at,cr.id ROWS UNBOUNDED PRECEDING) received_total,
 sum(cr.amount_cents) OVER(PARTITION BY cr.invoice_id ORDER BY cr.received_at,cr.id ROWS UNBOUNDED PRECEDING)-cr.amount_cents prior_received
 FROM credit_repayments cr JOIN credit_order_invoices i ON i.id=cr.invoice_id AND i.user_id=cr.user_id
), credit_order_lines AS (
 SELECT w.order_id,w.item_id,w.product_id,w.product_name,w.quantity,r.received_at,
 CASE WHEN w.frozen THEN coalesce(
 div(least(r.received_total,w.amount_cents::numeric)*w.cumulative,nullif(w.weight_total,0))-div(least(r.received_total,w.amount_cents::numeric)*(w.cumulative-w.weight),nullif(w.weight_total,0))
 -div(least(r.prior_received,w.amount_cents::numeric)*w.cumulative,nullif(w.weight_total,0))+div(least(r.prior_received,w.amount_cents::numeric)*(w.cumulative-w.weight),nullif(w.weight_total,0)),0)::bigint
 ELSE coalesce(div(least(r.amount_cents::numeric,w.weight_total)*w.cumulative,nullif(w.weight_total,0))-div(least(r.amount_cents::numeric,w.weight_total)*(w.cumulative-w.weight),nullif(w.weight_total,0)),0)::bigint END amount
 FROM credit_order_weights w JOIN credit_order_receipts r ON r.invoice_id=w.invoice_id
), selected_credit_order_lines AS (
 SELECT order_id,item_id,product_id,max(product_name) product_name,max(quantity) quantity,sum(amount)::bigint amount
 FROM credit_order_lines WHERE received_at >= $1 AND received_at < $2 GROUP BY order_id,item_id,product_id
), credit_products AS (
 SELECT r.invoice_id,r.amount_cents,ii.id item_id,ii.item_type,ii.service_id,s.product_id,p.name
 FROM credit_repayments r JOIN invoices i ON i.id=r.invoice_id
 JOIN invoice_items ii ON ii.invoice_id=i.id JOIN services s ON s.id=ii.service_id AND s.user_id=r.user_id JOIN products p ON p.id=s.product_id
 WHERE r.received_at >= $1 AND r.received_at < $2 AND admin_customer_allowed($3,r.user_id)
 AND i.invoice_kind='automatic' AND i.order_id IS NULL AND ii.item_type IN ('service','renew')
 AND (SELECT count(*) FROM invoice_items x WHERE x.invoice_id=i.id)=1
 ), totals AS (
 SELECT product_id,name,amount new_amount,order_count new_count,0::bigint renew_amount,0::bigint renew_count,quantity new_quantity,0::bigint renew_quantity FROM new_sales
 UNION ALL SELECT product_id,name,0,0,amount,order_count,0,quantity FROM renewals
 UNION ALL SELECT product_id,max(product_name),sum(amount)::bigint,count(DISTINCT order_id)::bigint,0,0,sum(quantity)::bigint,0 FROM selected_credit_order_lines GROUP BY product_id
 UNION ALL SELECT product_id,max(name),coalesce(sum(amount_cents) FILTER(WHERE item_type='service'),0)::bigint,count(DISTINCT invoice_id) FILTER(WHERE item_type='service')::bigint,coalesce(sum(amount_cents) FILTER(WHERE item_type='renew'),0)::bigint,count(DISTINCT invoice_id) FILTER(WHERE item_type='renew')::bigint,count(DISTINCT service_id) FILTER(WHERE item_type='service')::bigint,count(DISTINCT item_id) FILTER(WHERE item_type='renew')::bigint FROM credit_products GROUP BY product_id
 ) SELECT product_id,max(name),sum(new_amount)::bigint,sum(new_count)::bigint,sum(renew_amount)::bigint,sum(renew_count)::bigint,(sum(new_amount)+sum(renew_amount))::bigint,sum(new_quantity)::bigint,sum(renew_quantity)::bigint FROM totals GROUP BY product_id
ORDER BY 7 DESC,1`, start, end, actor)
	if err != nil {
		return nil, err
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ProductIncomeRow])
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = []ProductIncomeRow{}
	}
	return result, nil
}

type ProductIncomeCategory struct {
	ProductID  int64
	CategoryID int64
	Name       string
	Sort       int
}

func (s *Store) ProductIncomeRows(ctx context.Context, start, end time.Time, actor int64) ([]ProductIncomeRow, error) {
	return productIncomeRowsFrom(ctx, s.Pool, start, end, actor)
}

// ProductIncomeCategoryData reads both report amounts and their category labels
// under one snapshot. HTTP filters remain presentation-only after allocation.
func (s *Store) ProductIncomeCategoryData(ctx context.Context, start, end time.Time, actor int64) ([]ProductIncomeRow, []ProductIncomeCategory, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	items, err := productIncomeRowsFrom(ctx, tx, start, end, actor)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ProductID)
	}
	rows, err := tx.Query(ctx, `SELECT p.id,coalesce(c.id,0) category_id,coalesce(c.name,'未分类') category_name,coalesce(c.sort_order,0) sort_order FROM products p LEFT JOIN product_categories c ON c.id=p.category_id WHERE p.id=ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, nil, err
	}
	metadata, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ProductIncomeCategory])
	if err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return items, metadata, nil
}

type SalesReportDetailFilter struct {
	Start, End                                             time.Time
	Owner, Customer, Product                               *int64
	Q, CustomerName, ProductName, Kind, Invoice, Condition string
	Limit, Offset                                          int
}

// All receipt amounts use the same current-customer scope. The tier turnover
// deliberately uses visible purchase receipts, before optional search filters;
// it is a labelled estimate, never a replacement for a confirmation snapshot.
const salesReceiptFactsCTE = `WITH invoices_scope AS (
 SELECT i.*,c.display_name customer_name,c.sales_owner_id,u.display_name sales_owner_name,coalesce(o.status IN ('cancelled','refunded'),false) order_invalid,
 g.id rule_group_id,g.name rule_group_name,g.version rule_version,g.purchase_rate_bps,g.renewal_rate_bps,g.upgrade_rate_bps,g.include_renewal,g.include_upgrade
 FROM invoices i JOIN account_identities c ON c.id=i.user_id LEFT JOIN administrators u ON u.id=c.sales_owner_id LEFT JOIN orders o ON o.id=i.order_id AND o.user_id=i.user_id
 LEFT JOIN sales_commission_groups g ON g.id=u.sales_commission_group_id AND g.active AND u.sales_enabled AND u.status='active'
 WHERE i.invoice_kind<>'topup' AND i.status IN ('paid','refunded') AND i.paid_at IS NOT NULL AND admin_customer_allowed($1,i.user_id)
), raw_lines AS (
 SELECT i.id invoice_id,'order:'||oi.id line_key,oi.id line_id,oi.product_id,oi.product_name,'purchase'::text kind,coalesce(pricing.net_cents,oi.total_cents) gross
 FROM invoices_scope i JOIN orders o ON o.id=i.order_id AND o.user_id=i.user_id JOIN order_items oi ON oi.order_id=o.id
 LEFT JOIN order_item_pricing pricing ON pricing.order_item_id=oi.id
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
 SELECT i.id invoice_id,i.amount_cents,i.paid_at received_at,false credit,i.amount_cents::numeric cumulative_received,0::numeric prior_received FROM invoices_scope i WHERE NOT EXISTS(SELECT 1 FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id)
 UNION ALL SELECT cr.invoice_id,cr.amount_cents,cr.received_at,true,
 sum(cr.amount_cents) OVER(PARTITION BY cr.invoice_id ORDER BY cr.received_at,cr.id ROWS UNBOUNDED PRECEDING),
 sum(cr.amount_cents) OVER(PARTITION BY cr.invoice_id ORDER BY cr.received_at,cr.id ROWS UNBOUNDED PRECEDING)-cr.amount_cents
 FROM credit_repayments cr JOIN invoices_scope i ON i.id=cr.invoice_id AND i.user_id=cr.user_id
), amounts AS (
 SELECT i.*,w.line_key,w.line_id,w.product_id,w.product_name,w.kind,w.gross,w.cumulative,w.gross_total,r.received_at,r.credit,
 CASE WHEN i.order_id IS NOT NULL AND w.kind='purchase' AND NOT r.credit THEN purchase_invoice_item_net(i.id,w.line_id)
 WHEN i.order_id IS NOT NULL AND w.kind='purchase' AND r.credit AND EXISTS(SELECT 1 FROM order_item_pricing pricing JOIN order_items oi ON oi.id=pricing.order_item_id WHERE oi.order_id=i.order_id)
 THEN coalesce(div(least(r.cumulative_received,i.amount_cents::numeric)*w.cumulative,nullif(w.gross_total,0))-div(least(r.cumulative_received,i.amount_cents::numeric)*(w.cumulative-w.gross),nullif(w.gross_total,0))
 -div(least(r.prior_received,i.amount_cents::numeric)*w.cumulative,nullif(w.gross_total,0))+div(least(r.prior_received,i.amount_cents::numeric)*(w.cumulative-w.gross),nullif(w.gross_total,0)),0)::bigint
 ELSE coalesce(div(least(r.amount_cents::numeric,w.gross_total)*w.cumulative,nullif(w.gross_total,0))-div(least(r.amount_cents::numeric,w.gross_total)*(w.cumulative-w.gross),nullif(w.gross_total,0)),0)::bigint END amount
 FROM invoices_scope i JOIN weighted w ON w.invoice_id=i.id JOIN receipts r ON r.invoice_id=i.id
)`

// Detailed reports extend the same line-level receipt facts with estimates,
// filtering and presentation; dashboard rankings consume only the facts.
const salesDetailCTE = salesReceiptFactsCTE + `, turnover AS (
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

func (s *Store) SalesReportDetails(ctx context.Context, actor int64, f SalesReportDetailFilter, now time.Time) (json.RawMessage, error) {
	unit := "day"
	if f.End.Sub(f.Start) > 93*24*time.Hour {
		unit = "month"
	}
	args := []any{actor, f.Owner, f.Q, f.CustomerName, f.ProductName, f.Kind, f.Invoice, f.Customer, f.Product, f.Condition, f.Start, f.End, f.Limit, f.Offset, now}
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
	if err := s.Pool.QueryRow(ctx, query, args...).Scan(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}
