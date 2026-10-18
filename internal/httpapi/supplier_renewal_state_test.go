package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

// Exercise the real supplier HTTP adapter: only the upstream clock and the
// post-payment lifecycle response differ from the existing purchase fixture.
type supplierRenewalStateFixture struct {
	f                              *supplierFixture
	sid                            int64
	due, after                     time.Time
	paidActive                     atomic.Bool
	paidReads                      atomic.Int64
	fault                          atomic.Value
	paymentEntered, paymentBlocked chan struct{}
	paymentOnce                    sync.Once
}

func newSupplierRenewalStateFixture(t *testing.T, reason string) *supplierRenewalStateFixture {
	t.Helper()
	return newSupplierRenewalStateFixtureWithStatus(t, "Suspended", reason)
}

func newSupplierRenewalStateFixtureWithStatus(t *testing.T, status, reason string) *supplierRenewalStateFixture {
	t.Helper()
	f, sid := activeSupplierRenewalFixture(t)
	x := &supplierRenewalStateFixture{f: f, sid: sid, due: time.Now().UTC().Add(time.Hour).Truncate(time.Second)}
	after, clock := supplierRenewalClockServer(t, f, x.due)
	x.after = after
	x.fault.Store("")
	f.remoteStatus.Store(status)
	f.remoteSuspendReason.Store(reason)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fault, _ := x.fault.Load().(string)
		if r.URL.Path == "/apply_credit" && r.PostFormValue("invoiceid") == "91" && x.paymentEntered != nil {
			x.paymentOnce.Do(func() { close(x.paymentEntered) })
			<-x.paymentBlocked
		}
		response := httptest.NewRecorder()
		clock.Config.Handler.ServeHTTP(response, r)
		if fault == "unknown_renew" && r.URL.Path == "/host/renew" {
			fmt.Fprint(w, `{"status":200,"data":`)
			return
		}
		mutateHeader := r.URL.Path == "/host/header" && f.renewalPays.Load() > 0
		mutateQuote := r.URL.Path == "/host/renewpage" && fault == "quote_changed"
		if !mutateHeader && !mutateQuote {
			w.WriteHeader(response.Code)
			w.Write(response.Body.Bytes())
			return
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		data := body["data"].(map[string]any)
		if mutateQuote {
			data["host"].(map[string]any)["amount"] = "16.01"
		}
		if mutateHeader {
			x.paidReads.Add(1)
			host := data["host_data"].(map[string]any)
			host["domain"], host["dedicatedip"] = "renewed.vm.example", "198.51.100.42"
			if x.paidActive.Load() {
				host["domainstatus"], host["suspendreason"], host["suspendreason_type"] = "Active", "", ""
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(remote.Close)
	supplierFixtureIdentityWrite(t, f.db, "UPDATE supplier_accounts SET base_url=$2,config=jsonb_set(config,'{endpoint}',to_jsonb($2::text)),identity_key='' WHERE id=$1", f.supplierID, remote.URL)
	x.callback(t, status, reason)
	supplierArmNearDue(t, f, sid, x.due)
	return x
}

func (x *supplierRenewalStateFixture) callback(t *testing.T, status, reason string) {
	t.Helper()
	id := strconv.FormatInt(x.sid, 10)
	values := url.Values{
		"id": {id}, "host_id": {"42"}, "domainstatus": {status}, "suspendreason": {reason},
		"rand_str": {"renewal-state-callback"},
	}
	values.Set("signature", supplierCallbackSign(id, supplierCallbackToken(x.sid), "renewal-state-callback"))
	expect(t, supplierCallbackRequest(x.f.h, "/api/host/sync", values), http.StatusOK)
}

func (x *supplierRenewalStateFixture) collect(t *testing.T) *store.ProvisionJob {
	t.Helper()
	if n, err := x.f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 1 {
		t.Fatalf("automatic collection count=%d err=%v", n, err)
	}
	j := x.f.claim(t)
	if j == nil || j.Action != "renew" || j.ServiceID != x.sid {
		t.Fatalf("claimed unexpected renewal: %+v", j)
	}
	return j
}

func (x *supplierRenewalStateFixture) sync(t *testing.T, key string) {
	t.Helper()
	expect(t, request(x.f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", x.sid), map[string]any{"action": "sync"}, x.f.root, key), http.StatusAccepted)
	j := x.f.claim(t)
	if j == nil || j.Action != "sync" || j.ServiceID != x.sid {
		t.Fatalf("claimed unexpected sync: %+v", j)
	}
	if err := x.f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	state, _, lease := x.f.state(t, j)
	if state != "succeeded" || lease != nil {
		t.Fatalf("read-only sync state=%s lease=%v", state, lease)
	}
}

func (x *supplierRenewalStateFixture) check(t *testing.T, wantStatus, wantReason string, wantDue time.Time, wantResumed int) {
	t.Helper()
	var status, reason string
	var due time.Time
	var resumed int
	if err := x.f.db.Pool.QueryRow(t.Context(), `SELECT status,coalesce(resource_info->>'suspend_reason',''),next_due_at,
 (SELECT count(*) FROM notification_events WHERE service_id=$1 AND kind='service.resumed') FROM services WHERE id=$1`, x.sid).Scan(&status, &reason, &due, &resumed); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus || reason != wantReason || !due.Equal(wantDue) || resumed != wantResumed {
		t.Fatalf("service status=%s reason=%q due=%s resumed=%d; want %s/%q/%s/%d", status, reason, due, resumed, wantStatus, wantReason, wantDue, wantResumed)
	}
}

func TestSupplierRenewalRefreshesDueSuspensionAfterPayment(t *testing.T) {
	for _, verify := range []bool{false, true} {
		t.Run(fmt.Sprintf("operator_confirmation=%t", verify), func(t *testing.T) {
			reason := "due-expired"
			if verify {
				reason = "due"
			}
			x := newSupplierRenewalStateFixture(t, reason)
			x.paidActive.Store(true)
			x.f.renewalDueUnchanged.Store(verify)
			balance := autoRenewBalance(t, x.f.db, x.f.uid)
			j := x.collect(t)
			if err := x.f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			if verify {
				x.check(t, "suspended", reason, x.due, 0)
				supplierRenewalEvents(t, x.f, x.sid, 0)
				x.f.renewalDueUnchanged.Store(false)
				path := fmt.Sprintf("/api/v1/admin/services/%d/supplier-verification", x.sid)
				body := store.SupplierVerificationInput{JobID: j.ID, ExpectedAttempts: j.Attempts, UpstreamID: 42, Outcome: "delivered", Note: "确认原续费账单已支付且上游完整月份已经生效"}
				expect(t, request(x.f.h, "POST", path, body, x.f.root, ""), http.StatusOK)
				expect(t, request(x.f.h, "POST", path, body, x.f.root, ""), http.StatusNotFound)
			}
			state, _, lease := x.f.state(t, j)
			if state != "succeeded" || lease != nil {
				t.Fatalf("confirmed renewal state=%s lease=%v", state, lease)
			}
			x.check(t, "active", "", x.after, 1)
			supplierRenewalEvents(t, x.f, x.sid, 1)
			var domain, ip string
			if err := x.f.db.Pool.QueryRow(t.Context(), `SELECT domain,resource_info->>'dedicated_ip' FROM services WHERE id=$1`, x.sid).Scan(&domain, &ip); err != nil || domain != "renewed.vm.example" || ip != "198.51.100.42" || x.paidReads.Load() < 1 {
				t.Fatalf("post-payment upstream read was not applied: domain=%s ip=%s reads=%d err=%v", domain, ip, x.paidReads.Load(), err)
			}
			x.sync(t, "due-renewal-post-success-sync")
			x.callback(t, "Active", "")
			x.check(t, "active", "", x.after, 1)
			supplierRenewalEvents(t, x.f, x.sid, 1)
			if n, err := x.f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 0 {
				t.Fatal("confirmed renewal collected again", n, err)
			}
			if x.f.renews.Load() != 1 || x.f.renewalPays.Load() != 1 || x.f.actions.Load() != 0 || autoRenewBalance(t, x.f.db, x.f.uid) != balance-2500 {
				t.Fatal("state reconciliation repeated upstream writes or local collection")
			}
		})
	}
	for _, reason := range []string{"due-expired", "manual-security-hold", "abuse-policy-violation"} {
		t.Run("suspension_during_payment_"+reason, func(t *testing.T) {
			x := newSupplierRenewalStateFixtureWithStatus(t, "Active", "")
			x.paidActive.Store(true)
			x.paymentEntered, x.paymentBlocked = make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(x.paymentBlocked) }) }
			j := x.collect(t)
			done := make(chan error, 1)
			go func() { done <- x.f.db.ProcessProvisionJob(t.Context(), *j) }()
			finished := false
			defer func() {
				release()
				if !finished {
					select {
					case err := <-done:
						if err != nil {
							t.Error(err)
						}
					case <-time.After(10 * time.Second):
						t.Error("blocked renewal did not finish after releasing payment")
					}
				}
			}()
			select {
			case <-x.paymentEntered:
			case <-time.After(10 * time.Second):
				t.Fatal("renewal never reached upstream payment")
			}
			// The write was authorized while Active. A real callback may report
			// the expiry pause before the paid read observes upstream recovery.
			x.callback(t, "Suspended", reason)
			x.check(t, "suspended", reason, x.due, 0)
			supplierRenewalEvents(t, x.f, x.sid, 0)
			release()
			select {
			case err := <-done:
				finished = true
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("renewal did not finish after upstream payment")
			}
			state, _, lease := x.f.state(t, j)
			if state != "succeeded" || lease != nil || x.paidReads.Load() < 1 {
				t.Fatalf("confirmed raced renewal state=%s lease=%v fresh reads=%d", state, lease, x.paidReads.Load())
			}
			wantStatus, wantReason, resumed := "suspended", reason, 0
			if reason == "due-expired" {
				wantStatus, wantReason, resumed = "active", "", 1
			}
			x.check(t, wantStatus, wantReason, x.after, resumed)
			supplierRenewalEvents(t, x.f, x.sid, 1)
			x.sync(t, "payment-race-follow-up-sync-"+reason)
			x.check(t, wantStatus, wantReason, x.after, resumed)
			supplierRenewalEvents(t, x.f, x.sid, 1)
			if x.f.renews.Load() != 1 || x.f.renewalPays.Load() != 1 || x.f.actions.Load() != 0 {
				t.Fatal("payment/callback race repeated supplier writes")
			}
		})
	}
}

func TestSupplierRenewalDeferredDueRecoveryRequiresTrustedSource(t *testing.T) {
	for _, change := range []string{"none", "reason", "account_version", "new_lifecycle", "missing_provenance", "terminated"} {
		t.Run(change, func(t *testing.T) {
			x := newSupplierRenewalStateFixture(t, "due")
			j := x.collect(t)
			if err := x.f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			state, _, lease := x.f.state(t, j)
			if state != "succeeded" || lease != nil {
				t.Fatal("paid full period should be a successful renewal", state, lease)
			}
			x.check(t, "suspended", "due", x.after, 0)
			supplierRenewalEvents(t, x.f, x.sid, 1)
			x.paidActive.Store(true)
			wantReason := "due"
			switch change {
			case "none":
				// The historical callback signature does not authenticate the
				// supplied lifecycle fields. Only a real upstream Sync may consume
				// the recovery marker left by this paid, confirmed renewal.
				x.callback(t, "Active", "")
				x.check(t, "suspended", "due", x.after, 0)
			case "reason":
				wantReason = "manual-security-hold"
				x.callback(t, "Suspended", wantReason)
			case "account_version":
				if _, err := x.f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET version=version+1 WHERE id=$1", x.f.supplierID); err != nil {
					t.Fatal(err)
				}
			case "new_lifecycle":
				// Historical manual work must invalidate an older renewal even if
				// the operator used the same coarse state and reason string.
				if _, err := x.f.db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,attempts,request_payload,completed_at)
VALUES($1,'suspend','new-manual-pause-after-renewal','succeeded',1,'{"manual_lifecycle":true}',clock_timestamp())`, x.sid); err != nil {
					t.Fatal(err)
				}
			case "missing_provenance":
				// A historical succeeded renewal must not gain new recovery
				// authority merely from its action name and paid local invoice.
				if _, err := x.f.db.Pool.Exec(t.Context(), `UPDATE provision_jobs SET result=result-'renewal_expiry_recovery' WHERE id=$1`, j.ID); err != nil {
					t.Fatal(err)
				}
			case "terminated":
				x.callback(t, "Deleted", "")
				x.callback(t, "Active", "")
				x.check(t, "terminated", "due", x.after, 0)
				supplierRenewalEvents(t, x.f, x.sid, 1)
				return
			}
			x.sync(t, "deferred-renewal-status-read-"+change)
			if change == "none" {
				x.check(t, "active", "", x.after, 1)
			} else {
				x.check(t, "suspended", wantReason, x.after, 0)
			}
			supplierRenewalEvents(t, x.f, x.sid, 1)
			x.sync(t, "deferred-renewal-status-replay-"+change)
			supplierRenewalEvents(t, x.f, x.sid, 1)
			if x.f.renews.Load() != 1 || x.f.renewalPays.Load() != 1 || x.f.actions.Load() != 0 {
				t.Fatal("deferred reconciliation repeated upstream renewal or resumed by command")
			}
		})
	}
}

func TestSupplierRenewalPreservesManualSuspension(t *testing.T) {
	for _, reason := range []string{"manual-security-hold", "abuse-policy-violation", "policy-due", "due"} {
		t.Run(reason, func(t *testing.T) {
			x := newSupplierRenewalStateFixture(t, reason)
			if reason == "due" {
				// Existing manual lifecycle provenance wins over a reason that
				// happens to match the upstream expiry code.
				if _, err := x.f.db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,attempts,request_payload,completed_at)
VALUES($1,'suspend','existing-manual-due-pause','succeeded',1,'{"manual_lifecycle":true}',clock_timestamp())`, x.sid); err != nil {
					t.Fatal(err)
				}
			}
			x.paidActive.Store(true)
			// A plain upstream Active observation does not authorize recovery.
			x.f.remoteStatus.Store("Active")
			x.sync(t, "manual-pause-before-renewal-sync-"+reason)
			x.callback(t, "Active", "")
			x.check(t, "suspended", reason, x.due, 0)
			j := x.collect(t)
			if err := x.f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			state, _, lease := x.f.state(t, j)
			if state != "succeeded" || lease != nil {
				t.Fatal("paid full-period renewal should retain the independent manual pause", state, lease)
			}
			x.check(t, "suspended", reason, x.after, 0)
			supplierRenewalEvents(t, x.f, x.sid, 1)
			x.sync(t, "manual-pause-after-renewal-sync-"+reason)
			x.check(t, "suspended", reason, x.after, 0)
		})
	}
}

func TestSupplierRenewalFailureCannotResumeDueSuspension(t *testing.T) {
	for _, failure := range []string{"quote_changed", "unknown_renew", "incomplete_period"} {
		t.Run(failure, func(t *testing.T) {
			x := newSupplierRenewalStateFixture(t, "due")
			x.paidActive.Store(true)
			j := x.collect(t)
			if failure == "incomplete_period" {
				x.f.renewalDueUnchanged.Store(true)
			} else {
				x.fault.Store(failure)
			}
			if err := x.f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			state, _, lease := x.f.state(t, j)
			want := "waiting_verification"
			if failure == "quote_changed" {
				want = "failed"
			}
			if state != want || (lease != nil) != (want == "waiting_verification") {
				t.Fatalf("failed renewal state=%s lease=%v want=%s", state, lease, want)
			}
			x.check(t, "suspended", "due", x.due, 0)
			supplierRenewalEvents(t, x.f, x.sid, 0)
			// Even an authenticated callback advertising Active cannot turn an
			// unknown payment or incomplete period into a verified recovery.
			x.callback(t, "Active", "")
			x.check(t, "suspended", "due", x.due, 0)
			supplierRenewalEvents(t, x.f, x.sid, 0)
		})
	}
}
