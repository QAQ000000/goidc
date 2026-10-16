package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const invoicePendingProjection = `(SELECT t.status FROM balance_topups t WHERE t.invoice_id=i.id) AS topup_status,
 (SELECT s.amount_cents FROM balance_topup_settlements s WHERE s.invoice_id=i.id AND s.status='pending_liability') AS topup_pending_liability_cents,
 i.status='unpaid' AND EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status IN ('pending','expired')) AS payment_pending,
 EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status='succeeded') AS external_payment_paid,
 i.status='unpaid' AND EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status IN ('pending','expired') AND pt.provider<>'mock') AS real_payment_pending, (SELECT jsonb_build_object('remaining_cents',cp.remaining_cents,'status',cp.status,'amount_cents',cp.amount_cents) FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id) AS credit_payment`
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
	var total int64
	if err = tx.QueryRow(r.Context(), "SELECT count(*)"+from, args...).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	args = append(args, limit, offset)
	items, err := collectTransactionRows(tx.Query(r.Context(), fmt.Sprintf("SELECT %s%s ORDER BY %s DESC LIMIT $%d OFFSET $%d", projection, from, sort, len(args)-1, len(args)), args...))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) customerOrderDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	uid := identityFrom(r).ID
	tx, err := s.db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT o.version,o.id,o.order_no,o.status,o.review_status,o.total_cents,o.currency,o.promo_code,o.discount_cents,o.created_at,o.updated_at,
 i.id AS invoice_id,i.invoice_no,i.status AS invoice_status,i.amount_cents,i.paid_at,i.due_at
 FROM orders o LEFT JOIN invoices i ON i.order_id=o.id AND i.user_id=o.user_id WHERE o.id=$1 AND o.user_id=$2 AND o.deleted_at IS NULL`, id, uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	order, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := collectTransactionRows(tx.Query(ctx, "SELECT "+orderItemProjection+" FROM order_items WHERE order_id=$1 ORDER BY id", id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	services, err := collectTransactionRows(tx.Query(ctx, `SELECT id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,unit_number,created_at
 FROM services WHERE order_id=$1 AND user_id=$2 ORDER BY id`, id, uid))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	order["items"], order["services"] = items, services
	if err = tx.Commit(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, order)
}

func (s *Server) invoiceDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	admin, actor := adminRoute(r), identityFrom(r).ID
	isolation := pgx.RepeatableRead
	if admin {
		// Recheck sales ownership using a fresh snapshot after the scope lock.
		isolation = pgx.ReadCommitted
	}
	tx, err := s.db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	if admin {
		if err = store.LockAdminResourceCustomer(ctx, tx, actor, "invoice", id); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	projection := `i.id,i.invoice_no,i.user_id,o.id AS order_id,o.order_no,i.status,i.amount_cents,i.due_at,i.paid_at,i.created_at,i.updated_at,i.version,i.invoice_kind,
 i.import_review_required,coalesce(o.promo_code,(SELECT ii.renewal_promo_code FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.renewal_promo_code<>'' LIMIT 1),'') AS promo_code,
 coalesce(o.discount_cents,(SELECT sum(ii.renewal_discount_cents) FROM invoice_items ii WHERE ii.invoice_id=i.id),0) AS discount_cents,` + invoicePendingProjection
	if admin {
		projection += ",u.display_name AS customer_name,u.email"
	}
	query := "SELECT " + projection + ` FROM invoices i JOIN users u ON u.id=i.user_id
 LEFT JOIN orders o ON o.id=i.order_id AND o.user_id=i.user_id WHERE i.id=$1 AND ($2::boolean OR i.user_id=$3)`
	if admin {
		query += " FOR SHARE OF i"
	}
	rows, err := tx.Query(ctx, query, id, admin, actor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	invoice, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var topupJSON []byte
	if invoice["invoice_kind"] == "topup" {
		err = tx.QueryRow(ctx, `SELECT jsonb_build_object('status',t.status,'settings_version',t.settings_version,'accepted_at',t.accepted_at,'failure_code',coalesce(s.failure_code,''),'payment_id',s.payment_id,'ledger_id',s.ledger_id,'amount_cents',t.amount_cents) FROM balance_topups t LEFT JOIN balance_topup_settlements s ON s.topup_id=t.id WHERE t.invoice_id=$1`, id).Scan(&topupJSON)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		var topup any
		if err = json.Unmarshal(topupJSON, &topup); err != nil {
			s.fail(w, r, err)
			return
		}
		invoice["topup"] = topup
	}
	items, err := collectTransactionRows(tx.Query(ctx, `SELECT ii.id,ii.description,ii.item_type,ii.amount_cents,ii.renewal_base_cents,ii.renewal_discount_cents,ii.renewal_discount_percent,ii.renewal_promo_code,ii.renewal_fixed_discount_cents,ii.renewal_discount_limit,ii.renewal_discount_ordinal,ii.upgrade_base_cents,ii.upgrade_discount_cents,ii.upgrade_promo_code,s.id AS service_id,s.name AS service_name
 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id LEFT JOIN services s ON s.id=ii.service_id AND s.user_id=i.user_id
 WHERE ii.invoice_id=$1 ORDER BY ii.id`, id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	invoiceOrderProjection := strings.Replace(orderItemProjection, maskedPurchaseFields+` AS purchase_fields`, `jsonb_path_query_array(purchase_fields,'$[*] ? (@.show_invoice == true)') AS purchase_fields`, 1)
	orderItems, err := collectTransactionRows(tx.Query(ctx, "SELECT "+invoiceOrderProjection+` FROM order_items WHERE order_id IN
 (SELECT o.id FROM orders o JOIN invoices i ON i.order_id=o.id AND i.user_id=o.user_id WHERE i.id=$1) ORDER BY id`, id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	payments, err := collectTransactionRows(tx.Query(ctx, `SELECT pt.id,pt.provider,pt.provider_payment_id,pt.external_transaction_id,CASE WHEN $2::boolean THEN '' ELSE pt.checkout_url END AS checkout_url,pt.amount_cents,pt.status,pt.created_at,pt.completed_at,pt.expires_at,
 (pt.status='expired' OR pt.status='pending' AND pt.expires_at<=clock_timestamp()) AS expired,
 (SELECT c.status FROM payment_reconciliation_checks c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS check_status,
 (SELECT c.result_code FROM payment_reconciliation_checks c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS check_code,
 (SELECT c.started_at FROM payment_reconciliation_checks c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS checked_at,
 (SELECT c.source FROM payment_reconciliation_checks c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS check_source,
 pt.next_reconcile_at,pt.auto_reconcile_attempts,
 pt.status IN ('pending','expired') AND pt.provider LIKE 'epay_%' AND pt.auto_reconcile_attempts>=32 AND pt.next_reconcile_at IS NULL AS reconciliation_exhausted
 FROM payment_transactions pt JOIN invoices i ON i.id=pt.invoice_id AND i.user_id=pt.user_id WHERE i.id=$1 ORDER BY pt.id DESC LIMIT 50`, id, admin))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	configuration, err := s.db.PaymentConfigurationTx(ctx, tx, s.paymentFallback())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, payment := range payments {
		_, enabled, gatewayErr := s.db.PaymentTransactionGatewayTx(ctx, tx, payment["provider"].(string), payment["provider_payment_id"].(string), configuration)
		payment["query_enabled"] = gatewayErr == nil && enabled
		if payment["provider"] == "mock" {
			payment["query_enabled"] = false
		}
	}
	ledger, err := collectTransactionRows(tx.Query(ctx, `SELECT l.id,l.entry_type,l.delta_cents,l.balance_after,l.created_at
 FROM balance_ledger l JOIN invoices i ON i.id=l.reference_id AND i.user_id=l.user_id
 WHERE l.reference_type IN ('invoice','gateway_topup') AND i.id=$1 ORDER BY l.id DESC LIMIT 50`, id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	invoice["items"], invoice["order_items"], invoice["payments"], invoice["ledger"] = items, orderItems, payments, ledger
	if err = tx.Commit(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, invoice)
}
