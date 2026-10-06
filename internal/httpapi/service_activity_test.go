package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestServiceActivitySafeProjectionScopeAndPaging(t *testing.T) {
	db, h := testServer(t)
	ownerID, owner := user(t, db, h, "activity-owner", "customer")
	foreignID, foreign := user(t, db, h, "activity-foreign", "customer")
	_, admin := user(t, db, h, "activity-admin", "super_admin")
	scopedID, scoped := user(t, db, h, "activity-scoped", "admin")
	pid := product(t, db, 2)
	version, item := add(t, h, owner, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, owner, "activity-order")
	expect(t, order, 201)
	invoiceID := int64(data(t, order)["invoice_id"].(float64))
	var serviceID int64
	if err := db.Pool.QueryRow(context.Background(), "SELECT id FROM services WHERE user_id=$1 AND product_id=$2", ownerID, pid).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", ownerID), map[string]any{"amount_cents": 5000}, admin, "activity-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, owner, "activity-pay"), 201)
	var jobID int64
	if err := db.Pool.QueryRow(context.Background(), "SELECT id FROM provision_jobs WHERE service_id=$1", serviceID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	secret := "activity-secret-password-should-not-leak"
	if _, err := db.Pool.Exec(context.Background(), `UPDATE provision_jobs SET result=jsonb_build_object('token',$2::text,'supplier_url','https://supplier.invalid/private'),last_error=$2 WHERE id=$1`, jobID, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES
($1,'service.manual.completed','service',$2,jsonb_build_object('password',$3::text,'supplier_url','https://supplier.invalid/private','job_id',999999)),
($1,'service.manual.completed','order',$2,jsonb_build_object('collision','must not appear'))`, ownerID, fmt.Sprint(serviceID), secret); err != nil {
		t.Fatal(err)
	}
	var beforeStatus, beforeError string
	if err := db.Pool.QueryRow(context.Background(), "SELECT s.status,j.last_error FROM services s JOIN provision_jobs j ON j.id=$2 WHERE s.id=$1", serviceID, jobID).Scan(&beforeStatus, &beforeError); err != nil {
		t.Fatal(err)
	}

	activity := request(h, "GET", fmt.Sprintf("/api/v1/services/%d/activity?limit=1", serviceID), nil, owner, "")
	expect(t, activity, 200)
	activityBody := activity.Body.String()
	if strings.Contains(activityBody, secret) || strings.Contains(activityBody, "supplier.invalid") || strings.Contains(activityBody, "last_error") || strings.Contains(activityBody, "request_payload") || strings.Contains(activityBody, "result") || strings.Contains(activityBody, "collision") {
		t.Fatalf("unsafe activity projection: %s", activityBody)
	}
	activityData := data(t, activity)
	if activityData["total"] != float64(2) || len(activityData["items"].([]any)) != 1 {
		t.Fatalf("unexpected deduplicated total/page: %s", activityBody)
	}
	pageTwo := request(h, "GET", fmt.Sprintf("/api/v1/services/%d/activity?limit=1&offset=1", serviceID), nil, owner, "")
	expect(t, pageTwo, 200)
	second := data(t, pageTwo)["items"].([]any)[0].(map[string]any)
	if second["source"] != "job" || second["action"] != "create" || second["actor_kind"] != "system" {
		t.Fatalf("queued event was not merged safely: %s", pageTwo.Body.String())
	}
	if second["attempts"] != float64(0) {
		t.Fatalf("job attempts were not surfaced safely: %s", pageTwo.Body.String())
	}
	filtered := request(h, "GET", fmt.Sprintf("/api/v1/services/%d/activity?action=create&status=pending", serviceID), nil, owner, "")
	expect(t, filtered, 200)
	if data(t, filtered)["total"] != float64(1) {
		t.Fatalf("action/status filter mismatch: %s", filtered.Body.String())
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/services/%d/activity?limit=0", serviceID), nil, owner, ""), 400)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/services/%d/activity?action=unknown", serviceID), nil, owner, ""), 400)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/services/%d/activity", serviceID), nil, foreign, ""), 404)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/services/%d/activity", serviceID), nil, admin, ""), 200)

	if _, err := db.Pool.Exec(context.Background(), "UPDATE users SET customer_scope='owned' WHERE id=$1", scopedID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", ownerID, scopedID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/services/%d/activity", serviceID), nil, scoped, ""), 200)
	if _, err := db.Pool.Exec(context.Background(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", ownerID, foreignID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/services/%d/activity", serviceID), nil, scoped, ""), 404)
	if _, err := db.Pool.Exec(context.Background(), "UPDATE users SET status='disabled' WHERE id=$1", ownerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/services/%d/activity", serviceID), nil, owner, ""), 401)
	var afterStatus, afterError string
	if err := db.Pool.QueryRow(context.Background(), "SELECT s.status,j.last_error FROM services s JOIN provision_jobs j ON j.id=$2 WHERE s.id=$1", serviceID, jobID).Scan(&afterStatus, &afterError); err != nil {
		t.Fatal(err)
	}
	if beforeStatus != afterStatus || beforeError != afterError {
		t.Fatalf("activity read changed service/job state: before=%s/%s after=%s/%s", beforeStatus, beforeError, afterStatus, afterError)
	}
}
