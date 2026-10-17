package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// Hold only the service row as a barrier. The real cancellation must acquire
// the invoice and then wait for this row; callbacks must wait for that invoice,
// rather than letting the association trigger mistake NOWAIT contention for a
// refund hold. No sleep determines the lock interleaving and no guard is removed.
func TestEpayUpgradeCallbackWaitsForConcurrentCancellation(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	var version int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version < 180000 || version >= 190000 {
		t.Fatal("requires isolated PostgreSQL 18", err)
	}
	var logs bytes.Buffer
	f.h = New(f.db, epayTestConfig(), slog.New(slog.NewTextHandler(&logs, nil))).Handler()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	handler := f.h
	f.h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r.WithContext(ctx)) })
	change := f.submit(t, f.quote(t, f.large), "epay-barrier-upgrade")
	invoice, changeID := int64(change["invoice_id"].(float64)), int64(change["id"].(float64))
	intent := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": "epay_alipay", "expected_version": 1}, f.customer, "epay-barrier-intent")
	expect(t, intent, 201)
	u, err := url.Parse(data(t, intent)["checkout_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	callback := epayNotification(u.Query(), "barrier-trade")
	var beforeBalance int64
	if err = f.db.Pool.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&beforeBalance); err != nil {
		t.Fatal(err)
	}
	// Reserve the observer before filling the pool with callback requests.
	observer, err := f.db.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Release()
	barrier, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback(context.Background())
	var barrierPID int
	if err = barrier.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&barrierPID); err != nil {
		t.Fatal(err)
	}
	if _, err = barrier.Exec(ctx, "SELECT id FROM services WHERE id=$1 FOR UPDATE", f.sid); err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan int, 1)
	go func() {
		cancelled <- request(f.h, "DELETE", fmt.Sprintf("/api/v1/services/%d/configuration-changes/%d", f.sid, changeID), nil, f.customer, "").Code
	}()
	// Observe the exact blocking edge, not query strings or a timing guess.
	waiter := func(blocker int) int {
		t.Helper()
		for {
			var pid int
			err := observer.QueryRow(ctx, `SELECT coalesce(min(pid),0) FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid))`, blocker).Scan(&pid)
			if err != nil {
				t.Fatal("lock barrier observation failed", err)
			}
			if pid != 0 {
				return pid
			}
			select {
			case <-ctx.Done():
				t.Fatal("expected lock barrier was not reached")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	cancellationPID := waiter(barrierPID)
	const callbackCount = 8
	responses := make(chan *httptest.ResponseRecorder, callbackCount)
	for range callbackCount {
		go func() { responses <- epayCallback(f.h, "GET", "epay_alipay", callback) }()
	}
	var received []*httptest.ResponseRecorder
	// The first real callback must block on the cancelling transaction. A 500
	// before we release the service barrier deterministically exposes the bug.
	for {
		var blocked bool
		if err = observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, cancellationPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case response := <-responses:
			received = append(received, response)
		case <-ctx.Done():
			t.Fatal("callback did not wait for invoice holder")
		case <-time.After(10 * time.Millisecond):
			continue
		}
		if len(received) > 0 {
			break
		}
	}
	if err = barrier.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-cancelled:
		if code != 409 {
			t.Errorf("real cancellation must retain pending-payment guard, got %d", code)
		}
	case <-ctx.Done():
		t.Fatal("real cancellation did not finish")
	}
	for len(received) < callbackCount {
		select {
		case response := <-responses:
			received = append(received, response)
		case <-ctx.Done():
			t.Fatal("concurrent callbacks did not finish")
		}
	}
	for _, response := range received {
		if response.Code != 200 || response.Body.String() != "success" {
			// All handlers have completed, so the shared slog buffer is quiescent.
			t.Fatalf("concurrent notification rejected %d %s; settlement diagnostic: %s", response.Code, response.Body.String(), logs.String())
		}
	}
	var valid bool
	err = f.db.Pool.QueryRow(ctx, `SELECT i.status='paid' AND i.paid_at IS NOT NULL AND c.status='queued'
 AND p.status='succeeded' AND p.external_transaction_id=(SELECT event_id FROM payment_webhook_events)
 AND p.raw_result->'trade_no'->>0='barrier-trade'
 AND (SELECT count(*) FROM payment_webhook_events)=1
 AND (SELECT count(*) FROM audit_events WHERE action='payment.webhook.succeeded' AND entity_id=i.id::text)=1
 AND (SELECT count(*) FROM provision_jobs WHERE service_id=c.service_id AND action='upgrade' AND status='pending' AND attempts=0)=1
 AND (SELECT balance_cents FROM users WHERE id=i.user_id)=$2
 AND NOT EXISTS(SELECT 1 FROM external_refund_order_holds)
 FROM invoices i JOIN service_configuration_changes c ON c.invoice_id=i.id JOIN payment_transactions p ON p.invoice_id=i.id WHERE i.id=$1`, invoice, beforeBalance).Scan(&valid)
	if err != nil || !valid {
		t.Fatal("callback/cancel conservation failed", err)
	}
}
