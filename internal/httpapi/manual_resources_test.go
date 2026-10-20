package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

const manualResourceEndpoint = "/api/v1/admin/manual-resources"

func TestManualResourceQueryValidation(t *testing.T) {
	for _, query := range []string{
		"unknown=1", "customer_id=1", "password=1", "q=a&q=b", "q=%zz", "q=%ff", "q=" + strings.Repeat("x", 121),
		"status=", "status=assigned", "status=available&status=retired", "binding=", "binding=all", "binding=bound&binding=unbound",
		"limit=", "limit=0", "limit=101", "limit=+1", "limit=%2B1", "limit=1.0", "limit=999999999999999999999",
		"offset=", "offset=-1", "offset=1x", "offset=1&offset=2",
	} {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			if _, ok := manualResourceQuery(w, httptest.NewRequest("GET", manualResourceEndpoint+"?"+query, nil), true); ok || w.Code != 400 {
				t.Fatal("invalid inventory query accepted", w.Code, w.Body.String())
			}
		})
	}
	for _, query := range []string{"", "q=", "q=%20test%20&status=disabled&binding=unbound&limit=100&offset=200", "status=available", "status=retired&binding=bound"} {
		w := httptest.NewRecorder()
		filter, ok := manualResourceQuery(w, httptest.NewRequest("GET", manualResourceEndpoint+"?"+query, nil), true)
		if !ok || filter.Limit < 1 || filter.Limit > 100 || filter.Offset < 0 {
			t.Fatal("valid inventory query rejected", query, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	if _, ok := manualResourceQuery(w, httptest.NewRequest("GET", manualResourceEndpoint+"/1?q=hidden", nil), false); ok || w.Code != 400 {
		t.Fatal("detail or mutation query unexpectedly accepted")
	}
}

func manualResourceCreateBody() map[string]any {
	return map[string]any{
		"name": "manual-inventory-fixture", "resource_type": "server", "provider": "fixture-provider", "region": "test-region",
		"address": "203.0.113.117", "upstream_id": "manual-inventory-117", "cost_cents": 1200,
		"upstream_expires_at": "2027-01-01T00:00:00+08:00", "status": "available", "notes": "operator-only-note",
		"username": "private-login", "password": "private-inventory-password",
	}
}

func createManualResourceHTTP(t *testing.T, h http.Handler, cookie, key string) int64 {
	t.Helper()
	out := request(h, "POST", manualResourceEndpoint, manualResourceCreateBody(), cookie, key)
	expect(t, out, 201)
	if out.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("inventory response is cacheable")
	}
	return int64(data(t, out)["id"].(float64))
}

func TestManualResourceInventoryCRUDReplayAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	actor, root := user(t, db, h, "manual-resource-crud-root", "super_admin")
	body := manualResourceCreateBody()
	created := request(h, "POST", manualResourceEndpoint, body, root, "manual-resource-create-key")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	path := fmt.Sprintf("%s/%d", manualResourceEndpoint, id)
	replay := request(h, "POST", manualResourceEndpoint, body, root, "manual-resource-create-key")
	expect(t, replay, 200)
	if data(t, replay)["id"] != float64(id) || data(t, replay)["replayed"] != true {
		t.Fatal("resource creation did not recover original intent", replay.Body.String())
	}
	body["cost_cents"] = 1300
	policyErrorCode(t, request(h, "POST", manualResourceEndpoint, body, root, "manual-resource-create-key"), 409, "idempotency_conflict")
	for _, query := range []string{"", "?q=manual-inventory-fixture&status=available&binding=unbound&limit=1&offset=0"} {
		out := request(h, "GET", manualResourceEndpoint+query, nil, root, "")
		expect(t, out, 200)
		page := data(t, out)
		if page["total"] != float64(1) || len(page["items"].([]any)) != 1 || strings.Contains(out.Body.String(), "private-login") || strings.Contains(out.Body.String(), "private-inventory-password") {
			t.Fatal("inventory pagination or credential privacy mismatch", out.Body.String())
		}
	}
	detail := request(h, "GET", path, nil, root, "")
	expect(t, detail, 200)
	if strings.Contains(detail.Body.String(), "private-login") || strings.Contains(detail.Body.String(), "private-inventory-password") || data(t, detail)["has_credentials"] != true {
		t.Fatal("inventory detail must only expose credential presence", detail.Body.String())
	}
	policyErrorCode(t, request(h, "PATCH", path, map[string]any{"notes": "missing version"}, root, "manual-resource-no-version"), 409, "manual_resource_version_required")
	for _, field := range []string{"status", "resource_type"} {
		policyErrorCode(t, request(h, "PATCH", path, map[string]any{"expected_version": 1, field: ""}, root, "manual-resource-empty-"+field), 400, "invalid_manual_resource")
	}
	update := map[string]any{"expected_version": 1, "cost_cents": 2300, "upstream_expires_at": "", "notes": "updated resource metadata"}
	saved := request(h, "PATCH", path, update, root, "manual-resource-update-key")
	expect(t, saved, 200)
	if data(t, saved)["version"] != float64(2) {
		t.Fatal("resource update did not advance CAS", saved.Body.String())
	}
	expect(t, request(h, "PATCH", path, update, root, "manual-resource-update-key"), 200)
	update["notes"] = "changed intent"
	policyErrorCode(t, request(h, "PATCH", path, update, root, "manual-resource-update-key"), 409, "idempotency_conflict")
	policyErrorCode(t, request(h, "PATCH", path, update, root, "manual-resource-stale-key"), 409, "manual_resource_version_conflict")
	// The entire resource row, audit and unified receipts must survive a failed
	// audit unchanged, including the secret ciphertext and version timestamps.
	snapshot := func() string {
		t.Helper()
		var out string
		if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'resource',(SELECT to_jsonb(r) FROM manual_resources r WHERE id=$1),
 'audit',(SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id),'[]'::jsonb) FROM audit_events a WHERE action LIKE 'manual_resource.%'),
 'receipts',(SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY scope,idempotency_key),'[]'::jsonb) FROM mutation_operations m WHERE actor_id=$2))::text`, id, actor).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot()
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_manual_resource_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'manual_resource.%' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_manual_resource_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_manual_resource_audit()`); err != nil {
		t.Fatal(err)
	}
	failBody := map[string]any{"expected_version": 2, "password": "replacement-secret", "cost_cents": 4500}
	expect(t, request(h, "PATCH", path, failBody, root, "manual-resource-audit-key"), 500)
	if snapshot() != before {
		t.Fatal("failed inventory audit left changed resource or receipt")
	}
	if _, err := db.Pool.Exec(t.Context(), "DROP TRIGGER reject_manual_resource_audit ON audit_events"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PATCH", path, failBody, root, "manual-resource-audit-key"), 200)
	expect(t, request(h, "PATCH", path, failBody, root, "manual-resource-audit-key"), 200)
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM manual_resources").Scan(&count); err != nil || count != 1 {
		t.Fatal("inventory retry duplicated resource", count, err)
	}
}

func TestManualResourceInventoryPermissionsScopeAndCredentialRevocation(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "manual-resource-auth-root", "super_admin")
	staffID, staff := user(t, db, h, "manual-resource-auth-staff", "admin")
	_, customer := user(t, db, h, "manual-resource-auth-customer", "customer")
	id := createManualResourceHTTP(t, h, root, "manual-resource-auth-create")
	path := fmt.Sprintf("%s/%d", manualResourceEndpoint, id)
	for _, cookie := range []string{"", customer} {
		expect(t, request(h, "GET", manualResourceEndpoint, nil, cookie, ""), 401)
		expect(t, request(h, "POST", manualResourceEndpoint, manualResourceCreateBody(), cookie, "manual-resource-forbidden"), 401)
	}
	keyResult := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "manual-resource-revoked-key"}, root, "")
	expect(t, keyResult, 201)
	secret := data(t, keyResult)["secret"].(string)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1", tokenHash(secret)); err != nil {
		t.Fatal(err)
	}
	expect(t, requestBearer(h, "GET", path, nil, secret), 401)
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(root))); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", path, nil, root, ""), 401)
	contentRole(t, db, staffID, []string{}...)
	expect(t, request(h, "GET", path, nil, staff, ""), 403)
	contentRole(t, db, staffID, "resources.view")
	expect(t, request(h, "GET", path, nil, staff, ""), 200)
	expect(t, request(h, "PATCH", path, map[string]any{"expected_version": 1, "notes": "forbidden"}, staff, "manual-resource-forbidden"), 403)
	contentRole(t, db, staffID, "resources.view", "resources.manage")
	for _, headers := range []map[string]string{{"X-Requested-With": ""}, {"Origin": "https://invalid.example"}} {
		expect(t, requestFrom(h, "PATCH", path, map[string]any{"expected_version": 1, "notes": "cross-site"}, "192.0.2.23:4567", staff, headers), 403)
	}
	expect(t, request(h, "PATCH", path, map[string]any{"expected_version": 1, "notes": "missing stable key"}, staff, ""), 400)
	expect(t, request(h, "PATCH", path+"?q=hidden", map[string]any{"expected_version": 1, "notes": "query"}, staff, "manual-resource-query"), 400)
	expect(t, request(h, "PATCH", path, map[string]any{"expected_version": 1, "unknown_field": true}, staff, "manual-resource-unknown"), 400)
	for _, query := range []string{"status=", "binding=", "customer_id=1", "limit=-1", "q=" + url.QueryEscape(strings.Repeat("x", 121))} {
		expect(t, request(h, "GET", manualResourceEndpoint+"?"+query, nil, staff, ""), 400)
	}
	// A credential accepted by the HTTP wrapper must be checked again after a
	// real row-lock wait. Natural expiry advances while the shared credential
	// lock correctly prevents a concurrent revocation from racing the write.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE sessions SET expires_at=clock_timestamp()+interval '2 seconds' WHERE token_hash=$1", tokenHash(cookieValue(staff))); err != nil {
		t.Fatal(err)
	}
	blocker, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), "SELECT id FROM manual_resources WHERE id=$1 FOR UPDATE", id); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request(h, "PATCH", path, map[string]any{"expected_version": 1, "notes": "revoked while waiting"}, staff, "manual-resource-revocation")
	}()
	waitUploadBlockedBy(t, db, blocker.Conn().PgConn().PID())
	deadline := time.Now().Add(5 * time.Second)
	for {
		var expired bool
		if err = db.Pool.QueryRow(t.Context(), "SELECT clock_timestamp()>=expires_at FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(staff))).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inventory credential fixture did not expire")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	expect(t, awaitBalanceResponse(t, done), 401)
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM manual_resources WHERE id=$1", id).Scan(&version); err != nil || version != 1 {
		t.Fatal("revoked inventory operation changed resource", version, err)
	}
}

func TestManualResourceDeliveryBindingAndRelease(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "manual-resource-delivery-root", "super_admin")
	staffID, staff := user(t, db, h, "manual-resource-delivery-staff", "admin")
	owner, customer := user(t, db, h, "manual-resource-delivery-customer", "customer")
	_, otherCustomer := user(t, db, h, "manual-resource-delivery-other", "customer")
	resourceID := createManualResourceHTTP(t, h, root, "manual-resource-delivery-create")
	resourcePath := fmt.Sprintf("%s/%d", manualResourceEndpoint, resourceID)
	contentRole(t, db, staffID, "resources.view", "resources.manage", "services.view", "services.manage")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, owner); err != nil {
		t.Fatal(err)
	}
	// Unbound global inventory is not a back door around a restricted sales
	// scope. Bound inventory becomes visible only through its actual customer.
	expect(t, request(h, "GET", resourcePath, nil, staff, ""), 404)
	expect(t, request(h, "POST", manualResourceEndpoint, manualResourceCreateBody(), staff, "manual-resource-scoped-create"), 403)
	pid := product(t, db, 5)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET provision_mode='manual' WHERE id=$1", pid); err != nil {
		t.Fatal(err)
	}
	version, item := add(t, h, customer, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, customer, "manual-resource-service-order")
	expect(t, order, 201)
	invoiceID := int64(data(t, order)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", owner), map[string]any{"amount_cents": 10000}, root, "manual-resource-service-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "manual-resource-service-pay"), 201)
	job, err := db.ClaimProvisionJob(t.Context())
	if err != nil || job == nil {
		t.Fatal("manual inventory service job missing", err)
	}
	if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	var serviceVersion int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT version FROM services WHERE id=$1", job.ServiceID).Scan(&serviceVersion); err != nil {
		t.Fatal(err)
	}
	deliveryPath := fmt.Sprintf("/api/v1/admin/services/%d/delivery", job.ServiceID)
	body := map[string]any{"job_id": job.ID, "manual_resource_id": resourceID, "resource_expected_version": 1, "expected_version": serviceVersion, "note": "verified physical inventory delivery"}
	snapshot := func() string {
		t.Helper()
		var out string
		if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'resource',(SELECT to_jsonb(r) FROM manual_resources r WHERE id=$1),
 'bindings',(SELECT coalesce(jsonb_agg(to_jsonb(b) ORDER BY id),'[]'::jsonb) FROM manual_resource_bindings b),
 'service',(SELECT to_jsonb(s) FROM services s WHERE id=$2),
 'job',(SELECT to_jsonb(j) FROM provision_jobs j WHERE id=$3),
 'events',(SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY id),'[]'::jsonb) FROM notification_events e),
 'deliveries',(SELECT coalesce(jsonb_agg(to_jsonb(d) ORDER BY id),'[]'::jsonb) FROM notification_deliveries d),
 'email',(SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY id),'[]'::jsonb) FROM email_outbox e),
 'sms',(SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY id),'[]'::jsonb) FROM sms_outbox s),
 'audit',(SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id),'[]'::jsonb) FROM audit_events a))::text`, resourceID, job.ServiceID, job.ID).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	beforeRestrictedBind := snapshot()
	policyErrorCode(t, request(h, "POST", deliveryPath, body, staff, ""), 404, "not_found")
	if after := snapshot(); after != beforeRestrictedBind {
		t.Fatal("restricted inventory binding changed resource, service, job, audit or notification facts")
	}
	missing := map[string]any{"job_id": job.ID, "manual_resource_id": resourceID, "note": "versions omitted"}
	expect(t, request(h, "POST", deliveryPath, missing, root, ""), 409)
	// A caller cannot replace the credentials selected from the inventory row.
	override := map[string]any{"job_id": job.ID, "manual_resource_id": resourceID, "resource_expected_version": 1, "expected_version": serviceVersion, "upstream_id": "forged-resource", "note": "overridden inventory"}
	expect(t, request(h, "POST", deliveryPath, override, root, ""), 409)
	completed := request(h, "POST", deliveryPath, body, root, "")
	expect(t, completed, 200)
	replayed := request(h, "POST", deliveryPath, body, root, "")
	expect(t, replayed, 200)
	if data(t, replayed)["replayed"] != true {
		t.Fatal("inventory delivery did not replay the same job intent", replayed.Body.String())
	}
	changed := map[string]any{"job_id": job.ID, "manual_resource_id": resourceID, "resource_expected_version": 1, "expected_version": serviceVersion, "note": "different delivery evidence"}
	policyErrorCode(t, request(h, "POST", deliveryPath, changed, root, ""), 409, "delivery_conflict")
	detail := request(h, "GET", resourcePath, nil, staff, "")
	expect(t, detail, 200)
	resourceVersion := int64(data(t, detail)["version"].(float64))
	if data(t, detail)["service_id"] != float64(job.ServiceID) || data(t, detail)["customer_id"] != float64(owner) {
		t.Fatal("bound inventory did not use the service customer", detail.Body.String())
	}
	credentialsPath := fmt.Sprintf("/api/v1/services/%d/credentials", job.ServiceID)
	credentials := request(h, "POST", credentialsPath, map[string]any{}, customer, "")
	expect(t, credentials, 200)
	if data(t, credentials)["username"] != "private-login" || data(t, credentials)["password"] != "private-inventory-password" || credentials.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("delivered credentials were not scoped to the real service")
	}
	expect(t, request(h, "POST", credentialsPath, map[string]any{}, otherCustomer, ""), 404)
	expect(t, request(h, "PATCH", resourcePath, map[string]any{"expected_version": resourceVersion, "password": "unsafe-replacement"}, root, "manual-resource-bound-edit"), 409)
	releaseBody := map[string]any{"expected_version": resourceVersion, "note": "verified resource recovery"}
	policyErrorCode(t, request(h, "POST", resourcePath+"/release", releaseBody, root, "manual-resource-release"), 409, "manual_resource_release_requires_termination")
	if _, err = db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", owner); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", resourcePath, nil, staff, ""), 404)
	// Exercise the real manual lifecycle: asking to terminate and processing
	// the job retain the last known service state and inventory occupation until
	// an operator confirms completion through the existing delivery endpoint.
	termination := request(h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", job.ServiceID), map[string]any{"action": "terminate"}, root, "manual-resource-terminate")
	expect(t, termination, 202)
	terminateJob, err := db.ClaimProvisionJob(t.Context())
	if err != nil || terminateJob == nil || terminateJob.ID != int64(data(t, termination)["job_id"].(float64)) {
		t.Fatal("manual termination job was not claimed", err)
	}
	if err = db.ProcessProvisionJob(t.Context(), *terminateJob); err != nil {
		t.Fatal(err)
	}
	var serviceState, jobState string
	if err = db.Pool.QueryRow(t.Context(), "SELECT s.status,j.status FROM services s JOIN provision_jobs j ON j.service_id=s.id WHERE j.id=$1", terminateJob.ID).Scan(&serviceState, &jobState); err != nil || serviceState != "active" || jobState != "waiting_manual" {
		t.Fatal("manual termination changed service before confirmation", serviceState, jobState, err)
	}
	policyErrorCode(t, request(h, "POST", resourcePath+"/release", releaseBody, root, "manual-resource-release"), 409, "manual_resource_release_requires_termination")
	expect(t, request(h, "POST", deliveryPath, map[string]any{"job_id": terminateJob.ID, "note": "operator confirmed actual resource termination"}, root, ""), 200)
	detail = request(h, "GET", resourcePath, nil, root, "")
	expect(t, detail, 200)
	if data(t, detail)["service_id"] != float64(job.ServiceID) || data(t, detail)["version"] != float64(resourceVersion) {
		t.Fatal("manual termination automatically released resource", detail.Body.String())
	}
	expect(t, request(h, "POST", credentialsPath, map[string]any{}, customer, ""), 404)
	expect(t, request(h, "POST", resourcePath+"/release", releaseBody, root, "manual-resource-release"), 200)
	expect(t, request(h, "POST", resourcePath+"/release", releaseBody, root, "manual-resource-release"), 200)
	changedRelease := map[string]any{"expected_version": resourceVersion, "note": "different recovery evidence"}
	policyErrorCode(t, request(h, "POST", resourcePath+"/release", changedRelease, root, "manual-resource-release"), 409, "idempotency_conflict")
	detail = request(h, "GET", resourcePath, nil, root, "")
	expect(t, detail, 200)
	if data(t, detail)["service_id"] != nil || data(t, detail)["binding_id"] != nil {
		t.Fatal("released inventory still exposes an active binding", detail.Body.String())
	}
}
