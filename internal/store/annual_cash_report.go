package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// AnnualCashMonth is the persistence projection used by the administrator
// annual cash report. Manual receipts are kept separate from gateway receipts
// while both remain part of actual cash received.
type AnnualCashMonth struct {
	Month                  string
	GatewayReceivedCents   int64
	ManualReceivedCents    int64
	CreditReceivedCents    int64
	AnomalyReceivedCents   int64
	UnverifiedAnomalyCents int64
	RefundCents            int64
	ExpenseCents           int64
	BusinessConsumption    int64
	SimulatedReceived      int64
	SimulatedRefund        int64
}

type AnnualCashReport struct {
	Months     []AnnualCashMonth
	Global     bool
	Manageable bool
}

const annualCashSQL = `WITH normal_claims AS (
 SELECT * FROM jsonb_to_recordset($5::jsonb) AS n(external_transaction_id text,merchant_scope text)
), events AS (
 SELECT pt.user_id,coalesce(pt.completed_at,pt.created_at) occurred_at,
 CASE WHEN pt.provider='manual_offline' THEN 'manual'
      WHEN pt.provider='mock' THEN 'simulation' ELSE 'gateway' END kind,pt.amount_cents amount
 FROM payment_transactions pt JOIN invoices i ON i.id=pt.invoice_id AND i.user_id=pt.user_id
 WHERE pt.status='succeeded' AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id)
 UNION ALL SELECT cr.user_id,cr.received_at,'credit',cr.amount_cents FROM credit_repayments cr
 UNION ALL SELECT er.user_id,er.received_at,CASE WHEN er.provider='mock' THEN 'simulation_refund' ELSE 'refund' END,er.amount_cents FROM external_refunds er WHERE er.status='confirmed'
 UNION ALL SELECT ar.user_id,ar.received_at,CASE WHEN ar.provider='mock' THEN 'simulation_refund' ELSE 'refund' END,ar.amount_cents FROM payment_close_anomaly_refunds ar WHERE ar.status='confirmed'
 UNION ALL SELECT i.user_id,i.paid_at,'business',i.amount_cents FROM invoices i
 WHERE (i.status='paid' OR (i.status='refunded' AND EXISTS(SELECT 1 FROM external_refunds er WHERE er.invoice_id=i.id AND er.status='confirmed')))
 AND i.invoice_kind<>'topup' AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id)
 UNION ALL SELECT cr.user_id,cr.received_at,'business',cr.amount_cents FROM credit_repayments cr
), anomaly_groups AS (
 SELECT DISTINCT ON(coalesce(b.merchant_scope,'unverified:'||g.payment_id),g.external_transaction_id)
 g.user_id,g.first_seen occurred_at,g.amount_cents amount,
 CASE WHEN EXISTS(SELECT 1 FROM normal_claims n WHERE n.external_transaction_id=g.external_transaction_id AND n.merchant_scope=b.merchant_scope) THEN 'duplicate'
 WHEN g.amount_consistent AND p.status='closed' AND b.merchant_scope IS NOT NULL AND btrim(g.external_transaction_id)<>''
 AND NOT EXISTS(SELECT 1 FROM normal_claims n WHERE n.external_transaction_id=g.external_transaction_id AND n.merchant_scope IS NULL)
 THEN CASE WHEN p.provider='mock' THEN 'simulation' ELSE 'anomaly' END ELSE 'unverified' END kind
 FROM payment_close_anomaly_groups g JOIN payment_transactions p ON p.id=g.payment_id
 LEFT JOIN payment_close_anomaly_merchant_bindings b ON b.payment_id=g.payment_id AND b.credential_sha256=g.credential_sha256
 ORDER BY coalesce(b.merchant_scope,'unverified:'||g.payment_id),g.external_transaction_id,g.first_seen,g.anchor_anomaly_id
), scoped AS (
 SELECT occurred_at,kind,amount FROM (SELECT * FROM events UNION ALL SELECT user_id,occurred_at,kind,amount FROM anomaly_groups) e
 WHERE occurred_at >= $1 AND occurred_at < $2 AND admin_customer_allowed($3,user_id)
 UNION ALL SELECT e.occurred_on::timestamp AT TIME ZONE 'Asia/Shanghai','expense',e.amount_cents FROM report_expenses e
 WHERE $4::boolean AND e.occurred_on>=($1::timestamptz AT TIME ZONE 'Asia/Shanghai')::date AND e.occurred_on<($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date
 AND NOT EXISTS(SELECT 1 FROM report_expense_voids v WHERE v.expense_id=e.id)
), monthly AS (
 SELECT date_trunc('month',occurred_at AT TIME ZONE 'Asia/Shanghai') AS month,kind,sum(amount)::bigint amount FROM scoped GROUP BY 1,2
)
SELECT to_char(d.month,'YYYY-MM'),
 coalesce(sum(amount) FILTER(WHERE kind='gateway'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='manual'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='credit'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='anomaly'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='unverified'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='refund'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='expense'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='business'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='simulation'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='simulation_refund'),0)::bigint
FROM generate_series(date_trunc('month',$1::timestamptz AT TIME ZONE 'Asia/Shanghai'),
 date_trunc('month',$2::timestamptz AT TIME ZONE 'Asia/Shanghai')-interval '1 month',interval '1 month')d(month)
LEFT JOIN monthly m USING(month) GROUP BY d.month ORDER BY d.month`

func (s *Store) AnnualCashReport(ctx context.Context, actor int64, start, end time.Time) (AnnualCashReport, error) {
	var out AnnualCashReport
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	global, manageable, err := ReportExpenseAccess(ctx, tx, actor, "reports.view")
	if err != nil {
		return out, err
	}
	type claim struct {
		ExternalTransactionID string  `json:"external_transaction_id"`
		MerchantScope         *string `json:"merchant_scope"`
	}
	claims := []claim{}
	claimRows, err := tx.Query(ctx, `SELECT coalesce(c.transaction_no,p.external_transaction_id),p.provider,p.provider_payment_id,p.credential_snapshot FROM payment_transactions p
 LEFT JOIN manual_collections c ON c.payment_id=p.id
 WHERE p.status='succeeded' AND coalesce(c.transaction_no,p.external_transaction_id)<>''
 AND EXISTS(SELECT 1 FROM payment_close_anomaly_groups g WHERE g.external_transaction_id=coalesce(c.transaction_no,p.external_transaction_id) AND g.first_seen >= $1 AND g.first_seen < $2)`, start.AddDate(-1, 0, 0), end)
	if err != nil {
		return out, err
	}
	for claimRows.Next() {
		var c claim
		var provider, reference string
		var sealed []byte
		if err = claimRows.Scan(&c.ExternalTransactionID, &provider, &reference, &sealed); err != nil {
			claimRows.Close()
			return out, err
		}
		// An offline account cannot attest that a late gateway receipt belongs
		// to the same merchant. Keep the raw claim with unknown scope so that
		// overlapping observations remain unverified rather than extra cash.
		if provider != ManualCollectionProvider {
			if scope, e := s.ReportPaymentMerchantScope(provider, reference, sealed); e == nil {
				c.MerchantScope = &scope
			}
		}
		claims = append(claims, c)
	}
	if err = claimRows.Err(); err != nil {
		claimRows.Close()
		return out, err
	}
	claimRows.Close()
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, annualCashSQL, start.AddDate(-1, 0, 0), end, actor, global, claimsJSON)
	if err != nil {
		return out, err
	}
	out.Months, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AnnualCashMonth, error) {
		var m AnnualCashMonth
		err := row.Scan(&m.Month, &m.GatewayReceivedCents, &m.ManualReceivedCents, &m.CreditReceivedCents, &m.AnomalyReceivedCents, &m.UnverifiedAnomalyCents, &m.RefundCents, &m.ExpenseCents, &m.BusinessConsumption, &m.SimulatedReceived, &m.SimulatedRefund)
		return m, err
	})
	if err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	out.Global, out.Manageable = global, manageable
	return out, nil
}
