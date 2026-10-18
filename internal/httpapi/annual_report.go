package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type annualCashMonth struct {
	Month                    string `json:"month,omitempty"`
	ReceivedCents            int64  `json:"received_cents"`
	GatewayReceivedCents     int64  `json:"gateway_received_cents"`
	CreditReceivedCents      int64  `json:"credit_received_cents"`
	AnomalyReceivedCents     int64  `json:"anomaly_received_cents"`
	UnverifiedAnomalyCents   int64  `json:"unverified_anomaly_cents"`
	RefundCents              int64  `json:"refund_cents"`
	ManualExpenseCents       *int64 `json:"manual_expense_cents"`
	OutgoingCents            *int64 `json:"outgoing_cents"`
	NetCents                 *int64 `json:"net_cents"`
	BusinessConsumptionCents int64  `json:"business_consumption_cents"`
	SimulatedReceivedCents   int64  `json:"simulated_received_cents"`
	SimulatedRefundCents     int64  `json:"simulated_refund_cents"`
}

func (m *annualCashMonth) setExpenses(expense int64, available bool) {
	if available {
		outgoing, net := m.RefundCents+expense, m.ReceivedCents-m.RefundCents-expense
		m.ManualExpenseCents, m.OutgoingCents, m.NetCents = &expense, &outgoing, &net
	}
}

func annualCashTotal(months []annualCashMonth, available bool) annualCashMonth {
	var total annualCashMonth
	var expense int64
	for _, m := range months {
		total.ReceivedCents += m.ReceivedCents
		total.GatewayReceivedCents += m.GatewayReceivedCents
		total.CreditReceivedCents += m.CreditReceivedCents
		total.AnomalyReceivedCents += m.AnomalyReceivedCents
		total.UnverifiedAnomalyCents += m.UnverifiedAnomalyCents
		total.RefundCents += m.RefundCents
		total.BusinessConsumptionCents += m.BusinessConsumptionCents
		total.SimulatedReceivedCents += m.SimulatedReceivedCents
		total.SimulatedRefundCents += m.SimulatedRefundCents
		if m.ManualExpenseCents != nil {
			expense += *m.ManualExpenseCents
		}
	}
	total.setExpenses(expense, available)
	return total
}

func annualCashComparison(current, previous annualCashMonth, available bool) annualCashMonth {
	diff := annualCashMonth{
		ReceivedCents:            current.ReceivedCents - previous.ReceivedCents,
		GatewayReceivedCents:     current.GatewayReceivedCents - previous.GatewayReceivedCents,
		CreditReceivedCents:      current.CreditReceivedCents - previous.CreditReceivedCents,
		AnomalyReceivedCents:     current.AnomalyReceivedCents - previous.AnomalyReceivedCents,
		UnverifiedAnomalyCents:   current.UnverifiedAnomalyCents - previous.UnverifiedAnomalyCents,
		RefundCents:              current.RefundCents - previous.RefundCents,
		BusinessConsumptionCents: current.BusinessConsumptionCents - previous.BusinessConsumptionCents,
		SimulatedReceivedCents:   current.SimulatedReceivedCents - previous.SimulatedReceivedCents,
		SimulatedRefundCents:     current.SimulatedRefundCents - previous.SimulatedRefundCents,
	}
	if available {
		diff.setExpenses(*current.ManualExpenseCents-*previous.ManualExpenseCents, true)
	}
	return diff
}

// Cash sources are separate from paid business invoices. In particular, an
// online top-up is one gateway receipt; spending that balance is consumption.
// Refunds use evidence received_at, not the later confirmation timestamp.
const annualCashSQL = `WITH normal_claims AS (
 SELECT * FROM jsonb_to_recordset($5::jsonb) AS n(external_transaction_id text,merchant_scope text)
), events AS (
 SELECT pt.user_id,coalesce(pt.completed_at,pt.created_at) occurred_at,CASE WHEN pt.provider='mock' THEN 'simulation' ELSE 'gateway' END kind,pt.amount_cents amount
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

func (s *Server) annualCashReport(w http.ResponseWriter, r *http.Request) {
	period, err := parseReportPeriod(r, false)
	if s.reportInputFailure(w, r, err) {
		return
	}
	if period.Month != 0 {
		writeError(w, 400, "invalid_report_period", "年度现金报表只接受年份")
		return
	}
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	global, manageable, err := reportExpenseAccess(r, tx, "reports.view")
	if err != nil {
		if e, ok := err.(*txHTTPError); ok {
			writeError(w, e.status, e.code, e.message)
		} else {
			s.fail(w, r, err)
		}
		return
	}
	// External numbers are only unique inside a merchant. Resolve original
	// successful-payment scopes once, including credential rotation, so another
	// merchant using the same number cannot hide a real late receipt.
	type claim struct {
		ExternalTransactionID string  `json:"external_transaction_id"`
		MerchantScope         *string `json:"merchant_scope"`
	}
	claims := []claim{}
	claimRows, err := tx.Query(r.Context(), `SELECT p.external_transaction_id,p.provider,p.provider_payment_id,p.credential_snapshot FROM payment_transactions p
 WHERE p.status='succeeded' AND p.external_transaction_id<>'' AND EXISTS(SELECT 1 FROM payment_close_anomaly_groups g WHERE g.external_transaction_id=p.external_transaction_id AND g.first_seen >= $1 AND g.first_seen < $2)`, period.Start.AddDate(-1, 0, 0), period.End)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for claimRows.Next() {
		var c claim
		var provider, reference string
		var sealed []byte
		if err = claimRows.Scan(&c.ExternalTransactionID, &provider, &reference, &sealed); err != nil {
			claimRows.Close()
			s.fail(w, r, err)
			return
		}
		if scope, e := s.db.ReportPaymentMerchantScope(provider, reference, sealed); e == nil {
			c.MerchantScope = &scope
		}
		claims = append(claims, c)
	}
	err = claimRows.Err()
	claimRows.Close()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := tx.Query(r.Context(), annualCashSQL, period.Start.AddDate(-1, 0, 0), period.End, identityFrom(r).ID, global, claimsJSON)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	months, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (annualCashMonth, error) {
		var m annualCashMonth
		var expense int64
		err := row.Scan(&m.Month, &m.GatewayReceivedCents, &m.CreditReceivedCents, &m.AnomalyReceivedCents, &m.UnverifiedAnomalyCents, &m.RefundCents, &expense, &m.BusinessConsumptionCents, &m.SimulatedReceivedCents, &m.SimulatedRefundCents)
		m.ReceivedCents = m.GatewayReceivedCents + m.CreditReceivedCents + m.AnomalyReceivedCents
		m.setExpenses(expense, global)
		return m, err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	// The SQL always returns 24 months, including an entirely empty prior year.
	previous, current := []annualCashMonth{}, []annualCashMonth{}
	for _, m := range months {
		date, _ := time.Parse("2006-01", m.Month)
		if date.Year() == period.Year {
			current = append(current, m)
		} else {
			previous = append(previous, m)
		}
	}
	currentTotal, previousTotal := annualCashTotal(current, global), annualCashTotal(previous, global)
	writeReport(w, r, map[string]any{"year": period.Year, "currency": "CNY", "expenses_available": global, "expenses_manageable": manageable, "months": current, "total": currentTotal,
		"previous_year": map[string]any{"year": period.Year - 1, "months": previous, "total": previousTotal}, "comparison": annualCashComparison(currentTotal, previousTotal, global)})
}
