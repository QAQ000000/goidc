package httpapi

import (
	"fmt"
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const topupRetryProjection = `i.status='unpaid' AND EXISTS(SELECT 1 FROM balance_topups t WHERE t.invoice_id=i.id AND t.status='accepted') AND EXISTS(SELECT 1 FROM payment_transactions p WHERE p.invoice_id=i.id AND p.status='closed') AND NOT EXISTS(SELECT 1 FROM payment_transactions p WHERE p.invoice_id=i.id AND p.status<>'closed') AND NOT EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AND NOT EXISTS(SELECT 1 FROM balance_topup_settlements s WHERE s.invoice_id=i.id)`
const invoicePendingProjection = `EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AS payment_close_anomaly,
 ` + topupRetryProjection + ` AS topup_can_retry,
(SELECT t.status FROM balance_topups t WHERE t.invoice_id=i.id) AS topup_status,
 (SELECT s.amount_cents FROM balance_topup_settlements s WHERE s.invoice_id=i.id AND s.status='pending_liability' AND NOT EXISTS(SELECT 1 FROM balance_topup_liability_resolutions r WHERE r.topup_id=s.topup_id)) AS topup_pending_liability_cents,
 (i.status='unpaid' AND EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status IN ('pending','expired'))) OR EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AS payment_pending,
 EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status='succeeded') AS external_payment_paid,
 (i.status='unpaid' AND EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status IN ('pending','expired') AND pt.provider<>'mock')) OR EXISTS(SELECT 1 FROM payment_close_operations c WHERE c.invoice_id=i.id AND payment_close_operation_unresolved(c.id)) OR EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AS real_payment_pending, (SELECT jsonb_build_object('remaining_cents',cp.remaining_cents,'status',cp.status,'amount_cents',cp.amount_cents) FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id) AS credit_payment`
const maskedPurchaseFields = `coalesce((SELECT jsonb_agg(CASE WHEN field->>'encrypted'='true' THEN jsonb_set(field,'{value}','"********"'::jsonb) ELSE field END) FROM jsonb_array_elements(purchase_fields) field),'[]'::jsonb)`
const orderItemProjection = `id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents,config_options,option_quantities,config_snapshot,` + maskedPurchaseFields + ` AS purchase_fields,renewal_discount_percent,renewal_promo_code,renewal_fixed_discount_cents,renewal_discount_limit`

func collectTransactionRows(rows pgx.Rows, err error) ([]map[string]any, error) {
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if items == nil && err == nil {
		items = []map[string]any{}
	}
	return items, err
}

func (s *Server) transactionPage(w http.ResponseWriter, r *http.Request, projection, from, sort string, args []any, limit, offset int) {
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	page, err := listPage(r.Context(), tx, "SELECT count(*)"+from,
		fmt.Sprintf("SELECT %s%s ORDER BY %s DESC", projection, from, sort), args, limit, offset, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, page)
}

func (s *Server) customerOrderDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := s.db.ReadCustomerOrder(r.Context(), identityFrom(r).ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) invoiceDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := s.db.ReadInvoice(r.Context(), identityFrom(r).ID, id, store.InvoiceReadOptions{
		Admin: adminRoute(r), CanClosePayment: identityFrom(r).can("payments.close"),
		CanVerifyClose: identityFrom(r).can("payments.close_verify"), PaymentFallback: s.paymentFallback(),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}
