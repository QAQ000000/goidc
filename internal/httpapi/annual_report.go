package httpapi

import (
	"net/http"
	"time"
)

type annualCashMonth struct {
	Month                    string `json:"month,omitempty"`
	ReceivedCents            int64  `json:"received_cents"`
	GatewayReceivedCents     int64  `json:"gateway_received_cents"`
	ManualReceivedCents      int64  `json:"manual_received_cents"`
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
		total.ManualReceivedCents += m.ManualReceivedCents
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
		ManualReceivedCents:      current.ManualReceivedCents - previous.ManualReceivedCents,
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
func (s *Server) annualCashReport(w http.ResponseWriter, r *http.Request) {
	period, err := parseReportPeriod(r, false)
	if s.reportInputFailure(w, r, err) {
		return
	}
	if period.Month != 0 {
		writeError(w, 400, "invalid_report_period", "年度现金报表只接受年份")
		return
	}
	report, err := s.db.AnnualCashReport(requestFileContext(r), identityFrom(r).ID, period.Start, period.End)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	previous, current := []annualCashMonth{}, []annualCashMonth{}
	for _, row := range report.Months {
		m := annualCashMonth{
			Month: row.Month, GatewayReceivedCents: row.GatewayReceivedCents, ManualReceivedCents: row.ManualReceivedCents,
			CreditReceivedCents: row.CreditReceivedCents, AnomalyReceivedCents: row.AnomalyReceivedCents,
			UnverifiedAnomalyCents: row.UnverifiedAnomalyCents, RefundCents: row.RefundCents,
			BusinessConsumptionCents: row.BusinessConsumption, SimulatedReceivedCents: row.SimulatedReceived,
			SimulatedRefundCents: row.SimulatedRefund,
		}
		m.ReceivedCents = m.GatewayReceivedCents + m.ManualReceivedCents + m.CreditReceivedCents + m.AnomalyReceivedCents
		m.setExpenses(row.ExpenseCents, report.Global)
		date, _ := time.Parse("2006-01", m.Month)
		if date.Year() == period.Year {
			current = append(current, m)
		} else {
			previous = append(previous, m)
		}
	}
	currentTotal, previousTotal := annualCashTotal(current, report.Global), annualCashTotal(previous, report.Global)
	writeReport(w, r, map[string]any{"year": period.Year, "currency": "CNY", "expenses_available": report.Global, "expenses_manageable": report.Manageable, "months": current, "total": currentTotal,
		"previous_year": map[string]any{"year": period.Year - 1, "months": previous, "total": previousTotal}, "comparison": annualCashComparison(currentTotal, previousTotal, report.Global)})
}
