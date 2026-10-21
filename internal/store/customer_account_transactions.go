package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const transactionEntries = `(
 SELECT 'payment:'||pt.id AS event_key,CASE WHEN pt.provider='manual_offline' THEN 'manual_receipt' ELSE 'payment' END AS source,pt.id,pt.user_id,i.id AS invoice_id,i.invoice_no,
 coalesce(pt.completed_at,pt.created_at) AS occurred_at,pt.provider AS channel,pt.amount_cents,
 'income'::text AS direction,coalesce(mc.transaction_no,nullif(pt.external_transaction_id,''),pt.provider_payment_id) AS transaction_no,
 CASE WHEN pt.provider='manual_offline' THEN '人工到账 · ' ELSE '支付成功 · ' END||i.invoice_no AS description,
 CASE WHEN EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.item_type='renew') THEN 'renew'
 WHEN i.invoice_kind='topup' THEN 'topup' WHEN i.order_id IS NOT NULL THEN 'product' ELSE 'other' END AS kind,NULL::bigint AS balance_after
 FROM payment_transactions pt JOIN invoices i ON i.id=pt.invoice_id AND i.user_id=pt.user_id
 LEFT JOIN manual_collections mc ON mc.payment_id=pt.id WHERE pt.status='succeeded'
 UNION ALL
 SELECT 'external-refund:'||r.id,'external_refund',r.id,r.user_id,i.id,i.invoice_no,r.received_at,
 r.provider,r.amount_cents,'expense',r.provider_refund_id,'外部退款到账 · '||i.invoice_no,
 'other',NULL::bigint
 FROM external_refunds r JOIN invoices i ON i.id=r.invoice_id AND i.user_id=r.user_id WHERE r.status='confirmed'
 UNION ALL
 SELECT 'ledger:'||l.id,'ledger',l.id,l.user_id,i.id,i.invoice_no,l.created_at,
 CASE WHEN l.reference_type='admin_topup' THEN 'admin_topup' WHEN l.reference_type='gateway_topup' THEN 'gateway_topup' ELSE 'balance' END,abs(l.delta_cents),
 CASE WHEN l.reference_type='gateway_topup' THEN 'neutral' WHEN l.delta_cents<0 THEN 'expense' WHEN l.delta_cents>0 THEN 'income' ELSE 'neutral' END,
 'L-'||l.id,coalesce(nullif(l.note,''),l.entry_type),
 CASE WHEN l.entry_type='topup' THEN 'topup'
 WHEN l.entry_type='payment' AND EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.item_type='renew') THEN 'renew'
 WHEN l.entry_type='payment' AND i.order_id IS NOT NULL THEN 'product' ELSE 'other' END,l.balance_after
 FROM balance_ledger l LEFT JOIN invoices i ON l.reference_type IN ('invoice','gateway_topup') AND i.id=l.reference_id AND i.user_id=l.user_id
 UNION ALL
 SELECT 'credit-consumption:'||cp.invoice_id,'credit_consumption',cp.invoice_id,cp.user_id,i.id,i.invoice_no,cp.created_at,
 'credit',cp.amount_cents,'neutral',i.invoice_no,'信用消费 · '||i.invoice_no,
 CASE WHEN EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.item_type='renew') THEN 'renew'
 WHEN i.order_id IS NOT NULL THEN 'product' ELSE 'other' END,NULL::bigint
 FROM credit_invoice_payments cp JOIN invoices i ON i.id=cp.invoice_id AND i.user_id=cp.user_id
 UNION ALL
 SELECT 'credit-repayment:'||r.id,'credit_repayment',r.id,r.user_id,i.id,i.invoice_no,r.received_at,
 'credit_repayment',r.amount_cents,'income','CR-'||r.id,'信用还款到账 · '||i.invoice_no,
 CASE WHEN EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.item_type='renew') THEN 'renew'
 WHEN i.order_id IS NOT NULL THEN 'product' ELSE 'other' END,NULL::bigint
 FROM credit_repayments r JOIN invoices i ON i.id=r.invoice_id AND i.user_id=r.user_id
 UNION ALL
 SELECT 'invoice:'||i.id,'invoice',i.id,i.user_id,i.id,i.invoice_no,i.paid_at,
 'unrecorded',
 i.amount_cents,'neutral',i.invoice_no,'已付账单 · '||i.invoice_no,
 CASE WHEN EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.item_type='renew') THEN 'renew'
 WHEN i.order_id IS NOT NULL THEN 'product' ELSE 'other' END,NULL::bigint
 FROM invoices i WHERE i.status='paid' AND i.paid_at IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.user_id=i.user_id AND pt.status='succeeded')
 AND NOT EXISTS(SELECT 1 FROM balance_ledger l WHERE l.reference_type='invoice' AND l.reference_id=i.id AND l.user_id=i.user_id AND l.entry_type='payment')
 AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id AND cp.user_id=i.user_id)
) e JOIN users u ON u.id=e.user_id LEFT JOIN administrators owner ON owner.id=u.sales_owner_id`

type AdminTransactionFilter struct {
	Keyword, Kind, Direction, Channel string
	CustomerID                        int64
	Start, End                        *time.Time
	Minimum, Maximum                  *int64
}

func adminTransactionEntryFilter(actor int64, in AdminTransactionFilter) (string, []any) {
	args := []any{actor, "%" + in.Keyword + "%", in.CustomerID, in.Kind, in.Direction, in.Channel, in.Start, in.End, in.Minimum, in.Maximum}
	from := fmt.Sprintf(` FROM %s WHERE admin_customer_allowed($1,e.user_id)
 AND ($2='%%' OR u.display_name ILIKE $2 OR u.username ILIKE $2 OR u.email ILIKE $2 OR e.description ILIKE $2 OR e.transaction_no ILIKE $2 OR e.invoice_no ILIKE $2)
 AND ($3::bigint=0 OR e.user_id=$3) AND ($4='' OR e.kind=$4) AND ($5='' OR e.direction=$5) AND ($6='' OR e.channel=$6)
 AND ($7::timestamptz IS NULL OR e.occurred_at >= $7) AND ($8::timestamptz IS NULL OR e.occurred_at < $8)
 AND ($9::bigint IS NULL OR e.amount_cents >= $9) AND ($10::bigint IS NULL OR e.amount_cents <= $10)`, transactionEntries)
	return from, args
}
func (s *Store) AdminTransactionEntryPage(ctx context.Context, actor int64, in AdminTransactionFilter, limit, offset int) (map[string]any, error) {
	from, args := adminTransactionEntryFilter(actor, in)
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var total int64
	if err = tx.QueryRow(ctx, "SELECT count(*)"+from, args...).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT e.*,u.display_name AS customer_name,u.username AS customer_username,owner.display_name AS sales_owner_name,'CNY'::text AS currency`+from+fmt.Sprintf(" ORDER BY e.occurred_at DESC,e.event_key DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), append(args, limit, offset)...)
	items, err := collectMaps(rows, err)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, nil
}

// Wallet movements and credit consumption are account events. Confirmed credit
// repayments are external receipts, separate from gateway receipts.
func (s *Store) AdminTransactionEntrySummary(ctx context.Context, actor int64, in AdminTransactionFilter) (map[string]any, error) {
	from, args := adminTransactionEntryFilter(actor, in)
	var income, manualIncome, refunds, manualRefunds, creditRepayments int64
	err := s.Pool.QueryRow(ctx, `SELECT coalesce(sum(e.amount_cents) FILTER (WHERE e.source='payment'),0)::bigint,
 coalesce(sum(e.amount_cents) FILTER (WHERE e.source='manual_receipt'),0)::bigint,
 coalesce(sum(e.amount_cents) FILTER (WHERE e.source='external_refund'),0)::bigint,
 coalesce(sum(e.amount_cents) FILTER (WHERE e.source='external_refund' AND e.channel='manual_offline'),0)::bigint,
 coalesce(sum(e.amount_cents) FILTER (WHERE e.source='credit_repayment'),0)::bigint`+from, args...).Scan(&income, &manualIncome, &refunds, &manualRefunds, &creditRepayments)
	if err != nil {
		return nil, err
	}
	cashIncome := income + manualIncome + creditRepayments
	return map[string]any{
		"gateway_income_cents": income, "manual_income_cents": manualIncome, "external_refund_cents": refunds, "net_gateway_cents": income - (refunds - manualRefunds),
		"manual_refund_cents": manualRefunds, "net_manual_cents": manualIncome - manualRefunds,
		"credit_repayment_cents": creditRepayments, "cash_income_cents": cashIncome,
		"net_cash_cents": cashIncome - refunds, "currency": "CNY",
	}, nil
}
