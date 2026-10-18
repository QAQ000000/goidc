package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
)

type supplierExpiryMoney struct {
	Invoices, Ledger int
	Balance          int64
}

func supplierExpiryMoneySnapshot(t *testing.T, db *store.Store, userID int64) supplierExpiryMoney {
	t.Helper()
	var got supplierExpiryMoney
	if err := db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM invoices WHERE user_id=$1),
 (SELECT count(*) FROM balance_ledger WHERE user_id=$1),balance_cents FROM users WHERE id=$1`, userID).
		Scan(&got.Invoices, &got.Ledger, &got.Balance); err != nil {
		t.Fatal(err)
	}
	return got
}

func supplierExpiryArm(t *testing.T, f *supplierLifecycleFixture) time.Time {
	t.Helper()
	var due time.Time
	if err := f.db.Pool.QueryRow(t.Context(), `UPDATE services SET next_due_at=date_trunc('second',now()-interval '2 days'),auto_renew=false
 WHERE id=$1 RETURNING next_due_at`, f.sid).Scan(&due); err != nil {
		t.Fatal(err)
	}
	f.remote(func(s *supplier.Service) { s.NextDueDate = fmt.Sprint(due.Unix()) })
	return due
}

func supplierExpiryJob(t *testing.T, db *store.Store, serviceID int64) *store.ProvisionJob {
	t.Helper()
	var j store.ProvisionJob
	if err := db.Pool.QueryRow(t.Context(), `SELECT id,service_id,action,attempts FROM provision_jobs
 WHERE service_id=$1 AND action='suspend' ORDER BY id DESC LIMIT 1`, serviceID).
		Scan(&j.ID, &j.ServiceID, &j.Action, &j.Attempts); err != nil {
		t.Fatal(err)
	}
	return &j
}

func supplierExpirySource(t *testing.T, db *store.Store, j *store.ProvisionJob, due time.Time, manual bool) {
	t.Helper()
	var key string
	var raw []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT idempotency_key,request_payload FROM provision_jobs WHERE id=$1", j.ID).Scan(&key, &raw); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		System bool  `json:"system_expiry_suspension"`
		Due    int64 `json:"expiry_due_unix_micro"`
		Manual bool  `json:"manual_lifecycle"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if key != fmt.Sprintf("expiry-suspend-service-%d-due-%d", j.ServiceID, due.UnixMicro()) || !payload.System || payload.Due != due.UnixMicro() || payload.Manual != manual {
		t.Fatalf("expiry source key=%s payload=%s due=%s manual=%v", key, raw, due, manual)
	}
}

func supplierExpiryMarker(t *testing.T, db *store.Store, serviceID, jobID int64) {
	t.Helper()
	var marker int64
	var billing bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT coalesce((resource_info->>'system_expiry_suspension_job_id')::bigint,0),
 resource_info ? 'system_billing_suspension_job_id' FROM services WHERE id=$1`, serviceID).Scan(&marker, &billing); err != nil {
		t.Fatal(err)
	}
	if marker != jobID || billing {
		t.Fatalf("expiry marker=%d want=%d billing marker=%v", marker, jobID, billing)
	}
}

func TestSupplierExpirySuspendsWithoutCreatingDebtByCapability(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprintf("supported=%t", supported), func(t *testing.T) {
			f := newSupplierLifecycleFixture(t, supported)
			due := supplierExpiryArm(t, f)
			before := supplierExpiryMoneySnapshot(t, f.db, f.uid)
			if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
				t.Fatalf("expiry queued=%d err=%v", n, err)
			}
			j := supplierExpiryJob(t, f.db, f.sid)
			supplierExpirySource(t, f.db, j, due, !supported)
			supplierExpiryMarker(t, f.db, f.sid, 0)
			want := "waiting_manual"
			if supported {
				want = "pending"
			}
			f.check(t, j, want, "active", false)
			if f.calls() != 0 {
				t.Fatal("expiry scan dispatched upstream lifecycle action")
			}
			if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
				t.Fatalf("pending expiry replay queued=%d err=%v", n, err)
			}
			if supported {
				j = f.claim(t)
				if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
					t.Fatal(err)
				}
			} else {
				f.remote(func(s *supplier.Service) {
					s.Status, s.SuspendReason, s.SuspendReasonType = "Suspended", "expired", "due"
				})
				expect(t, f.verify(t, j, f.root), http.StatusOK)
			}
			f.check(t, j, "succeeded", "suspended", false)
			supplierExpiryMarker(t, f.db, f.sid, j.ID)
			if after := supplierExpiryMoneySnapshot(t, f.db, f.uid); after != before {
				t.Fatalf("expiry suspension created debt or spent money: before=%+v after=%+v", before, after)
			}
			if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
				t.Fatalf("confirmed expiry replay queued=%d err=%v", n, err)
			}
			if supported {
				if err := f.db.ProcessProvisionJob(t.Context(), *j); err == nil {
					t.Fatal("completed expiry suspension was processed again")
				}
			} else {
				// Manual lifecycle verification replays the same durable
				// operator receipt successfully without re-reading or writing.
				expect(t, f.verify(t, j, f.root), http.StatusOK)
				f.check(t, j, "succeeded", "suspended", false)
				supplierExpiryMarker(t, f.db, f.sid, j.ID)
				var receipts int
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events
 WHERE action='supplier.lifecycle.manual_verified' AND entity_type='service' AND entity_id=$1`, fmt.Sprint(f.sid)).Scan(&receipts); err != nil || receipts != 1 {
					t.Fatalf("manual expiry verification receipts=%d err=%v", receipts, err)
				}
			}
			wantCalls := 0
			if supported {
				wantCalls = 1
			}
			if f.calls() != wantCalls {
				t.Fatalf("expiry upstream writes=%d want=%d", f.calls(), wantCalls)
			}
		})
	}
}

func TestSupplierExpiryHonorsGraceAndServiceEligibility(t *testing.T) {
	for _, change := range []string{"grace", "future", "no_deadline", "once", "terminated", "cancelled", "failed", "suspended", "import_review", "approved_cancellation", "busy"} {
		t.Run(change, func(t *testing.T) {
			f := newSupplierLifecycleFixture(t, true)
			supplierExpiryArm(t, f)
			var err error
			switch change {
			case "grace":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=now()-interval '23 hours' WHERE id=$1", f.sid)
			case "future":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=now()+interval '1 hour' WHERE id=$1", f.sid)
			case "no_deadline":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=NULL WHERE id=$1", f.sid)
			case "once":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET billing_cycle='once' WHERE id=$1", f.sid)
			case "import_review":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET import_review_required=true WHERE id=$1", f.sid)
			case "approved_cancellation":
				_, err = f.db.Pool.Exec(t.Context(), `INSERT INTO service_cancellation_requests(service_id,user_id,type,reason,status,idempotency_key)
 VALUES($1,$2,'end_of_billing','已批准到期取消','approved','expiry-approved-cancellation')`, f.sid, f.uid)
			case "busy":
				_, err = f.db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status)
 VALUES($1,'sync','expiry-busy-read','pending')`, f.sid)
			default:
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET status=$2 WHERE id=$1", f.sid, change)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := supplierExpiryMoneySnapshot(t, f.db, f.uid)
			for range 2 {
				if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
					t.Fatalf("ineligible %s queued=%d err=%v", change, n, err)
				}
			}
			var jobs int
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='suspend'", f.sid).Scan(&jobs); err != nil || jobs != 0 {
				t.Fatalf("ineligible expiry jobs=%d err=%v", jobs, err)
			}
			if after := supplierExpiryMoneySnapshot(t, f.db, f.uid); after != before || f.calls() != 0 {
				t.Fatalf("ineligible expiry changed money or upstream: before=%+v after=%+v calls=%d", before, after, f.calls())
			}
		})
	}
}

func TestSupplierExpiryConcurrentScansQueueOnce(t *testing.T) {
	f := newSupplierLifecycleFixture(t, false)
	supplierExpiryArm(t, f)
	before := supplierExpiryMoneySnapshot(t, f.db, f.uid)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				if _, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var jobs int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='suspend'", f.sid).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("concurrent expiry tasks=%d err=%v", jobs, err)
	}
	if after := supplierExpiryMoneySnapshot(t, f.db, f.uid); after != before || f.calls() != 0 {
		t.Fatalf("concurrent expiry changed money or upstream: before=%+v after=%+v calls=%d", before, after, f.calls())
	}
}

func TestSupplierExpiryRejectsForgedSystemActionKey(t *testing.T) {
	f := newSupplierLifecycleFixture(t, true)
	due := supplierExpiryArm(t, f)
	key := fmt.Sprintf("expiry-suspend-service-%d-due-%d", f.sid, due.UnixMicro())
	expect(t, request(f.h, "POST", f.path()+"/actions", map[string]any{"action": "suspend"}, f.root, key), http.StatusConflict)
	var jobs int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='suspend'", f.sid).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("forged expiry jobs=%d err=%v", jobs, err)
	}
	if f.calls() != 0 {
		t.Fatal("forged expiry source dispatched lifecycle command")
	}
}

func TestSupplierExpiryDispatchRechecksExtendedDeadline(t *testing.T) {
	f := newSupplierLifecycleFixture(t, true)
	supplierExpiryArm(t, f)
	before := supplierExpiryMoneySnapshot(t, f.db, f.uid)
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("expiry queued=%d err=%v", n, err)
	}
	j := f.claim(t)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=now()+interval '1 month' WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	f.check(t, j, "cancelled", "active", false)
	supplierExpiryMarker(t, f.db, f.sid, 0)
	if after := supplierExpiryMoneySnapshot(t, f.db, f.uid); after != before || f.calls() != 0 {
		t.Fatalf("obsolete expiry action changed money or upstream: before=%+v after=%+v calls=%d", before, after, f.calls())
	}
}

func TestSupplierExpiryFreshReadProtectsUpstreamEntitlement(t *testing.T) {
	for _, upstream := range []string{"future_deadline", "read_failure", "wrong_resource", "wrong_product", "no_deadline", "suspended_due", "suspended_abuse"} {
		t.Run(upstream, func(t *testing.T) {
			f := newSupplierLifecycleFixture(t, true)
			due := supplierExpiryArm(t, f)
			before := supplierExpiryMoneySnapshot(t, f.db, f.uid)
			if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
				t.Fatalf("expiry queued=%d err=%v", n, err)
			}
			future := time.Now().UTC().AddDate(0, 1, 0).Truncate(time.Second)
			wantStatus, wantReason := "active", ""
			switch upstream {
			case "future_deadline":
				// Change only the upstream copy: a local-only eligibility
				// recheck cannot protect this still-paid service.
				f.remote(func(s *supplier.Service) { s.NextDueDate = fmt.Sprint(future.Unix()) })
			case "read_failure":
				// Leave the write key valid. Execute would succeed if the
				// worker wrongly treated the failed read as permission to stop.
				compiledSupplierState.Lock()
				compiledSupplierState.accounts[f.accountKey].catalogKey = "read-key-invalidated-before-expiry-dispatch"
				compiledSupplierState.Unlock()
			case "wrong_resource":
				f.remote(func(s *supplier.Service) { s.UpstreamID++ })
			case "wrong_product":
				f.remote(func(s *supplier.Service) { s.ProductID++ })
			case "no_deadline":
				f.remote(func(s *supplier.Service) { s.NextDueDate = "" })
			case "suspended_due":
				wantStatus, wantReason = "suspended", "due-existing-expiry-pause"
				f.remote(func(s *supplier.Service) {
					s.Status, s.SuspendReason, s.SuspendReasonType = "Suspended", "existing-expiry-pause", "due"
				})
			case "suspended_abuse":
				wantStatus, wantReason = "suspended", "abuse-existing-policy-pause"
				f.remote(func(s *supplier.Service) {
					s.Status, s.SuspendReason, s.SuspendReasonType = "Suspended", "existing-policy-pause", "abuse"
				})
			}
			j := f.claim(t)
			if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			var state, service, reason string
			var currentDue time.Time
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT j.status,s.status,
 coalesce(s.resource_info->>'suspend_reason',''),s.next_due_at FROM provision_jobs j
 JOIN services s ON s.id=j.service_id WHERE j.id=$1`, j.ID).Scan(&state, &service, &reason, &currentDue); err != nil {
				t.Fatal(err)
			}
			if service != wantStatus || reason != wantReason || currentDue.Before(due) {
				t.Fatalf("fresh expiry read state=%s service=%s reason=%q due=%s; want %s/%q due>=%s", state, service, reason, currentDue, wantStatus, wantReason, due)
			}
			if upstream == "future_deadline" {
				if state != "cancelled" || !currentDue.Equal(future) {
					t.Fatalf("still-paid upstream stopped or stale date retained: state=%s due=%s want=%s", state, currentDue, future)
				}
			} else if state == "succeeded" {
				t.Fatal("fresh observation or failed preflight was claimed as a successful system suspension")
			}
			// Already-paused upstream state is an observed fact, never proof
			// that this unsent system action caused that pause.
			supplierExpiryMarker(t, f.db, f.sid, 0)
			var pausedEvents int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_events
 WHERE service_id=$1 AND kind='service.suspended'`, f.sid).Scan(&pausedEvents); err != nil {
				t.Fatal(err)
			}
			wantEvents := 0
			if wantStatus == "suspended" {
				wantEvents = 1
			}
			if pausedEvents != wantEvents {
				t.Fatalf("fresh observation suspension events=%d want=%d", pausedEvents, wantEvents)
			}
			if after := supplierExpiryMoneySnapshot(t, f.db, f.uid); after != before || f.calls() != 0 {
				t.Fatalf("fresh expiry preflight spent money or dispatched a stop: before=%+v after=%+v calls=%d", before, after, f.calls())
			}
			if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
				t.Fatalf("unsafe expiry preflight was requeued: n=%d err=%v", n, err)
			}
		})
	}
}

func TestSupplierExpiryFreshReadRequeuesChangedExpiredDeadline(t *testing.T) {
	f := newSupplierLifecycleFixture(t, true)
	due := supplierExpiryArm(t, f)
	before := supplierExpiryMoneySnapshot(t, f.db, f.uid)
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("original expiry queued=%d err=%v", n, err)
	}
	original := f.claim(t)
	actualDue := due.Truncate(time.Second).Add(6 * time.Hour)
	f.remote(func(s *supplier.Service) { s.NextDueDate = fmt.Sprint(actualDue.Unix()) })
	if err := f.db.ProcessProvisionJob(t.Context(), *original); err != nil {
		t.Fatal(err)
	}
	f.check(t, original, "cancelled", "active", false)
	supplierExpiryMarker(t, f.db, f.sid, 0)
	var refreshed time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", f.sid).Scan(&refreshed); err != nil || !refreshed.Equal(actualDue) {
		t.Fatalf("changed expired entitlement was not refreshed: due=%s want=%s err=%v", refreshed, actualDue, err)
	}
	if f.calls() != 0 {
		t.Fatal("old expiry identity dispatched a suspension after the upstream term changed")
	}
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("fresh expired deadline queued=%d err=%v", n, err)
	}
	next := f.claim(t)
	if next.ID == original.ID {
		t.Fatal("changed upstream term reused cancelled expiry responsibility")
	}
	supplierExpirySource(t, f.db, next, actualDue, false)
	if err := f.db.ProcessProvisionJob(t.Context(), *next); err != nil {
		t.Fatal(err)
	}
	f.check(t, original, "cancelled", "suspended", false)
	f.check(t, next, "succeeded", "suspended", false)
	supplierExpiryMarker(t, f.db, f.sid, next.ID)
	if after := supplierExpiryMoneySnapshot(t, f.db, f.uid); after != before || f.calls() != 1 {
		t.Fatalf("expiry responsibility transfer changed money or duplicated writes: before=%+v after=%+v calls=%d", before, after, f.calls())
	}
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
		t.Fatalf("fresh expiry replay queued=%d err=%v", n, err)
	}
}

func TestSupplierExpiryEarlierUpstreamDeadlineRequiresManualReview(t *testing.T) {
	f := newSupplierLifecycleFixture(t, true)
	due := supplierExpiryArm(t, f)
	before := supplierExpiryMoneySnapshot(t, f.db, f.uid)
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("expiry queued=%d err=%v", n, err)
	}
	j := f.claim(t)
	f.remote(func(s *supplier.Service) { s.NextDueDate = fmt.Sprint(due.Add(-6 * time.Hour).Unix()) })
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	f.check(t, j, "waiting_manual", "active", false)
	supplierExpiryMarker(t, f.db, f.sid, 0)
	supplierExpirySource(t, f.db, j, due, true)
	var retained time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", f.sid).Scan(&retained); err != nil || !retained.Equal(due) {
		t.Fatalf("earlier upstream date shortened local entitlement: due=%s want=%s err=%v", retained, due, err)
	}
	if after := supplierExpiryMoneySnapshot(t, f.db, f.uid); after != before || f.calls() != 0 {
		t.Fatalf("ambiguous upstream term caused expense or pause: before=%+v after=%+v calls=%d", before, after, f.calls())
	}
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
		t.Fatalf("manual deadline review duplicated: n=%d err=%v", n, err)
	}
}

func TestSupplierExpiryManualConfirmationPreservesPauseAfterDeadlineExtension(t *testing.T) {
	f := newSupplierLifecycleFixture(t, false)
	supplierExpiryArm(t, f)
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("expiry queued=%d err=%v", n, err)
	}
	j := supplierExpiryJob(t, f.db, f.sid)
	future := time.Now().UTC().AddDate(0, 1, 0).Truncate(time.Second)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=$2 WHERE id=$1", f.sid, future); err != nil {
		t.Fatal(err)
	}
	f.remote(func(s *supplier.Service) {
		s.Status, s.SuspendReason, s.SuspendReasonType = "Suspended", "expired", "due"
		s.NextDueDate = fmt.Sprint(future.Unix())
	})
	before := supplierExpiryMoneySnapshot(t, f.db, f.uid)
	expect(t, f.verify(t, j, f.root), http.StatusOK)
	f.check(t, j, "succeeded", "suspended", false)
	supplierExpiryMarker(t, f.db, f.sid, j.ID)
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
		t.Fatalf("a changed date alone authorized automatic recovery: queued=%d err=%v", n, err)
	}
	if after := supplierExpiryMoneySnapshot(t, f.db, f.uid); after != before || f.calls() != 0 {
		t.Fatalf("manual expiry verification changed money or upstream: before=%+v after=%+v calls=%d", before, after, f.calls())
	}
}

// Use the built-in Mofang HTTP provider, which intentionally has no lifecycle
// write capabilities. Only its local upstream clock and post-payment read are
// simulated; the real scheduler, manual verifier and renewal adapter execute.
func newSupplierExpiryRenewalFixture(t *testing.T, callbackBeforeVerification ...bool) *supplierRenewalStateFixture {
	t.Helper()
	var options supplierExpiryRenewalOptions
	if len(callbackBeforeVerification) > 0 && callbackBeforeVerification[0] {
		options.callbackReason = "due"
	}
	return newSupplierExpiryRenewalFixtureWithOptions(t, options)
}

type supplierExpiryRenewalOptions struct {
	callbackReason, remoteReason string
	localMicroseconds            int64
}

func newSupplierExpiryRenewalFixtureWithOptions(t *testing.T, options supplierExpiryRenewalOptions) *supplierRenewalStateFixture {
	t.Helper()
	f, sid := activeSupplierRenewalFixture(t)
	remoteDue := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	x := &supplierRenewalStateFixture{f: f, sid: sid, due: remoteDue.Add(time.Duration(options.localMicroseconds) * time.Microsecond)}
	after, clock := supplierRenewalClockServer(t, f, remoteDue)
	x.after = after
	f.remoteStatus.Store("Active")
	f.remoteSuspendReason.Store("")
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := httptest.NewRecorder()
		clock.Config.Handler.ServeHTTP(response, r)
		if r.URL.Path != "/host/header" || f.renewalPays.Load() == 0 {
			w.WriteHeader(response.Code)
			w.Write(response.Body.Bytes())
			return
		}
		x.paidReads.Add(1)
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if x.paidActive.Load() {
			host := body["data"].(map[string]any)["host_data"].(map[string]any)
			host["domainstatus"], host["suspendreason"], host["suspendreason_type"] = "Active", "", ""
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(remote.Close)
	supplierFixtureIdentityWrite(t, f.db, "UPDATE supplier_accounts SET base_url=$2,config=jsonb_set(config,'{endpoint}',to_jsonb($2::text)),identity_key='' WHERE id=$1", f.supplierID, remote.URL)
	supplierArmNearDue(t, f, sid, x.due)
	if options.localMicroseconds != 0 {
		// The upstream speaks whole seconds; preserve the local immutable
		// microsecond identity rather than pretending its deadline changed.
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=$2 WHERE id=$1", sid, x.due); err != nil {
			t.Fatal(err)
		}
	}
	before := supplierExpiryMoneySnapshot(t, f.db, f.uid)
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("Mofang expiry queued=%d err=%v", n, err)
	}
	j := supplierExpiryJob(t, f.db, sid)
	supplierExpirySource(t, f.db, j, x.due, true)
	if state, service, lease := f.state(t, j); state != "waiting_manual" || service != "active" || lease != nil {
		t.Fatalf("unconfirmed Mofang expiry state=%s service=%s lease=%v", state, service, lease)
	}
	f.remoteStatus.Store("Suspended")
	reason := options.remoteReason
	if reason == "" {
		reason = "due"
	}
	f.remoteSuspendReason.Store(reason)
	if options.callbackReason != "" {
		x.callback(t, "Suspended", options.callbackReason)
		x.check(t, "suspended", options.callbackReason, x.due, 0)
		// An ordinary callback can report the pause, but only the later
		// trusted manual reconciliation establishes this system provenance.
		supplierExpiryMarker(t, f.db, sid, 0)
	}
	path := fmt.Sprintf("/api/v1/admin/services/%d/supplier-lifecycle/verify", sid)
	input := store.SupplierVerificationInput{JobID: j.ID, ExpectedAttempts: j.Attempts, UpstreamID: 42, Outcome: "delivered", Note: "只读核验过期资源已由人工在供应商侧暂停"}
	expect(t, request(f.h, "POST", path, input, f.root, ""), http.StatusOK)
	x.check(t, "suspended", reason, x.due, 0)
	wantSource := j.ID
	callbackDue := options.callbackReason == "due" || strings.HasPrefix(options.callbackReason, "due-")
	remoteDueReason := reason == "due" || strings.HasPrefix(reason, "due-")
	if options.callbackReason != "" && (!callbackDue || !remoteDueReason) {
		wantSource = 0
	}
	supplierExpiryMarker(t, f.db, sid, wantSource)
	if got := supplierExpiryMoneySnapshot(t, f.db, f.uid); got != before || f.actions.Load() != 0 {
		t.Fatalf("Mofang expiry created debt or sent lifecycle write: before=%+v after=%+v actions=%d", before, got, f.actions.Load())
	}
	return x
}

func TestSupplierExpiryManualCallbackBeforeVerificationStillRecovers(t *testing.T) {
	x := newSupplierExpiryRenewalFixture(t, true)
	x.paidActive.Store(true)
	before := autoRenewBalance(t, x.f.db, x.f.uid)
	j := x.collect(t)
	if err := x.f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	if state, _, lease := x.f.state(t, j); state != "succeeded" || lease != nil || x.paidReads.Load() == 0 {
		t.Fatalf("callback-raced renewal state=%s lease=%v postpayment reads=%d", state, lease, x.paidReads.Load())
	}
	x.check(t, "active", "", x.after, 1)
	supplierExpiryMarker(t, x.f.db, x.sid, 0)
	supplierRenewalEvents(t, x.f, x.sid, 1)
	x.sync(t, "expiry-manual-callback-recovery-replay")
	x.check(t, "active", "", x.after, 1)
	if x.f.renews.Load() != 1 || x.f.renewalPays.Load() != 1 || x.f.actions.Load() != 0 || autoRenewBalance(t, x.f.db, x.f.uid) != before-2500 {
		t.Fatal("callback/manual-verification race repeated supplier writes or local collection")
	}
}

func TestSupplierExpiryMicrosecondSourceSurvivesSecondPrecisionRenewal(t *testing.T) {
	x := newSupplierExpiryRenewalFixtureWithOptions(t, supplierExpiryRenewalOptions{localMicroseconds: 123456})
	if x.due.Nanosecond() != 123456000 {
		t.Fatalf("fixture did not preserve a microsecond deadline: %s", x.due)
	}
	x.paidActive.Store(true)
	before := autoRenewBalance(t, x.f.db, x.f.uid)
	j := x.collect(t)
	if err := x.f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	if state, _, lease := x.f.state(t, j); state != "succeeded" || lease != nil || x.paidReads.Load() == 0 {
		t.Fatalf("microsecond-source renewal state=%s lease=%v postpayment reads=%d", state, lease, x.paidReads.Load())
	}
	x.check(t, "active", "", x.after, 1)
	supplierExpiryMarker(t, x.f.db, x.sid, 0)
	supplierRenewalEvents(t, x.f, x.sid, 1)
	x.sync(t, "expiry-microsecond-source-recovery-replay")
	x.check(t, "active", "", x.after, 1)
	if x.f.renews.Load() != 1 || x.f.renewalPays.Load() != 1 || x.f.actions.Load() != 0 || autoRenewBalance(t, x.f.db, x.f.uid) != before-2500 {
		t.Fatal("deadline precision normalization duplicated collection or supplier writes")
	}
}

func TestSupplierExpiryPolicyCallbackBeforeVerificationCannotGainRecoveryAuthority(t *testing.T) {
	for _, remoteReason := range []string{"due", "abuse-policy-hold"} {
		t.Run("verified_remote_"+remoteReason, func(t *testing.T) {
			x := newSupplierExpiryRenewalFixtureWithOptions(t, supplierExpiryRenewalOptions{
				callbackReason: "abuse-policy-hold", remoteReason: remoteReason,
			})
			// Even if the manual verifier now reads the coarser due reason,
			// it cannot reclassify an already observed independent policy hold.
			supplierExpiryMarker(t, x.f.db, x.sid, 0)
			x.paidActive.Store(true)
			before := autoRenewBalance(t, x.f.db, x.f.uid)
			j := x.collect(t)
			if err := x.f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			if state, _, lease := x.f.state(t, j); state != "succeeded" || lease != nil {
				t.Fatalf("independent paid renewal state=%s lease=%v", state, lease)
			}
			x.check(t, "suspended", remoteReason, x.after, 0)
			x.sync(t, "expiry-policy-callback-confirmation-replay-"+remoteReason)
			x.check(t, "suspended", remoteReason, x.after, 0)
			supplierExpiryMarker(t, x.f.db, x.sid, 0)
			supplierRenewalEvents(t, x.f, x.sid, 1)
			if n, err := x.f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
				t.Fatalf("policy callback gained scheduled recovery: n=%d err=%v", n, err)
			}
			var resumes int
			if err := x.f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='resume'", x.sid).Scan(&resumes); err != nil || resumes != 0 {
				t.Fatalf("policy callback queued resume=%d err=%v", resumes, err)
			}
			if x.f.renews.Load() != 1 || x.f.renewalPays.Load() != 1 || x.f.actions.Load() != 0 || autoRenewBalance(t, x.f.db, x.f.uid) != before-2500 {
				t.Fatal("policy protection duplicated renewal or performed lifecycle write")
			}
		})
	}
}

func TestSupplierExpiryBillingPolicyCallbackBeforeVerificationCannotGainRecoveryAuthority(t *testing.T) {
	f, sid := activeSupplierRenewalFixture(t)
	var orderID, invoiceID int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash)
 VALUES('EXPIRY-BILLING-ORDER',$1,'pending',100,'expiry-billing-policy-order','billing-policy-fixture') RETURNING id`, f.uid).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,due_at)
 VALUES('EXPIRY-BILLING-HOLD-'||replace(gen_random_uuid()::text,'-',''),$1,$2,'unpaid',100,now()-interval '2 days') RETURNING id`, f.uid, orderID).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id)
 VALUES($1,'独立账单逾期暂停来源回归','service',100,$2)`, invoiceID, sid); err != nil {
		t.Fatal(err)
	}
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("billing suspension queued=%d err=%v", n, err)
	}
	j := supplierExpiryJob(t, f.db, sid)
	x := &supplierRenewalStateFixture{f: f, sid: sid}
	x.callback(t, "Suspended", "abuse-policy-hold")
	supplierExpiryMarker(t, f.db, sid, 0)
	f.remoteStatus.Store("Suspended")
	f.remoteSuspendReason.Store("due")
	path := fmt.Sprintf("/api/v1/admin/services/%d/supplier-lifecycle/verify", sid)
	input := store.SupplierVerificationInput{JobID: j.ID, ExpectedAttempts: j.Attempts, UpstreamID: 42, Outcome: "delivered", Note: "核验已暂停事实但不得覆盖先到的策略暂停来源"}
	expect(t, request(f.h, "POST", path, input, f.root, ""), http.StatusOK)
	if state, service, lease := f.state(t, j); state != "succeeded" || service != "suspended" || lease != nil {
		t.Fatalf("billing manual verification state=%s service=%s lease=%v", state, service, lease)
	}
	supplierExpiryMarker(t, f.db, sid, 0)
	before := autoRenewBalance(t, f.db, f.uid)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, f.customer, "billing-policy-callback-invoice-payment"), http.StatusCreated)
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
		t.Fatalf("billing policy hold gained scheduled recovery: n=%d err=%v", n, err)
	}
	var status string
	var resumed, resumes int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT status,
 (SELECT count(*) FROM notification_events WHERE service_id=$1 AND kind='service.resumed'),
 (SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='resume')
 FROM services WHERE id=$1`, sid).Scan(&status, &resumed, &resumes); err != nil {
		t.Fatal(err)
	}
	if status != "suspended" || resumed != 0 || resumes != 0 || f.actions.Load() != 0 || f.renews.Load() != 0 || f.renewalPays.Load() != 0 || autoRenewBalance(t, f.db, f.uid) != before-100 {
		t.Fatalf("paid billing task bypassed policy hold: status=%s resumed=%d resume_jobs=%d actions=%d", status, resumed, resumes, f.actions.Load())
	}
	supplierExpiryMarker(t, f.db, sid, 0)
}

func TestSupplierExpiryPaidRenewalRefreshesSystemPause(t *testing.T) {
	for _, delayed := range []bool{false, true} {
		t.Run(fmt.Sprintf("delayed_active=%t", delayed), func(t *testing.T) {
			x := newSupplierExpiryRenewalFixture(t)
			x.paidActive.Store(!delayed)
			balance := autoRenewBalance(t, x.f.db, x.f.uid)
			j := x.collect(t)
			if err := x.f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			if state, _, lease := x.f.state(t, j); state != "succeeded" || lease != nil || x.paidReads.Load() == 0 {
				t.Fatalf("paid renewal state=%s lease=%v postpayment reads=%d", state, lease, x.paidReads.Load())
			}
			if delayed {
				x.check(t, "suspended", "due", x.after, 0)
				var recovery int64
				if err := x.f.db.Pool.QueryRow(t.Context(), "SELECT (resource_info->>'supplier_renewal_recovery_job_id')::bigint FROM services WHERE id=$1", x.sid).Scan(&recovery); err != nil || recovery != j.ID {
					t.Fatalf("deferred expiry recovery marker=%d want=%d err=%v", recovery, j.ID, err)
				}
				x.paidActive.Store(true)
				x.callback(t, "Active", "")
				x.check(t, "suspended", "due", x.after, 0)
				x.sync(t, "expiry-paid-deferred-fresh-read")
			}
			x.check(t, "active", "", x.after, 1)
			supplierExpiryMarker(t, x.f.db, x.sid, 0)
			supplierRenewalEvents(t, x.f, x.sid, 1)
			x.sync(t, "expiry-paid-refresh-replay")
			x.check(t, "active", "", x.after, 1)
			if n, err := x.f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 0 {
				t.Fatalf("confirmed expiry renewal recollected: n=%d err=%v", n, err)
			}
			if x.f.renews.Load() != 1 || x.f.renewalPays.Load() != 1 || x.f.actions.Load() != 0 || autoRenewBalance(t, x.f.db, x.f.uid) != balance-2500 {
				t.Fatal("expiry recovery repeated renewal, collection or explicit lifecycle command")
			}
		})
	}
}

func TestSupplierExpiryPaidRenewalPreservesNewManualOrPolicyHold(t *testing.T) {
	for _, reason := range []string{"manual-security-hold", "self-operator-hold", "abuse-policy-violation", "due"} {
		t.Run(reason, func(t *testing.T) {
			x := newSupplierExpiryRenewalFixture(t)
			if reason == "due" {
				// A later manual suspension may use exactly the same coarse
				// reason. Its provenance must invalidate the older expiry marker.
				if _, err := x.f.db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,attempts,request_payload,completed_at)
 VALUES($1,'suspend','expiry-later-manual-due-pause','succeeded',1,'{"manual_lifecycle":true}',clock_timestamp())`, x.sid); err != nil {
					t.Fatal(err)
				}
			} else {
				x.f.remoteSuspendReason.Store(reason)
				x.callback(t, "Suspended", reason)
			}
			x.paidActive.Store(true)
			j := x.collect(t)
			if err := x.f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			if state, _, lease := x.f.state(t, j); state != "succeeded" || lease != nil {
				t.Fatalf("independent renewal state=%s lease=%v", state, lease)
			}
			x.check(t, "suspended", reason, x.after, 0)
			x.sync(t, "expiry-independent-hold-fresh-read-"+reason)
			x.check(t, "suspended", reason, x.after, 0)
			supplierRenewalEvents(t, x.f, x.sid, 1)
			if x.f.renews.Load() != 1 || x.f.renewalPays.Load() != 1 || x.f.actions.Load() != 0 {
				t.Fatal("manual hold protection repeated writes")
			}
		})
	}
}
