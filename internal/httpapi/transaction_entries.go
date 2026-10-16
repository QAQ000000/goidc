package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// These are recorded events, not a cash-flow aggregate: a balance payment is an
// expense from the customer's balance, while a gateway payment is a receipt.
// Paid invoices without either record retain their provenance as invoice events.
const transactionEntries = `(
 SELECT 'payment:'||pt.id AS event_key,'payment'::text AS source,pt.id,pt.user_id,i.id AS invoice_id,i.invoice_no,
 coalesce(pt.completed_at,pt.created_at) AS occurred_at,pt.provider AS channel,pt.amount_cents,
 'income'::text AS direction,coalesce(nullif(pt.external_transaction_id,''),pt.provider_payment_id) AS transaction_no,
 '支付成功 · '||i.invoice_no AS description,
 CASE WHEN EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.item_type='renew') THEN 'renew'
 WHEN i.order_id IS NOT NULL THEN 'product' ELSE 'other' END AS kind,NULL::bigint AS balance_after
 FROM payment_transactions pt JOIN invoices i ON i.id=pt.invoice_id AND i.user_id=pt.user_id WHERE pt.status='succeeded'
 UNION ALL
 SELECT 'external-refund:'||r.id,'external_refund',r.id,r.user_id,i.id,i.invoice_no,r.received_at,
 r.provider,r.amount_cents,'expense',r.provider_refund_id,'外部退款到账 · '||i.invoice_no,
 'other',NULL::bigint
 FROM external_refunds r JOIN invoices i ON i.id=r.invoice_id AND i.user_id=r.user_id WHERE r.status='confirmed'
 UNION ALL
 SELECT 'ledger:'||l.id,'ledger',l.id,l.user_id,i.id,i.invoice_no,l.created_at,
 CASE WHEN l.reference_type='admin_topup' THEN 'admin_topup' ELSE 'balance' END,abs(l.delta_cents),
 CASE WHEN l.delta_cents<0 THEN 'expense' WHEN l.delta_cents>0 THEN 'income' ELSE 'neutral' END,
 'L-'||l.id,coalesce(nullif(l.note,''),l.entry_type),
 CASE WHEN l.entry_type='topup' THEN 'topup'
 WHEN l.entry_type='payment' AND EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.item_type='renew') THEN 'renew'
 WHEN l.entry_type='payment' AND i.order_id IS NOT NULL THEN 'product' ELSE 'other' END,l.balance_after
 FROM balance_ledger l LEFT JOIN invoices i ON l.reference_type='invoice' AND i.id=l.reference_id AND i.user_id=l.user_id
 UNION ALL
 SELECT 'invoice:'||i.id,'invoice',i.id,i.user_id,i.id,i.invoice_no,i.paid_at,
 CASE WHEN EXISTS(SELECT 1 FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id) THEN 'credit' ELSE 'unrecorded' END,
 i.amount_cents,'neutral',i.invoice_no,'已付账单 · '||i.invoice_no,
 CASE WHEN EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.item_type='renew') THEN 'renew'
 WHEN i.order_id IS NOT NULL THEN 'product' ELSE 'other' END,NULL::bigint
 FROM invoices i WHERE i.status='paid' AND i.paid_at IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.user_id=i.user_id AND pt.status='succeeded')
 AND NOT EXISTS(SELECT 1 FROM balance_ledger l WHERE l.reference_type='invoice' AND l.reference_id=i.id AND l.user_id=i.user_id AND l.entry_type='payment')
) e JOIN users u ON u.id=e.user_id LEFT JOIN users owner ON owner.id=u.sales_owner_id`

func (s *Server) adminTransactionEntries(w http.ResponseWriter, r *http.Request) {
	from, args, ok := transactionEntriesFilter(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	s.transactionPage(w, r, `e.*,u.display_name AS customer_name,u.username AS customer_username,owner.display_name AS sales_owner_name,'CNY'::text AS currency`, from, "e.occurred_at DESC,e.event_key", args, limit, offset)
}

// The summary deliberately excludes balance movements and credit settlement:
// those are account events and cannot be added to gateway receipts as new cash.
func (s *Server) adminTransactionSummary(w http.ResponseWriter, r *http.Request) {
	from, args, ok := transactionEntriesFilter(w, r)
	if !ok {
		return
	}
	var income, refunds int64
	err := s.db.Pool.QueryRow(r.Context(), `SELECT
 coalesce(sum(e.amount_cents) FILTER (WHERE e.source='payment'),0)::bigint,
 coalesce(sum(e.amount_cents) FILTER (WHERE e.source='external_refund'),0)::bigint`+from, args...).Scan(&income, &refunds)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"gateway_income_cents": income, "external_refund_cents": refunds, "net_gateway_cents": income - refunds, "currency": "CNY"})
}

func transactionEntriesFilter(w http.ResponseWriter, r *http.Request) (string, []any, bool) {
	q := r.URL.Query()
	keyword, kind, direction, channel := strings.TrimSpace(q.Get("q")), q.Get("kind"), q.Get("direction"), strings.TrimSpace(q.Get("channel"))
	invalid := func() { writeError(w, http.StatusBadRequest, "invalid_transaction_filter", "交易流水筛选无效") }
	if len(keyword) > 120 || len(channel) > 100 || (kind != "" && kind != "topup" && kind != "product" && kind != "renew" && kind != "other") || (direction != "" && direction != "income" && direction != "expense" && direction != "neutral") {
		invalid()
		return "", nil, false
	}
	var customerID int64
	if raw := q.Get("customer_id"); raw != "" {
		var err error
		customerID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || customerID < 1 {
			invalid()
			return "", nil, false
		}
	}
	var start, end *time.Time
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	for _, key := range []string{"from", "to"} {
		if raw := q.Get(key); raw != "" {
			v, err := time.ParseInLocation("2006-01-02", raw, zone)
			if err != nil {
				invalid()
				return "", nil, false
			}
			if key == "from" {
				start = &v
			} else {
				v = v.AddDate(0, 0, 1)
				end = &v
			}
		}
	}
	if start != nil && end != nil && !start.Before(*end) {
		invalid()
		return "", nil, false
	}
	var minimum, maximum *int64
	for _, key := range []string{"min_cents", "max_cents"} {
		if raw := q.Get(key); raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || v < 0 {
				invalid()
				return "", nil, false
			}
			if key == "min_cents" {
				minimum = &v
			} else {
				maximum = &v
			}
		}
	}
	if minimum != nil && maximum != nil && *minimum > *maximum {
		invalid()
		return "", nil, false
	}
	args := []any{identityFrom(r).ID, "%" + keyword + "%", customerID, kind, direction, channel, start, end, minimum, maximum}
	from := fmt.Sprintf(` FROM %s WHERE admin_customer_allowed($1,e.user_id)
 AND ($2='%%' OR u.display_name ILIKE $2 OR u.username ILIKE $2 OR u.email ILIKE $2 OR e.description ILIKE $2 OR e.transaction_no ILIKE $2 OR e.invoice_no ILIKE $2)
 AND ($3::bigint=0 OR e.user_id=$3) AND ($4='' OR e.kind=$4) AND ($5='' OR e.direction=$5) AND ($6='' OR e.channel=$6)
 AND ($7::timestamptz IS NULL OR e.occurred_at >= $7) AND ($8::timestamptz IS NULL OR e.occurred_at < $8)
 AND ($9::bigint IS NULL OR e.amount_cents >= $9) AND ($10::bigint IS NULL OR e.amount_cents <= $10)`, transactionEntries)
	return from, args, true
}
