package httpapi

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

// TestNOKVMReadOnlySyncCrashAndFailureRecoverAutomatically covers the NOKVM
// half of the read-only lease defect. A crashed or failed sync has no unknown
// upstream write, so it must not sit in waiting_verification and must not keep
// the service marked as having unresolved external delivery, which is what
// previously blocked refunds and credential reads.
func TestNOKVMReadOnlySyncCrashAndFailureRecoverAutomatically(t *testing.T) {
	t.Run("crash", func(t *testing.T) {
		var reads atomic.Int32
		f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "POST":
				fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"vm-42","public_ip":[{"ip":"192.0.2.42"}]}}`)
			case r.URL.Path == "/api/virtual/42":
				reads.Add(1)
				fmt.Fprint(w, `{"code":0,"data":{"name":"synced-42","ip_list":[{"ip":"192.0.2.43"}]}}`)
			case r.URL.Path == "/api/virtual_run_status/42":
				fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
			default:
				fmt.Fprint(w, `{"code":0}`)
			}
		}, true)
		f.process(t)
		var crashed int64
		if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,status,attempts,idempotency_key,locked_at,external_started_at)
VALUES($1,'sync','running',1,'nokvm-sync-crash-dispatched',now()-interval '6 minutes',clock_timestamp()) RETURNING id`, f.sid).Scan(&crashed); err != nil {
			t.Fatal(err)
		}
		if err := f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
			t.Fatal(err)
		}
		var state string
		var started *string
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT status,external_started_at::text FROM provision_jobs WHERE id=$1", crashed).Scan(&state, &started); err != nil {
			t.Fatal(err)
		}
		if state != "pending" {
			t.Fatalf("read-only NOKVM crash entered manual verification: %s", state)
		}
		if started != nil {
			t.Fatalf("read-only NOKVM job kept the dispatched marker: %v", *started)
		}
		// The service is not reported as having an unresolved external delivery.
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/credentials", f.sid), map[string]any{}, f.customer, ""), 200)
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET available_at=now() WHERE id=$1", crashed); err != nil {
			t.Fatal(err)
		}
		f.process(t)
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", crashed).Scan(&state); err != nil || state != "succeeded" {
			t.Fatalf("recovered NOKVM sync did not finish: %s %v", state, err)
		}
		if reads.Load() == 0 {
			t.Fatal("recovered sync never read upstream")
		}
	})

	t.Run("failure", func(t *testing.T) {
		var broken atomic.Bool
		f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" {
				fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"vm-42","public_ip":[{"ip":"192.0.2.42"}]}}`)
				return
			}
			if r.URL.Path == "/api/virtual_run_status/42" {
				fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
				return
			}
			if broken.Load() {
				// A malformed read cannot be interpreted, so the sync fails without
				// any upstream write having happened.
				fmt.Fprint(w, `{"code":0,"data":{"name":`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"name":"synced-42","ip_list":[{"ip":"192.0.2.43"}]}}`)
		}, true)
		f.process(t)
		broken.Store(true)
		// Queue a real sync through the same dispatch path the worker uses.
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/actions", f.sid), map[string]string{"action": "sync"}, f.customer, "nokvm-sync-read-failure"), 202)
		if _, err := f.db.Pool.Exec(t.Context(), `UPDATE provision_jobs SET available_at=now() WHERE service_id=$1 AND action='sync' AND status='pending'`, f.sid); err != nil {
			t.Fatal(err)
		}
		syncJob, err := f.db.ClaimProvisionJob(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if syncJob.Action != "sync" {
			t.Fatalf("expected sync, got %s", syncJob.Action)
		}
		if err := f.db.ProcessProvisionJob(t.Context(), *syncJob); err != nil {
			t.Fatal(err)
		}
		var state string
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", syncJob.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "failed" {
			t.Fatalf("failed read-only sync must be retryable, got %s", state)
		}
		// A failed read never blocks credential access or refunds.
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/credentials", f.sid), map[string]any{}, f.customer, ""), 200)
	})
}
