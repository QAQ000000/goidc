package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func monitoringGateways(t *testing.T, db *store.Store, paymentID int64) map[string]payment.QueryGateway {
	t.Helper()
	var checkout string
	if err := db.Pool.QueryRow(t.Context(), "SELECT checkout_url FROM payment_transactions WHERE id=$1", paymentID).Scan(&checkout); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(checkout)
	if err != nil {
		t.Fatal(err)
	}
	g := payment.EpayGateway{EpayConfig: payment.EpayConfig{APIURL: u.Scheme + "://" + u.Host, PublicOrigin: "http://localhost:3088", PID: "10001", Key: "integration-epay-secret", Types: []string{"alipay"}}, Type: "alipay"}
	return map[string]payment.QueryGateway{g.Name(): g}
}

func makeMonitoringDue(t *testing.T, db *store.Store, paymentID int64) {
	t.Helper()
	if _, err := db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET next_reconcile_at=clock_timestamp()-interval '1 minute' WHERE id=$1", paymentID); err != nil {
		t.Fatal(err)
	}
	clearQueryCooldown(t, db)
}

func TestAutomaticPaymentMonitoringSuccessAndLateCallback(t *testing.T) {
	var calls atomic.Int64
	var checkout url.Values
	f, invoiceID, paymentID, v := reconciliationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("out_trade_no") != checkout.Get("out_trade_no") {
			t.Error("wrong payment queried")
		}
		_ = json.NewEncoder(w).Encode(queryProtocolResponse(checkout, 1))
	})
	checkout = v
	gateways := monitoringGateways(t, f.db, paymentID)
	if worked, err := f.db.ProcessAutomaticPaymentCheck(t.Context(), gateways); err != nil || worked || calls.Load() != 0 {
		t.Fatal("queried before schedule", worked, err, calls.Load())
	}
	makeMonitoringDue(t, f.db, paymentID)
	// A disabled customer's valid payment must still settle, just like a callback.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1; UPDATE site_settings SET maintenance_enabled=true; UPDATE payment_transactions SET expires_at=now()-interval '1 day'", pgx.QueryExecModeSimpleProtocol, f.uid); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.db.ProcessAutomaticPaymentCheck(t.Context(), gateways); err != nil || !worked {
		t.Fatal("confirmed automatic payment failed", worked, err)
	}
	var state, source string
	var next *time.Time
	var actor *int64
	var debits, completions int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT i.status,p.next_reconcile_at,c.source,c.actor_id,
 (SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=i.id),
 (SELECT count(*) FROM audit_events WHERE action='payment.reconciliation.completed' AND actor_id IS NULL)
 FROM invoices i JOIN payment_transactions p ON p.invoice_id=i.id JOIN payment_reconciliation_checks c ON c.payment_id=p.id WHERE i.id=$1`, invoiceID).
		Scan(&state, &next, &source, &actor, &debits, &completions); err != nil || state != "paid" || next != nil || source != "worker" || actor != nil || debits != 0 || completions != 1 {
		t.Fatal("incorrect automatic settlement", state, next, source, actor, debits, completions, err)
	}
	w := epayCallback(f.h, "POST", "epay_alipay", epayNotification(v, "query-trade"))
	if w.Code != 200 || w.Body.String() != "success" {
		t.Fatal("late callback rejected", w.Code, w.Body.String())
	}
	if worked, err := f.db.ProcessAutomaticPaymentCheck(t.Context(), gateways); err != nil || worked || calls.Load() != 1 {
		t.Fatal("settled payment queried again", worked, err, calls.Load())
	}
}

func TestAutomaticPaymentMonitoringLeaseAndCallbackWins(t *testing.T) {
	f, invoiceID, paymentID, v := reconciliationFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("claim contacted provider") })
	makeMonitoringDue(t, f.db, paymentID)
	check, err := f.db.ClaimAutomaticPaymentCheck(t.Context(), []string{"epay_alipay"})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", queryPath(invoiceID, paymentID, false), nil, f.customer, ""), 409)
	var wg sync.WaitGroup
	errorsCh := make(chan error, 6)
	for i := 0; i < cap(errorsCh); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.db.ClaimAutomaticPaymentCheck(t.Context(), []string{"epay_alipay"})
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal("multiple workers claimed same payment", err)
		}
	}
	callback := epayCallback(f.h, "GET", "epay_alipay", epayNotification(v, "query-trade"))
	if callback.Code != 200 {
		t.Fatal("callback blocked by query lease", callback.Code, callback.Body.String())
	}
	result, err := f.db.CompletePaymentCheck(t.Context(), check, payment.QueryResult{ProviderPaymentID: v.Get("out_trade_no"), ExternalTransactionID: "10001:query-trade", AmountCents: check.AmountCents, Status: "pending"})
	if err != nil || result.Status != "succeeded" {
		t.Fatal("pending query overwrote callback", result, err)
	}
}

func TestAutomaticPaymentMonitoringExpiredLeaseFencingAndRecovery(t *testing.T) {
	f, invoiceID, paymentID, v := reconciliationFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("claim contacted provider") })
	makeMonitoringDue(t, f.db, paymentID)
	old, err := f.db.ClaimAutomaticPaymentCheck(t.Context(), []string{"epay_alipay"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE payment_reconciliation_checks SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", old.ID); err != nil {
		t.Fatal(err)
	}
	verified := payment.QueryResult{ProviderPaymentID: v.Get("out_trade_no"), ExternalTransactionID: "10001:query-trade", AmountCents: old.AmountCents, Status: "succeeded"}
	if _, err := f.db.CompletePaymentCheck(t.Context(), old, verified); err == nil {
		t.Fatal("expired lease settled payment")
	}
	makeMonitoringDue(t, f.db, paymentID)
	current, err := f.db.ClaimAutomaticPaymentCheck(t.Context(), []string{"epay_alipay"})
	if err != nil || current.ID == old.ID {
		t.Fatal("expired lease not reclaimed", current, err)
	}
	if _, err := f.db.CompletePaymentCheck(t.Context(), old, verified); err == nil {
		t.Fatal("superseded query settled payment")
	}
	if err := f.db.FailPaymentCheck(t.Context(), old, "provider_unavailable"); err != nil {
		t.Fatal(err)
	}
	var active, expired int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FILTER(WHERE status='checking'),count(*) FILTER(WHERE result_code='lease_expired') FROM payment_reconciliation_checks").Scan(&active, &expired); err != nil || active != 1 || expired != 1 {
		t.Fatal("stale completion changed active lease", active, expired, err)
	}
	if _, err := f.db.CompletePaymentCheck(t.Context(), current, verified); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", invoiceID).Scan(&state); err != nil || state != "paid" {
		t.Fatal("recovered query did not settle", state, err)
	}
}

func TestAutomaticPaymentMonitoringBackoffExhaustionAndManualRestart(t *testing.T) {
	var checkout url.Values
	f, invoiceID, paymentID, v := reconciliationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(queryProtocolResponse(checkout, 0))
	})
	checkout = v
	gateways := monitoringGateways(t, f.db, paymentID)
	for _, sample := range []struct{ previous, delay int }{{0, 300}, {3, 900}, {6, 3600}, {12, 21600}, {31, 0}} {
		makeMonitoringDue(t, f.db, paymentID)
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET auto_reconcile_attempts=$2,expires_at=now()-interval '1 day' WHERE id=$1", paymentID, sample.previous); err != nil {
			t.Fatal(err)
		}
		if worked, err := f.db.ProcessAutomaticPaymentCheck(t.Context(), gateways); err != nil || !worked {
			t.Fatal("pending query failed", worked, err)
		}
		var seconds *float64
		var attempts int
		var state string
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT auto_reconcile_attempts,extract(epoch FROM next_reconcile_at-clock_timestamp())::float8,status FROM payment_transactions WHERE id=$1`, paymentID).Scan(&attempts, &seconds, &state); err != nil || attempts != sample.previous+1 || state != "pending" {
			t.Fatal("incorrect attempt state", attempts, state, err)
		}
		if sample.delay == 0 {
			if seconds != nil {
				t.Fatal("exhausted monitor rescheduled")
			}
		} else if seconds == nil || *seconds > float64(sample.delay) || *seconds < float64(sample.delay-5) {
			t.Fatal("incorrect query backoff", sample, seconds)
		}
	}
	if worked, err := f.db.ProcessAutomaticPaymentCheck(t.Context(), gateways); err != nil || worked {
		t.Fatal("exhausted payment queried automatically", worked, err)
	}
	detail := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/invoices/%d", invoiceID), nil, f.root, "")
	expect(t, detail, 200)
	if !strings.Contains(detail.Body.String(), `"reconciliation_exhausted":true`) || !strings.Contains(detail.Body.String(), `"check_source":"worker"`) || strings.Contains(detail.Body.String(), "integration-epay-secret") {
		t.Fatal("missing monitoring status or credential leak", detail.Body.String())
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{"expected_version": 1}, f.customer, "monitor-expired-balance"), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/cancel", invoiceID), map[string]any{"expected_version": 1}, f.root, "monitor-expired-cancel"), 409)
	clearQueryCooldown(t, f.db)
	expect(t, request(f.h, "POST", queryPath(invoiceID, paymentID, false), nil, f.customer, ""), 200)
	var attempts int
	var scheduled bool
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT auto_reconcile_attempts,next_reconcile_at>clock_timestamp() FROM payment_transactions WHERE id=$1", paymentID).Scan(&attempts, &scheduled); err != nil || attempts != 0 || !scheduled {
		t.Fatal("manual check did not restart monitor", attempts, scheduled, err)
	}
}

func TestAutomaticPaymentMonitoringFailuresAndRollback(t *testing.T) {
	for _, mode := range []string{"invalid", "unavailable", "cancelled", "audit"} {
		t.Run(mode, func(t *testing.T) {
			var checkout url.Values
			f, invoiceID, paymentID, v := reconciliationFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "invalid":
					_, _ = w.Write([]byte(`{"code":1,"msg":"integration-epay-secret"}`))
				case "unavailable":
					w.WriteHeader(500)
				case "cancelled":
					<-r.Context().Done()
				default:
					_ = json.NewEncoder(w).Encode(queryProtocolResponse(checkout, 1))
				}
			})
			checkout = v
			makeMonitoringDue(t, f.db, paymentID)
			if mode == "audit" {
				if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION monitor_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.reconciliation.completed' THEN RAISE EXCEPTION 'monitor audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER monitor_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION monitor_reject_audit()`); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			if mode == "cancelled" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 200*time.Millisecond)
			}
			worked, err := f.db.ProcessAutomaticPaymentCheck(ctx, monitoringGateways(t, f.db, paymentID))
			cancel()
			if !worked || err == nil || strings.Contains(err.Error(), "integration-epay-secret") {
				t.Fatal("failure not retained or leaked key", worked, err)
			}
			var state, checkState, code string
			var events int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT i.status,c.status,c.result_code,(SELECT count(*) FROM payment_webhook_events)
 FROM invoices i JOIN payment_transactions p ON p.invoice_id=i.id JOIN payment_reconciliation_checks c ON c.payment_id=p.id WHERE i.id=$1`, invoiceID).Scan(&state, &checkState, &code, &events); err != nil || state != "unpaid" || checkState != "error" || events != 0 {
				t.Fatal("failure changed settlement or left checking lease", state, checkState, code, events, err)
			}
			want := map[string]string{"invalid": "provider_invalid", "unavailable": "provider_unavailable", "cancelled": "provider_unavailable", "audit": "settlement_failed"}[mode]
			if code != want {
				t.Fatal("incorrect sanitized failure", code, want)
			}
		})
	}
}

func TestAutomaticPaymentMonitoringConfiguredProvidersOnly(t *testing.T) {
	f, invoiceID, paymentID, _ := reconciliationFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("disabled provider contacted") })
	makeMonitoringDue(t, f.db, paymentID)
	if _, err := f.db.ClaimAutomaticPaymentCheck(t.Context(), []string{"epay_wxpay", "mock"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("unconfigured provider claimed", err)
	}
	if worked, err := f.db.ProcessAutomaticPaymentCheck(t.Context(), nil); worked || err != nil {
		t.Fatal("unconfigured worker performed query", worked, err)
	}
	cfg := epayTestConfig()
	cfg.EpayTypes = nil
	f.h = New(f.db, cfg, slog.Default()).Handler()
	detail := request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", invoiceID), nil, f.customer, "")
	expect(t, detail, 200)
	if !strings.Contains(detail.Body.String(), `"query_enabled":false`) {
		t.Fatal("unconfigured query shown enabled", detail.Body.String())
	}
}
