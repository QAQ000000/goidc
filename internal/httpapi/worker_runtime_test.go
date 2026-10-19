package httpapi

import (
	"context"
	"testing"
)

func TestWorkerRuntimePermissionsAndIndependentReadiness(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "runtime-root", "super_admin")
	_, customer := user(t, db, h, "runtime-customer", "customer")
	staffID, staff := user(t, db, h, "runtime-staff", "admin")
	if _, err := db.Pool.Exec(context.Background(), "UPDATE admin_roles SET permissions=ARRAY['jobs.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/runtime", nil, "", ""), 401)
	expect(t, request(h, "GET", "/api/v1/admin/runtime", nil, customer, ""), 401)
	expect(t, request(h, "GET", "/api/v1/admin/runtime", nil, staff, ""), 403)
	w := request(h, "GET", "/api/v1/admin/runtime", nil, root, "")
	expect(t, w, 200)
	body := data(t, w)
	if body["status"] != "degraded" || len(body["missing_lanes"].([]any)) != 7 {
		t.Fatalf("missing worker falsely healthy: %s", w.Body.String())
	}
	if len(body["queues"].([]any)) == 0 {
		t.Fatal("missing queue diagnostics")
	}
	// The release controller starts workers only after this API probe succeeds.
	expect(t, request(h, "GET", "/readyz", nil, "", ""), 200)
	expect(t, request(h, "GET", "/healthz", nil, "", ""), 200)
}
