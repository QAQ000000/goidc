package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

func TestSupplierJobsScopeSecretsAndSafeRetry(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "supplier-jobs-test")
	serviceID, _ := f.purchase(t, f.pid, "supplier-jobs-purchase")
	var jobID int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM provision_jobs WHERE service_id=$1", serviceID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE provision_jobs SET action='sync',status='failed',last_error='private-error',result='{"token":"private-result"}' WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status='active',upstream_id='42' WHERE id=$1", serviceID); err != nil {
		t.Fatal(err)
	}
	jobs := request(f.h, "GET", "/api/v1/admin/supplier-jobs?status=failed&supplier_id="+fmt.Sprint(f.supplierID), nil, f.reader, "")
	expect(t, jobs, 200)
	for _, secret := range []string{"private-error", "private-result"} {
		if strings.Contains(jobs.Body.String(), secret) {
			t.Fatal("supplier secret leaked:", secret)
		}
	}
	items := data(t, jobs)["items"].([]any)
	if len(items) != 1 {
		t.Fatal("missing scoped supplier job", jobs.Body.String())
	}
	job := items[0].(map[string]any)
	if job["can_retry"] != true {
		t.Fatal("undispatched failed job not retryable", job)
	}
	expect(t, request(f.h, "GET", "/api/v1/admin/supplier-jobs?customer_id="+fmt.Sprint(f.uid+999999), nil, f.reader, ""), 200)
	expect(t, request(f.h, "GET", "/api/v1/admin/supplier-tasks", nil, f.reader, ""), 403)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/supplier-jobs/%d/retry", jobID), map[string]any{"expected_attempts": job["attempts"], "expected_updated_at": job["updated_at"]}, f.reader, "supplier-jobs-reader-denied"), 403)
	path := fmt.Sprintf("/api/v1/admin/supplier-jobs/%d/retry", jobID)
	body := map[string]any{"expected_attempts": job["attempts"], "expected_updated_at": job["updated_at"]}
	expect(t, request(f.h, "POST", path, body, f.root, "supplier-jobs-retry-key"), 200)
	replay := request(f.h, "POST", path, body, f.root, "supplier-jobs-retry-key")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true {
		t.Fatal("retry was not replayed")
	}
	expect(t, request(f.h, "POST", path, map[string]any{"expected_attempts": 99, "expected_updated_at": job["updated_at"]}, f.root, "supplier-jobs-retry-key"), 409)
	expect(t, request(f.h, "POST", path, body, f.root, "supplier-jobs-new-key"), 409)
	var state, key string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status,idempotency_key FROM provision_jobs WHERE id=$1", jobID).Scan(&state, &key); err != nil || state != "pending" || key == "" {
		t.Fatal(state, key, err)
	}
	if f.creates.Load() != 0 || f.actions.Load() != 0 {
		t.Fatal("retry invoked supplier")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET status='waiting_verification',external_started_at=now() WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	verify := request(f.h, "GET", "/api/v1/admin/supplier-jobs?status=waiting_verification", nil, f.root, "")
	expect(t, verify, 200)
	row := data(t, verify)["items"].([]any)[0].(map[string]any)
	if row["can_retry"] != false || row["verification_required"] != true {
		t.Fatal(row)
	}
	expect(t, request(f.h, "POST", path, map[string]any{"expected_attempts": row["attempts"], "expected_updated_at": row["updated_at"]}, f.root, "supplier-jobs-verify-key"), 409)
}
