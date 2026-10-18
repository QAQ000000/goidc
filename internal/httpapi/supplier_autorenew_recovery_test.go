package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
)

// supplierRenewalClock lets auto-renewal fixtures describe an actually near-due
// upstream resource, rather than only ageing the local copy of its deadline.
// The production account API cannot change endpoint identity; this override is
// confined to an isolated fixture via its existing identity-write helper.
func supplierRenewalClock(t *testing.T, f *supplierFixture, before time.Time) time.Time {
	t.Helper()
	after, _ := supplierRenewalClockServer(t, f, before)
	return after
}

func supplierRenewalClockServer(t *testing.T, f *supplierFixture, before time.Time) (time.Time, *httptest.Server) {
	t.Helper()
	before = before.UTC().Truncate(time.Second)
	after := before.In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, 1, 0).UTC()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/host/header" && r.URL.Path != "/host/renewpage" {
			f.remote.Config.Handler.ServeHTTP(w, r)
			return
		}
		response := httptest.NewRecorder()
		f.remote.Config.Handler.ServeHTTP(response, r)
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		data, ok := body["data"].(map[string]any)
		if !ok {
			t.Error("upstream fixture missing data")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		key := "host_data"
		if r.URL.Path == "/host/renewpage" {
			key = "host"
		}
		host, ok := data[key].(map[string]any)
		if !ok {
			t.Error("upstream fixture missing host")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		due := before
		if f.renewalPays.Load() > 0 && !f.renewalDueUnchanged.Load() {
			due = after
		}
		host["nextduedate"] = due.Unix()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(remote.Close)
	supplierFixtureIdentityWrite(t, f.db, "UPDATE supplier_accounts SET base_url=$2,config=jsonb_set(config,'{endpoint}',to_jsonb($2::text)),identity_key='' WHERE id=$1", f.supplierID, remote.URL)
	return after, remote
}

func supplierArmNearDue(t *testing.T, f *supplierFixture, sid int64, due time.Time) int64 {
	t.Helper()
	var generation int64
	if err := f.db.Pool.QueryRow(t.Context(), "UPDATE services SET auto_renew=true,next_due_at=$2 WHERE id=$1 RETURNING renewal_generation", sid, due.UTC().Truncate(time.Second)).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	return generation
}

func supplierRenewalEvents(t *testing.T, f *supplierFixture, sid int64, want int) {
	t.Helper()
	var events, invoices int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*),count(DISTINCT invoice_id) FROM notification_events WHERE kind='service.renewed' AND service_id=$1`, sid).Scan(&events, &invoices); err != nil {
		t.Fatal(err)
	}
	if events != want || invoices != want {
		t.Fatalf("renewal notification facts=%d invoices=%d want=%d", events, invoices, want)
	}
}

func TestSupplierAutoRenewalRefreshesStaleDeadlineWithoutCharging(t *testing.T) {
	f, sid := activeSupplierRenewalFixture(t)
	actualDue := time.Now().UTC().Add(45 * 24 * time.Hour).Truncate(time.Second)
	supplierRenewalClock(t, f, actualDue)
	supplierArmNearDue(t, f, sid, time.Now().UTC().Add(time.Hour))
	balance := autoRenewBalance(t, f.db, f.uid)
	for range 2 {
		if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 0 {
			t.Fatalf("future upstream period was charged: count=%d err=%v", n, err)
		}
	}
	paid, unpaid, total := autoRenewInvoiceStats(t, f.db, sid)
	due, message, enabled := autoRenewState(t, f.db, sid)
	if paid != 0 || unpaid != 0 || total != 0 || autoRenewBalance(t, f.db, f.uid) != balance || f.renews.Load() != 0 || f.renewalPays.Load() != 0 {
		t.Fatalf("stale local deadline created collection: paid=%d unpaid=%d total=%d renew=%d pay=%d", paid, unpaid, total, f.renews.Load(), f.renewalPays.Load())
	}
	if !due.Equal(actualDue) || message != "" || !enabled {
		t.Fatalf("upstream deadline not refreshed cleanly: due=%s want=%s error=%q enabled=%v", due, actualDue, message, enabled)
	}
	supplierRenewalEvents(t, f, sid, 0)
	// An explicit customer decision may still buy another period early.
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", sid), map[string]any{}, f.customer, "manual-early-after-stale-refresh")
	expect(t, w, 201)
	if invoice := int64(data(t, w)["invoice_id"].(float64)); invoice < 1 {
		t.Fatal("explicit early renewal missing invoice")
	}
	if f.renews.Load() != 0 || autoRenewBalance(t, f.db, f.uid) != balance {
		t.Fatal("manual quote spent money")
	}
}

func TestSupplierAutoRenewalInsufficientBalancePreservesOriginalInvoice(t *testing.T) {
	f, sid := activeSupplierRenewalFixture(t)
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	supplierRenewalClock(t, f, due)
	supplierArmNearDue(t, f, sid, due)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=0 WHERE id=$1", f.uid); err != nil {
		t.Fatal(err)
	}
	if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	paid, unpaid, total := autoRenewInvoiceStats(t, f.db, sid)
	currentDue, message, _ := autoRenewState(t, f.db, sid)
	if paid != 0 || unpaid != 1 || total != 1 || !currentDue.Equal(due) || message == "" || autoRenewBalance(t, f.db, f.uid) != 0 {
		t.Fatalf("insufficient-balance state paid=%d unpaid=%d total=%d due=%s error=%q", paid, unpaid, total, currentDue, message)
	}
	var invoice int64
	var jobs int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT i.id,(SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='renew') FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id WHERE ii.service_id=$1 AND ii.item_type='renew'`, sid).Scan(&invoice, &jobs); err != nil || jobs != 0 {
		t.Fatal("failed collection queued renewal", invoice, jobs, err)
	}
	supplierRenewalEvents(t, f, sid, 0)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.uid), map[string]any{"amount_cents": 10000}, f.root, "insufficient-renewal-later-topup"), 201)
	for range 2 {
		if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 0 {
			t.Fatal("unpaid renewal automatically charged after topup", n, err)
		}
	}
	if autoRenewBalance(t, f.db, f.uid) != 10000 || f.renews.Load() != 0 || f.renewalPays.Load() != 0 {
		t.Fatal("topup triggered unrequested retry")
	}
	paid, unpaid, total = autoRenewInvoiceStats(t, f.db, sid)
	if paid != 0 || unpaid != 1 || total != 1 {
		t.Fatal("original unpaid invoice replaced", paid, unpaid, total)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "insufficient-renewal-manual-original-pay"), 201)
	supplierRenewalEvents(t, f, sid, 0)
	job := f.claim(t)
	if job.Action != "renew" {
		t.Fatal(job)
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	status, _, lease := f.state(t, job)
	if status != "succeeded" || lease != nil || f.renews.Load() != 1 || f.renewalPays.Load() != 1 || autoRenewBalance(t, f.db, f.uid) != 7500 {
		t.Fatal("manual original-invoice recovery failed", status, lease, f.renews.Load(), f.renewalPays.Load())
	}
	supplierRenewalEvents(t, f, sid, 1)
}

func TestSupplierAutoRenewalNotificationWaitsForConfirmedPeriod(t *testing.T) {
	for _, frozen := range []bool{false, true} {
		t.Run(fmt.Sprintf("unconfirmed_%t", frozen), func(t *testing.T) {
			f, sid := activeSupplierRenewalFixture(t)
			due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
			after := supplierRenewalClock(t, f, due)
			supplierArmNearDue(t, f, sid, due)
			f.renewalDueUnchanged.Store(frozen)
			if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 0 {
				t.Fatal("pending paid supplier renewal counted again", n, err)
			}
			_, pendingError, _ := autoRenewState(t, f.db, sid)
			if pendingError == "" {
				t.Fatal("pending paid supplier renewal lost its processing reason")
			}
			supplierRenewalEvents(t, f, sid, 0)
			job := f.claim(t)
			if job.Action != "renew" {
				t.Fatal(job)
			}
			var paidFacts int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_events WHERE kind='invoice.paid' AND invoice_id=(SELECT (request_payload->>'local_invoice_id')::bigint FROM provision_jobs WHERE id=$1)`, job.ID).Scan(&paidFacts); err != nil || paidFacts != 1 {
				t.Fatal("local collection lost paid fact", paidFacts, err)
			}
			if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			if frozen {
				supplierRenewalEvents(t, f, sid, 0)
				status, _, lease := f.state(t, job)
				if status != "waiting_verification" || lease == nil || *lease != job.ID {
					t.Fatal("unconfirmed renewal lost freeze", status, lease)
				}
				verify := fmt.Sprintf("/api/v1/admin/services/%d/supplier-verification", sid)
				body := map[string]any{"job_id": job.ID, "expected_attempts": job.Attempts, "upstream_id": 42, "outcome": "delivered", "note": "只读确认实际账单付款与完整续费期限"}
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 409)
				supplierRenewalEvents(t, f, sid, 0)
				_, unknownError, _ := autoRenewState(t, f.db, sid)
				if unknownError != pendingError {
					t.Fatal("unconfirmed or rejected verification cleared automatic error", pendingError, unknownError)
				}
				f.renewalDueUnchanged.Store(false)
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 200)
				// A lost successful response cannot create another lifecycle fact.
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 404)
			}
			supplierRenewalEvents(t, f, sid, 1)
			current, confirmedError, _ := autoRenewState(t, f.db, sid)
			if !current.Equal(after) || confirmedError != "" {
				t.Fatal("confirmed period or cleared automatic error differs", current, after, confirmedError)
			}
			for range 2 {
				if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 0 {
					t.Fatal("confirmed renewal rescanned", n, err)
				}
			}
			if _, err := f.db.EnqueuePeriodicSupplierSyncs(t.Context()); err != nil {
				t.Fatal(err)
			}
			sync := f.claim(t)
			if sync.Action != "sync" {
				t.Fatal(sync)
			}
			if err := f.db.ProcessProvisionJob(t.Context(), *sync); err != nil {
				t.Fatal(err)
			}
			supplierRenewalEvents(t, f, sid, 1)
			if f.renews.Load() != 1 || f.renewalPays.Load() != 1 {
				t.Fatal("notification replay caused another supplier write")
			}
		})
	}
}

func TestSupplierAutoRenewalPaymentRechecksOriginalIntent(t *testing.T) {
	for _, change := range []string{"disabled", "due", "generation"} {
		t.Run(change, func(t *testing.T) {
			f, sid := activeSupplierRenewalFixture(t)
			due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
			supplierRenewalClock(t, f, due)
			generation := supplierArmNearDue(t, f, sid, due)
			invoice, err := f.db.CreateAutoRenewalInvoice(t.Context(), f.uid, sid, due, "auto-intent-recheck-"+change, generation)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "disabled":
				err = f.db.SetAutoRenew(t.Context(), f.uid, sid, false, false)
			case "due":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=next_due_at+interval '1 month' WHERE id=$1", sid)
			case "generation":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET renewal_generation=renewal_generation+1 WHERE id=$1", sid)
			}
			if err != nil {
				t.Fatal(err)
			}
			balance := autoRenewBalance(t, f.db, f.uid)
			_, err = f.db.PayAutoRenewalInvoice(t.Context(), f.uid, sid, invoice.InvoiceID, due, generation, "auto-intent-payment-"+change)
			var c *store.Conflict
			if !errors.As(err, &c) || c.Code != "auto_renewal_race" {
				t.Fatalf("changed %s intent accepted: %v", change, err)
			}
			paid, unpaid, total := autoRenewInvoiceStats(t, f.db, sid)
			var jobs int
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='renew'", sid).Scan(&jobs); err != nil {
				t.Fatal(err)
			}
			if paid != 0 || unpaid != 1 || total != 1 || jobs != 0 || autoRenewBalance(t, f.db, f.uid) != balance || f.renews.Load() != 0 || f.renewalPays.Load() != 0 {
				t.Fatalf("invalid automatic intent spent money: paid=%d unpaid=%d total=%d jobs=%d", paid, unpaid, total, jobs)
			}
			supplierRenewalEvents(t, f, sid, 0)
		})
	}
}

func TestAutoRenewalPaymentRejectsCancellationApprovedAfterCreation(t *testing.T) {
	f := newCancellationRenewalFixture(t)
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	var generation int64
	if err := f.db.Pool.QueryRow(t.Context(), "UPDATE services SET auto_renew=true,next_due_at=$2 WHERE id=$1 RETURNING renewal_generation", f.serviceID, due).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	invoice, err := f.db.CreateAutoRenewalInvoice(t.Context(), f.userID, f.serviceID, due, "auto-payment-before-cancellation", generation)
	if err != nil {
		t.Fatal(err)
	}
	id := f.cancel(t, "end_of_billing")
	expect(t, f.approve(id), 200)
	before := f.state(t, invoice.InvoiceID)
	_, err = f.db.PayAutoRenewalInvoice(t.Context(), f.userID, f.serviceID, invoice.InvoiceID, due, generation, "auto-payment-after-cancellation")
	var c *store.Conflict
	if !errors.As(err, &c) || c.Code != "service_cancellation_approved" {
		t.Fatal("approved cancellation accepted automatic payment", err)
	}
	f.unchanged(t, invoice.InvoiceID, before)
}

func TestSupplierAutoRenewalRejectsManualInvoiceKeyCollision(t *testing.T) {
	f, sid := activeSupplierRenewalFixture(t)
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	supplierRenewalClock(t, f, due)
	generation := supplierArmNearDue(t, f, sid, due)
	key := fmt.Sprintf("auto-renew-%d-%s", sid, due.Format("20060102150405"))
	if generation > 0 {
		key += fmt.Sprintf("-g%d", generation)
	}
	manual, err := f.db.CreateRenewalInvoice(t.Context(), f.uid, sid, key)
	if err != nil {
		t.Fatal(err)
	}
	balance := autoRenewBalance(t, f.db, f.uid)
	_, err = f.db.CreateAutoRenewalInvoice(t.Context(), f.uid, sid, due, key, generation)
	var c *store.Conflict
	if !errors.As(err, &c) || c.Code != "auto_renewal_invoice_source_conflict" {
		t.Fatal("manual invoice replay became an automatic source", err)
	}
	_, err = f.db.PayAutoRenewalInvoice(t.Context(), f.uid, sid, manual.InvoiceID, due, generation, "auto-pay-"+key)
	if !errors.As(err, &c) || c.Code != "auto_renewal_invoice_source_conflict" {
		t.Fatal("manual invoice paid through automatic admission", err)
	}
	paid, unpaid, total := autoRenewInvoiceStats(t, f.db, sid)
	if paid != 0 || unpaid != 1 || total != 1 || autoRenewBalance(t, f.db, f.uid) != balance || f.renews.Load() != 0 || f.renewalPays.Load() != 0 {
		t.Fatal("manual-key collision collected money", paid, unpaid, total)
	}
}

func TestSupplierAutoRenewalSourceDatabaseGuards(t *testing.T) {
	f, sid := activeSupplierRenewalFixture(t)
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	supplierRenewalClock(t, f, due)
	generation := supplierArmNearDue(t, f, sid, due)
	invoice, err := f.db.CreateAutoRenewalInvoice(t.Context(), f.uid, sid, due, "auto-source-database-guards", generation)
	if err != nil {
		t.Fatal(err)
	}
	var candidate, otherUser int64
	if err = f.db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,idempotency_key) VALUES('guard-candidate-'||gen_random_uuid()::text,$1,'unpaid',2500,'auto-source-guard-candidate') RETURNING id`, f.uid).Scan(&candidate); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) VALUES($1,'source association fixture','renew',2500,$2)`, candidate, sid); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT id FROM users WHERE id<>$1 ORDER BY id LIMIT 1", f.uid).Scan(&otherUser); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var out string
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'source',(SELECT jsonb_agg(to_jsonb(r) ORDER BY invoice_id) FROM auto_renewal_invoice_sources r WHERE service_id=$1),
 'balance',(SELECT balance_cents FROM users WHERE id=$2),
 'ledger',(SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM balance_ledger l WHERE user_id=$2),
 'invoices',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invoices i WHERE user_id=$2),
 'service',(SELECT to_jsonb(s) FROM services s WHERE id=$1),
 'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM provision_jobs j WHERE service_id=$1))::text`, sid, f.uid).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot()
	for _, mismatch := range []struct {
		name       string
		user       int64
		expected   time.Time
		generation int64
	}{
		{"customer", otherUser, due, generation},
		{"deadline", f.uid, due.Add(time.Hour), generation},
		{"generation", f.uid, due, generation + 1},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			_, err := f.db.Pool.Exec(t.Context(), `INSERT INTO auto_renewal_invoice_sources(invoice_id,service_id,user_id,expected_due,generation) VALUES($1,$2,$3,$4,$5)`, candidate, sid, mismatch.user, mismatch.expected, mismatch.generation)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "automatic renewal invoice source association mismatch" {
				t.Fatal("mismatched automatic source accepted or wrong guard", err)
			}
			if snapshot() != before {
				t.Fatal("rejected automatic source changed finance or original source")
			}
		})
	}
	for _, statement := range []string{
		"UPDATE auto_renewal_invoice_sources SET expected_due=expected_due+interval '1 second' WHERE invoice_id=$1",
		"DELETE FROM auto_renewal_invoice_sources WHERE invoice_id=$1",
	} {
		_, err := f.db.Pool.Exec(t.Context(), statement, invoice.InvoiceID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.Message != "automatic renewal invoice source is immutable" {
			t.Fatal("automatic source update/delete accepted or wrong guard", err)
		}
		if snapshot() != before {
			t.Fatal("rejected source mutation changed finance or original source")
		}
	}
	if f.renews.Load() != 0 || f.renewalPays.Load() != 0 {
		t.Fatal("database guards triggered supplier mutation")
	}
}
