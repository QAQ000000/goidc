package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/jackc/pgx/v5"
)

type administratorPaymentFixture struct {
	db       *Store
	owner    int64
	payments map[string]PaymentIntentResult
}

// Construct real pre-split administrator commerce, including a verified closed
// payment, then apply the actual 181 -> 182 migration without disabling guards.
func historicalAdministratorPayments(t *testing.T) administratorPaymentFixture {
	t.Helper()
	db := adminOrderReceiptSchema178(t)
	for _, name := range []string{"179_admin_order_mutation_receipts.sql", "180_customer_group_pricing.sql", "181_auth_login_portal.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	f := administratorPaymentFixture{db: db, payments: map[string]PaymentIntentResult{}}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role,balance_cents) VALUES('legacy-payment-root','legacy-payment@test.invalid','never-log-secret','历史管理员','super_admin',731) RETURNING id`).Scan(&f.owner); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"order", "topup"} {
		for _, state := range []string{"pending", "closed", "succeeded"} {
			name := kind + "-" + state
			p := PaymentIntentResult{Provider: "epay_alipay", ProviderPaymentID: "legacy-payment-" + name, AmountCents: 12000, Status: state}
			if kind == "order" {
				r := lifecycleFixtureResources(t, notificationFixture{db: db, ctx: t.Context()}, f.owner, "admin-payment-"+name)
				p.InvoiceID = r.invoice
				if _, err := db.Pool.Exec(t.Context(), `UPDATE services SET status='pending_payment' WHERE id=$1`, r.service); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,invoice_kind,amount_cents) VALUES($1,$2,'topup',12000) RETURNING id`, name, f.owner).Scan(&p.InvoiceID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Pool.Exec(t.Context(), `INSERT INTO balance_topups(invoice_id,user_id,amount_cents,status,settings_version,accepted_settings,accepted_at,idempotency_key,request_hash) VALUES($1,$2,12000,'accepted',1,'{}',now(),$3,'fixture')`, p.InvoiceID, f.owner, "historical-"+name); err != nil {
					t.Fatal(err)
				}
			}
			if state == "succeeded" {
				if _, err := db.Pool.Exec(t.Context(), `UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1`, p.InvoiceID); err != nil {
					t.Fatal(err)
				}
			}
			// Insert pending first so a closed status is backed by its actual close
			// evidence before the deferred trusted-close constraint is checked.
			if err := db.Pool.QueryRow(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,external_transaction_id,next_reconcile_at) VALUES($1,$2,$3,$4,12000,$5,$6,clock_timestamp()) RETURNING id`, p.Provider, p.ProviderPaymentID, p.InvoiceID, f.owner, map[bool]string{true: "succeeded", false: "pending"}[state == "succeeded"], map[bool]string{true: "receipt-" + p.ProviderPaymentID, false: ""}[state == "succeeded"]).Scan(&p.PaymentID); err != nil {
				t.Fatal(err)
			}
			if state == "closed" {
				err := db.WithTx(t.Context(), func(tx pgx.Tx) error {
					if _, err := tx.Exec(t.Context(), `INSERT INTO payment_close_operations(payment_id,invoice_id,user_id,actor_id,provider,provider_payment_id,amount_cents,expected_version,idempotency_key,request_hash,operation_id,channel_instance_id,credential_sha256,status,completed_at) VALUES($1,$2,$3,$3,$4,$5,12000,1,$5,repeat('a',64),$5,'',encode(sha256(''::bytea),'hex'),'closed',clock_timestamp())`, p.PaymentID, p.InvoiceID, f.owner, p.Provider, p.ProviderPaymentID); err != nil {
						return err
					}
					_, err := tx.Exec(t.Context(), `UPDATE payment_transactions SET status='closed' WHERE id=$1`, p.PaymentID)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			f.payments[name] = p
		}
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return f
}

func administratorPaymentFacts(t *testing.T, f administratorPaymentFixture) string {
	t.Helper()
	var raw string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
  'users',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM users x),
  'administrators',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM administrators x),
  'payments',(SELECT jsonb_agg(to_jsonb(x)-'next_reconcile_at' ORDER BY id) FROM payment_transactions x),
  'orders',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM orders x),
  'invoices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),
  'services',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM services x),
  'ledger',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM balance_ledger x),
  'topups',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM balance_topups x),
  'settlements',(SELECT jsonb_agg(to_jsonb(x) ORDER BY topup_id) FROM balance_topup_settlements x),
  'jobs',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM provision_jobs x),
  'email',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM email_outbox x),
  'sms',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM sms_outbox x),
  'notifications',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM notification_events x),
  'deliveries',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM notification_deliveries x),
  'inbox',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM admin_inbox_messages x),
  'anomalies',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM payment_close_anomalies x),
  'commissions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM affiliate_commission_entries x)
 )::text`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func administratorPaymentEvidence(t *testing.T, f administratorPaymentFixture, event string) (int, string, string) {
	t.Helper()
	var n int
	var state, raw string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM audit_events WHERE action='payment.legacy_administrator.review_required' AND payload->>'event_id'=$1),(SELECT status FROM payment_webhook_events WHERE provider='epay_alipay' AND event_id=$1),COALESCE((SELECT payload::text FROM audit_events WHERE action='payment.legacy_administrator.review_required' AND payload->>'event_id'=$1),'')`, event).Scan(&n, &state, &raw); err != nil {
		t.Fatal(err)
	}
	return n, state, raw
}

func TestAdministratorHistoricalPaymentUpgradeCallbacksOnlyRecordEvidence(t *testing.T) {
	f := historicalAdministratorPayments(t)
	before := administratorPaymentFacts(t, f)
	for _, kind := range []string{"order", "topup"} {
		for _, state := range []string{"pending", "closed", "succeeded"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				p := f.payments[kind+"-"+state]
				event := "historical-callback-" + kind + "-" + state
				raw := []byte(`{"private_provider_field":"never-expose-callback-secret"}`)
				external := "receipt-" + p.ProviderPaymentID
				out, err := f.db.ProcessPaymentWebhook(t.Context(), p.Provider, event, p.ProviderPaymentID, p.InvoiceID, p.AmountCents, "succeeded", raw, external)
				if err != nil || out.Status != "review_required" || out.Replayed {
					t.Fatalf("result=%+v err=%v", out, err)
				}
				out, err = f.db.ProcessPaymentWebhook(t.Context(), p.Provider, event, p.ProviderPaymentID, p.InvoiceID, p.AmountCents, "succeeded", raw, external)
				if err != nil || out.Status != "review_required" || !out.Replayed {
					t.Fatalf("replay=%+v err=%v", out, err)
				}
				n, savedState, evidence := administratorPaymentEvidence(t, f, event)
				var payload map[string]any
				if err := json.Unmarshal([]byte(evidence), &payload); err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(raw)
				if n != 1 || savedState != "ignored" || payload["manual_review"] != true || payload["provider_status"] != "succeeded" || payload["amount_cents"] != float64(p.AmountCents) || payload["payload_sha256"] != hex.EncodeToString(hash[:]) || payload["external_transaction_id"] != external || strings.Contains(evidence, "never-expose") {
					t.Fatalf("unsafe/incomplete evidence: n=%d state=%s payload=%s", n, savedState, evidence)
				}
				var stopped bool
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT next_reconcile_at IS NULL FROM payment_transactions WHERE id=$1`, p.PaymentID).Scan(&stopped); err != nil || !stopped {
					t.Fatal("automatic queries not stopped", err)
				}
				if administratorPaymentFacts(t, f) != before {
					t.Fatal("provider observation changed historical business facts")
				}
			})
		}
	}
	// Failed provider observations are evidence too, without closing or mutating
	// the historical payment and without attempting customer settlement.
	p := f.payments["order-pending"]
	out, err := f.db.ProcessPaymentWebhook(t.Context(), p.Provider, "historical-failed", p.ProviderPaymentID, p.InvoiceID, p.AmountCents, "failed", []byte(`{"failed":true}`))
	if err != nil || out.Status != "review_required" || administratorPaymentFacts(t, f) != before {
		t.Fatalf("failed observation result=%+v err=%v", out, err)
	}
}

func TestAdministratorHistoricalPaymentValidationAndAuditRollback(t *testing.T) {
	f := historicalAdministratorPayments(t)
	p := f.payments["order-pending"]
	before := administratorPaymentFacts(t, f)
	for _, test := range []struct {
		name, reference string
		invoice, amount int64
		external        []string
		code            string
	}{
		{"wrong_amount", p.ProviderPaymentID, p.InvoiceID, p.AmountCents + 1, []string{"receipt"}, "payment_amount_mismatch"},
		{"wrong_invoice", p.ProviderPaymentID, f.payments["topup-pending"].InvoiceID, p.AmountCents, []string{"receipt"}, "payment_amount_mismatch"},
		{"empty_external", p.ProviderPaymentID, p.InvoiceID, p.AmountCents, []string{""}, "invalid_payment_webhook"},
		{"long_external", p.ProviderPaymentID, p.InvoiceID, p.AmountCents, []string{strings.Repeat("x", 161)}, "invalid_payment_webhook"},
		{"multiple_external", p.ProviderPaymentID, p.InvoiceID, p.AmountCents, []string{"a", "b"}, "invalid_payment_webhook"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := f.db.ProcessPaymentWebhook(t.Context(), p.Provider, test.name, test.reference, test.invoice, test.amount, "succeeded", []byte(`{"validation":true}`), test.external...)
			if conflictCode(t, err) != test.code {
				t.Fatal(err)
			}
		})
	}
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), p.Provider, "unknown-ref", "unknown", p.InvoiceID, p.AmountCents, "succeeded", []byte(`{}`)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("unknown transaction accepted", err)
	}
	paid := f.payments["order-succeeded"]
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), paid.Provider, "wrong-external", paid.ProviderPaymentID, paid.InvoiceID, paid.AmountCents, "succeeded", []byte(`{}`), "another-receipt"); conflictCode(t, err) != "payment_transaction_mismatch" {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_legacy_payment_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.legacy_administrator.review_required' THEN RAISE EXCEPTION 'isolated audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_legacy_payment_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_legacy_payment_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), p.Provider, "audit-rollback", p.ProviderPaymentID, p.InvoiceID, p.AmountCents, "succeeded", []byte(`{"private":"must-not-persist"}`), "receipt"); err == nil {
		t.Fatal("audit failure was ignored")
	}
	var events, audits int
	var due bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM payment_webhook_events),(SELECT count(*) FROM audit_events WHERE action='payment.legacy_administrator.review_required'),next_reconcile_at IS NOT NULL FROM payment_transactions WHERE id=$1`, p.PaymentID).Scan(&events, &audits, &due); err != nil || events != 0 || audits != 0 || !due || administratorPaymentFacts(t, f) != before {
		t.Fatal("rejected observation left partial state", events, audits, due, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `DROP TRIGGER reject_legacy_payment_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), p.Provider, "hash-conflict", p.ProviderPaymentID, p.InvoiceID, p.AmountCents, "succeeded", []byte(`{"version":1}`), "receipt"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), p.Provider, "hash-conflict", p.ProviderPaymentID, p.InvoiceID, p.AmountCents, "succeeded", []byte(`{"version":2}`), "receipt"); conflictCode(t, err) != "webhook_replay_mismatch" {
		t.Fatal(err)
	}
	n, _, _ := administratorPaymentEvidence(t, f, "hash-conflict")
	if n != 1 || administratorPaymentFacts(t, f) != before {
		t.Fatal("conflicting replay changed evidence or business facts")
	}
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), p.Provider, "hash-conflict", p.ProviderPaymentID, p.InvoiceID, p.AmountCents+1, "succeeded", []byte(`{"version":1}`), "receipt"); conflictCode(t, err) != "payment_amount_mismatch" {
		t.Fatal("matching replay hash bypassed transaction validation", err)
	}
}

func TestAdministratorHistoricalPaymentAutomaticQueriesRequireManualReview(t *testing.T) {
	f := historicalAdministratorPayments(t)
	before := administratorPaymentFacts(t, f)
	for i, status := range []string{"pending", "succeeded"} {
		p := f.payments[[]string{"order-pending", "topup-pending"}[i]]
		check := PaymentCheck{PaymentID: p.PaymentID, InvoiceID: p.InvoiceID, UserID: f.owner, Provider: p.Provider, ProviderPaymentID: p.ProviderPaymentID, AmountCents: p.AmountCents, automatic: true}
		if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO payment_reconciliation_checks(payment_id,actor_id,source) VALUES($1,NULL,'worker') RETURNING id`, p.PaymentID).Scan(&check.ID); err != nil {
			t.Fatal(err)
		}
		out, err := f.db.CompletePaymentCheck(t.Context(), check, payment.QueryResult{ProviderPaymentID: p.ProviderPaymentID, ExternalTransactionID: fmt.Sprintf("auto-receipt-%d", i), AmountCents: p.AmountCents, Status: status})
		if err != nil || out.Status != "review_required" {
			t.Fatalf("query %s result=%+v err=%v", status, out, err)
		}
		var stopped, complete bool
		var state, code string
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT p.next_reconcile_at IS NULL,c.status,c.result_code,c.completed_at IS NOT NULL FROM payment_transactions p JOIN payment_reconciliation_checks c ON c.payment_id=p.id WHERE c.id=$1`, check.ID).Scan(&stopped, &state, &code, &complete); err != nil || !stopped || !complete || state != "error" || code != "manual_review_required" {
			t.Fatal("query falsely completed as settlement or kept polling", stopped, state, code, complete, err)
		}
	}
	if administratorPaymentFacts(t, f) != before {
		t.Fatal("automatic query changed financial history")
	}
	// Expired leases cannot persist a provider observation or stop another check.
	p := f.payments["order-closed"]
	check := PaymentCheck{PaymentID: p.PaymentID, InvoiceID: p.InvoiceID, UserID: f.owner, Provider: p.Provider, ProviderPaymentID: p.ProviderPaymentID, AmountCents: p.AmountCents, automatic: true}
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO payment_reconciliation_checks(payment_id,actor_id,source,lease_until) VALUES($1,NULL,'worker',clock_timestamp()-interval '1 second') RETURNING id`, p.PaymentID).Scan(&check.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.CompletePaymentCheck(t.Context(), check, payment.QueryResult{ProviderPaymentID: p.ProviderPaymentID, ExternalTransactionID: "expired-query", AmountCents: p.AmountCents, Status: "pending"}); conflictCode(t, err) != "payment_check_expired" {
		t.Fatal(err)
	}
	var events int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM payment_webhook_events WHERE event_id='query:expired-query'`).Scan(&events); err != nil || events != 0 {
		t.Fatal("expired query persisted evidence", events, err)
	}
	// A failed audit must also roll back the automatic check outcome and its
	// scheduling change, so the original lease can still be retried safely.
	p = f.payments["topup-closed"]
	check = PaymentCheck{PaymentID: p.PaymentID, InvoiceID: p.InvoiceID, UserID: f.owner, Provider: p.Provider, ProviderPaymentID: p.ProviderPaymentID, AmountCents: p.AmountCents, automatic: true}
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO payment_reconciliation_checks(payment_id,actor_id,source) VALUES($1,NULL,'worker') RETURNING id`, p.PaymentID).Scan(&check.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_legacy_check_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.reconciliation.completed' THEN RAISE EXCEPTION 'isolated audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_legacy_check_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_legacy_check_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.CompletePaymentCheck(t.Context(), check, payment.QueryResult{ProviderPaymentID: p.ProviderPaymentID, ExternalTransactionID: "rollback-query", AmountCents: p.AmountCents, Status: "pending"}); err == nil {
		t.Fatal("query ignored its completion audit failure")
	}
	var state, code string
	var due bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT c.status,c.result_code,p.next_reconcile_at IS NOT NULL,(SELECT count(*) FROM payment_webhook_events WHERE event_id='query:rollback-query') FROM payment_reconciliation_checks c JOIN payment_transactions p ON p.id=c.payment_id WHERE c.id=$1`, check.ID).Scan(&state, &code, &due, &events); err != nil || state != "checking" || code != "" || !due || events != 0 || administratorPaymentFacts(t, f) != before {
		t.Fatal("failed query left partial evidence/outcome", state, code, due, events, err)
	}
}
