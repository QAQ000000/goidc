package httpapi

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
)

func supplierCallbackToken(serviceID int64) string {
	key := []byte("01234567890123456789012345678901")
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("supplier-service:" + strconv.FormatInt(serviceID, 10)))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func TestSupplierMaintainedLifecycleRules(t *testing.T) {
	for _, via := range []string{"callback", "periodic"} {
		t.Run(via, func(t *testing.T) {
			f := supplierSetup(t)
			f.publish(t, f.pid, "local-vm-lifecycle")
			f.remoteStatus.Store("Pending")
			sid, _ := f.purchase(t, f.pid, "supplier-lifecycle")
			if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
				t.Fatal(err)
			}
			// The pending create schedules a delayed sync; use this same row for
			// the subsequent periodic reads so no other job competes for claims.
			var syncID int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM provision_jobs WHERE service_id=$1 AND action='sync'", sid).Scan(&syncID); err != nil {
				t.Fatal(err)
			}
			id := strconv.FormatInt(sid, 10)
			values := url.Values{"id": {id}, "host_id": {"42"}, "rand_str": {"lifecycle-rules"}}
			values.Set("signature", supplierCallbackSign(id, supplierCallbackToken(sid), "lifecycle-rules"))
			check := func(wantState, wantReason string) {
				t.Helper()
				var state, reason string
				if err := f.db.Pool.QueryRow(t.Context(), "SELECT status,coalesce(resource_info->>'suspend_reason','') FROM services WHERE id=$1", sid).Scan(&state, &reason); err != nil {
					t.Fatal(err)
				}
				if state != wantState || reason != wantReason {
					t.Fatalf("got state=%s reason=%s, want %s/%s", state, reason, wantState, wantReason)
				}
			}
			check("provisioning", "")
			for _, step := range []struct{ remote, want string }{
				{"Active", "active"}, {"Pending", "active"},
				{"Suspended", "suspended"}, {"Active", "suspended"},
				{"Pending", "suspended"}, {"Suspended", "suspended"},
			} {
				if via == "callback" {
					values.Set("domainstatus", step.remote)
					values.Set("suspendreason", "overdue")
					values.Set("suspendreason_type", "billing")
					if step.remote == "Suspended" {
						// Already prefixed reasons must not receive a second prefix.
						values.Set("suspendreason", "billing-overdue")
					}
					expect(t, supplierCallbackRequest(f.h, "/api/host/sync", values), 200)
				} else {
					f.remoteStatus.Store(step.remote)
					f.remoteSuspendReason.Store("overdue")
					f.remoteSuspendType.Store("billing")
					if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET status='pending',attempts=0,available_at=now(),locked_at=NULL,external_started_at=NULL WHERE id=$1", syncID); err != nil {
						t.Fatal(err)
					}
					if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
						t.Fatal(err)
					}
				}
				wantReason := ""
				if step.want == "suspended" {
					wantReason = "billing-overdue"
				}
				check(step.want, wantReason)
			}
			// Unknown states remain explicit errors, without changing the service.
			values.Set("domainstatus", "Fraud")
			expect(t, supplierCallbackRequest(f.h, "/api/host/sync", values), 400)
			check("suspended", "billing-overdue")
		})
	}
}

func TestSupplierInflightActiveSyncCannotReviveDeleted(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm-inflight")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET stock=1 WHERE id=$1", f.pid); err != nil {
		t.Fatal(err)
	}
	sid, _ := f.purchase(t, f.pid, "supplier-inflight")
	if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
		t.Fatal(err)
	}
	if n, err := f.db.EnqueuePeriodicSupplierSyncs(t.Context()); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	j := f.claim(t)
	f.syncEntered, f.syncBlocked = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- f.db.ProcessProvisionJob(t.Context(), *j) }()
	// Always release the upstream response and await the worker before the
	// fixture closes its server or removes its schema, even if an assertion fails.
	finished := false
	finish := func() {
		if finished {
			return
		}
		finished = true
		close(f.syncBlocked)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	defer finish()
	select {
	case <-f.syncEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("upstream sync did not start")
	}
	id := strconv.FormatInt(sid, 10)
	values := url.Values{"id": {id}, "host_id": {"42"}, "domainstatus": {"Deleted"}, "rand_str": {"inflight-delete"}}
	values.Set("signature", supplierCallbackSign(id, supplierCallbackToken(sid), "inflight-delete"))
	expect(t, supplierCallbackRequest(f.h, "/api/host/sync", values), 200)
	// The late Active read is applied after the deletion callback commits.
	finish()
	var state string
	var stock int
	var released bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT s.status,p.stock,s.inventory_released
FROM services s JOIN products p ON p.id=s.product_id WHERE s.id=$1`, sid).Scan(&state, &stock, &released); err != nil {
		t.Fatal(err)
	}
	if state != "terminated" || stock != 1 || !released {
		t.Fatal(state, stock, released)
	}
	jobState, serviceState, lease := f.state(t, j)
	if jobState != "succeeded" || serviceState != "terminated" || lease != nil {
		t.Fatal(jobState, serviceState, lease)
	}
	if n, err := f.db.EnqueuePeriodicSupplierSyncs(t.Context()); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func supplierCallbackSign(id, token, randStr string) string {
	payload, _ := json.Marshal(map[string]string{"id": id, "rand_str": randStr, "token": token})
	sum := md5.Sum(payload)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func supplierCallbackRequest(h http.Handler, path string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestSupplierHostSyncCallback(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm-callback")
	sid, _ := f.purchase(t, f.pid, "supplier-callback")
	j := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	id, token, randStr := strconv.FormatInt(sid, 10), supplierCallbackToken(sid), "callback-rand"
	values := url.Values{
		"id": {id}, "host_id": {"42"}, "domain": {"callback.vm.example"}, "domainstatus": {"Active"},
		"username": {"root"}, "password": {"callback-secret"}, "dedicatedip": {"198.51.100.20"},
		"assignedips": {"198.51.100.21"}, "port": {"22"}, "os": {"Ubuntu 24.04"}, "os_url": {"ubuntu"},
		"nextduedate": {"1900000000"}, "rand_str": {randStr},
	}
	values.Set("signature", supplierCallbackSign(id, token, randStr))
	if got := supplierCallbackRequest(f.h, "/api/host/sync", values).Code; got != http.StatusOK {
		t.Fatalf("callback status=%d", got)
	}
	var status, domain, username, resource string
	var sealed []byte
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT status,domain,username,resource_info::text,credentials_encrypted FROM services WHERE id=$1`, sid).Scan(&status, &domain, &username, &resource, &sealed); err != nil {
		t.Fatal(err)
	}
	if status != "active" || domain != "callback.vm.example" || username != "root" || !strings.Contains(resource, "198.51.100.20") {
		t.Fatalf("callback not applied status=%s domain=%s username=%s resource=%s", status, domain, username, resource)
	}
	credentials, err := provision.Unseal(f.db.ProvisionEncryptionKey, sid, sealed)
	if err != nil || credentials.Password != "callback-secret" {
		t.Fatalf("callback credentials unavailable: %v", err)
	}
	values.Set("signature", "BAD")
	if got := supplierCallbackRequest(f.h, "/api/host/sync", values).Code; got != http.StatusBadRequest {
		t.Fatalf("bad callback status=%d", got)
	}
}
