package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// These are the actual source assertions, shared by the early parser preflight
// and pending/drained verification. Preparing never executes terminal SQL or
// changes fixture data. In particular, SQL parameter inference errors must fail
// before any source request or worker has started.
const (
	g09CohortSQLTicketOwner         = `SELECT t.user_id=$2 AND t.department_id=$3 AND (SELECT count(*) FROM ticket_mutation_receipts WHERE ticket_id=t.id AND actor_id=$2 AND operation='create')=1 AND (SELECT count(*) FROM ticket_notification_events WHERE ticket_id=t.id AND actor_id=$2 AND kind='customer_created')=1 FROM tickets t WHERE t.id=$1`
	g09CohortSQLTicketDeliveries    = `SELECT count(*)=2 AND bool_and(d.recipient_id=$2 AND d.outcome='queued' AND d.reason='' AND d.address_verified AND d.preference_enabled AND d.event_id=$3) FROM ticket_notification_events e JOIN ticket_notification_deliveries d ON d.event_id=e.id WHERE e.ticket_id=$1`
	g09CohortSQLTicketOutbox        = `SELECT m.status=$3 AND m.attempts=$4 AND m.locked_at IS NULL AND m.kind='ticket_customer_created' AND m.channel_instance_id=$5 AND m.recipient=$6 AND m.subject=$7 AND m.body=$8 AND m.expires_at>now()+interval '6 days' AND s.status=$3 AND s.attempts=$4 AND s.locked_at IS NULL AND s.kind='ticket_customer_created' AND s.channel_instance_id=$9 AND s.recipient=$10 AND s.expires_at>now()+interval '6 days' FROM email_outbox m CROSS JOIN sms_outbox s WHERE m.id=$1 AND s.id=$2`
	g09CohortSQLTicketSentEvidence  = `SELECT s.sent_at IS NOT NULL AND s.last_error='' AND octet_length(s.body_encrypted)=0 AND (SELECT count(*) FROM sms_delivery_evidence d WHERE d.outbox_id=s.id AND d.attempts=1 AND d.provider='generic_http_json' AND d.outcome='accepted' AND d.http_status=200 AND NOT d.response_truncated AND octet_length(d.response_encrypted)>0 AND d.locked_at IS NOT NULL)=1 AND (SELECT sent_at IS NOT NULL AND last_error='' FROM email_outbox WHERE id=$2) FROM sms_outbox s WHERE s.id=$1`
	g09CohortSQLPurchase            = `SELECT i.status='paid' AND i.user_id=$2 AND i.amount_cents=1000 AND i.paid_at IS NOT NULL AND i.order_id=$3 AND o.status='paid' AND o.review_status='approved' AND o.total_cents=1000 AND oi.id=$4 AND oi.product_id=$5 AND oi.quantity=1 AND s.id=$6 AND s.user_id=$2 AND s.product_id=$5 AND s.unit_number=1 AND s.provision_mode=$7 AND s.status=$8 AND NOT s.auto_renew AND NOT s.inventory_released AND j.id=$9 AND j.action='create' AND j.status=$10 AND j.attempts=$11 AND j.locked_at IS NULL AND (SELECT count(*) FROM balance_ledger l WHERE l.user_id=$2 AND l.reference_type='invoice' AND l.reference_id=i.id AND l.entry_type='payment' AND l.delta_cents=-1000)=1 AND (SELECT count(*) FROM order_items WHERE order_id=$3)=1 AND (SELECT count(*) FROM services WHERE order_item_id=$4)=1 AND (SELECT count(*) FROM provision_jobs WHERE service_id=$6 AND action='create')=1 FROM invoices i JOIN orders o ON o.id=i.order_id JOIN order_items oi ON oi.order_id=o.id JOIN services s ON s.order_item_id=oi.id JOIN provision_jobs j ON j.service_id=s.id AND j.id=$9 WHERE i.id=$1`
	g09CohortSQLSupplierBinding     = `SELECT (delivery_snapshot->>'supplier_id')::bigint=$2 AND (delivery_snapshot->>'binding_id')::bigint=$3 FROM services WHERE id=$1`
	g09CohortSQLSimulationCompleted = `SELECT s.upstream_id='local-'||s.id::text AND s.next_due_at>now() AND j.completed_at IS NOT NULL AND j.last_error='' AND j.result->>'mode'='simulation' AND (SELECT count(*) FROM audit_events WHERE entity_type='service' AND entity_id=s.id::text AND action='service.simulation.completed' AND (payload->>'job_id')::bigint=j.id)=1 FROM services s JOIN provision_jobs j ON j.service_id=s.id WHERE s.id=$1 AND j.id=$2`
	g09CohortSQLSupplierCompleted   = `SELECT s.upstream_id=$3::bigint::text AND s.domain=$4 AND s.next_due_at=to_timestamp(1795000000) AND j.completed_at IS NOT NULL AND j.external_started_at IS NOT NULL AND j.last_error='' AND (j.result->>'upstream_id')::bigint=$3 AND (j.result->>'invoice_id')::bigint=$5 AND (j.result->>'cost_cents')::bigint=100 AND j.result->>'currency'='CNY' AND (SELECT count(*) FROM audit_events WHERE entity_type='service' AND entity_id=s.id::text AND action='supplier.delivery.succeeded' AND (payload->>'job_id')::bigint=j.id)=1 FROM services s JOIN provision_jobs j ON j.service_id=s.id WHERE s.id=$1 AND j.id=$2`
	g09CohortSQLCustomerBalance     = `SELECT u.balance_cents=8000 AND u.balance_cents=(SELECT sum(delta_cents) FROM balance_ledger WHERE user_id=u.id) AND (SELECT count(*) FROM balance_ledger WHERE user_id=u.id)=3 AND NOT EXISTS(SELECT 1 FROM customer_credit_accounts WHERE user_id=u.id AND used_cents<>0) AND NOT EXISTS(SELECT 1 FROM payment_transactions WHERE user_id=u.id) FROM users u WHERE id=$1`
	g09CohortSQLFinalCounts         = `SELECT (SELECT count(*) FROM services)=300 AND (SELECT count(*) FROM orders)=300 AND (SELECT count(*) FROM invoices)=300 AND (SELECT count(*) FROM email_outbox)=150 AND (SELECT count(*) FROM sms_outbox)=150 AND (SELECT count(*) FROM tickets)=150 AND (SELECT count(*) FROM ticket_notification_events)=150 AND (SELECT count(*) FROM ticket_notification_deliveries)=300 AND NOT EXISTS(SELECT 1 FROM products WHERE stock<>0)`
)

func g09CohortErrorCode(err error) string {
	if err == nil {
		return "none"
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return "unavailable"
}

func g09CohortPrepareOracles(t *testing.T, db *store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal("cohort read-only oracle preflight unavailable")
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := tx.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Error("cohort oracle preflight rollback unavailable")
		}
	}()
	queries := []string{
		g09CohortSQLTicketOwner,
		g09CohortSQLTicketDeliveries,
		g09CohortSQLTicketOutbox,
		g09CohortSQLTicketSentEvidence,
		g09CohortSQLPurchase,
		g09CohortSQLSupplierBinding,
		g09CohortSQLSimulationCompleted,
		g09CohortSQLSupplierCompleted,
		g09CohortSQLCustomerBalance,
		g09CohortSQLFinalCounts,
	}
	for _, query := range queries {
		if _, err := tx.Prepare(ctx, "", query); err != nil {
			t.Fatalf("cohort oracle parse failed query_sha256=%s sqlstate=%s", g09SourceHash(query), g09CohortErrorCode(err))
		}
	}
	t.Logf("PASS: read-only oracle preflight prepared %d actual source assertions before source/worker startup", len(queries))
}
