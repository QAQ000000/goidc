package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// The opt-in fixture uses real paid, reviewed orders and manual job completion.
// Inventory credentials are synthetic and remain outside the browser ready file.
func TestManualResourceBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "manual-resource-super", func(db *store.Store, h http.Handler) {
		staffID, staff := user(t, db, h, "manual-resource-admin", "admin")
		readerID, reader := user(t, db, h, "manual-resource-reader", "admin")
		customerID, customer := user(t, db, h, "manual-resource-customer", "customer")
		contentRole(t, db, readerID, "resources.view", "services.view", "customers.view")

		createResource := func(name, upstream, address, status, key string) int64 {
			t.Helper()
			body := manualResourceCreateBody()
			body["name"], body["upstream_id"], body["address"], body["status"] = name, upstream, address, status
			body["provider"], body["region"] = "隔离浏览器验收", "测试机房"
			body["username"], body["password"] = "fixture-only-login", "fixture-only-inventory-secret"
			body["notes"] = "仅用于隔离浏览器验收；成本与上游期限不修改客户账单。"
			created := request(h, "POST", manualResourceEndpoint, body, staff, key)
			expect(t, created, http.StatusCreated)
			return int64(data(t, created)["id"].(float64))
		}
		availableID := createResource("浏览器验收·可用服务器", "fixture-server-available", "203.0.113.121", "available", "manual-browser-create-available")
		releaseID := createResource("浏览器验收·已终止待释放", "fixture-server-release", "203.0.113.122", "available", "manual-browser-create-release")
		disabledID := createResource("浏览器验收·停用服务器", "fixture-server-disabled", "203.0.113.123", "disabled", "manual-browser-create-disabled")

		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 10000}, staff, "manual-browser-customer-balance"), http.StatusCreated)
		waitingService := func(name, key string) (int64, int64, int64) {
			t.Helper()
			productID := product(t, db, 3)
			if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET name=$2,provision_mode='manual',approval_required=true WHERE id=$1", productID, name); err != nil {
				t.Fatal(err)
			}
			version, item := add(t, h, customer, productID, 1)
			order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, customer, key+"-order")
			expect(t, order, http.StatusCreated)
			orderID := int64(data(t, order)["order_id"].(float64))
			invoiceID := int64(data(t, order)["invoice_id"].(float64))
			expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, key+"-pay"), http.StatusCreated)
			approved := request(h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/review", orderID), map[string]any{"decision": "approved", "note": "隔离验收资料审核通过", "expected_version": orderVersion(t, db, orderID)}, staff, key+"-approve")
			expect(t, approved, http.StatusCreated)
			job, err := db.ClaimProvisionJob(t.Context())
			if err != nil || job == nil || job.Action != "create" {
				t.Fatal("browser manual create job missing", err)
			}
			if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			var serviceVersion int64
			var serviceState, jobState, reviewState, invoiceState string
			if err = db.Pool.QueryRow(t.Context(), `SELECT s.version,s.status,j.status,o.review_status,i.status
 FROM services s JOIN provision_jobs j ON j.service_id=s.id JOIN orders o ON o.id=s.order_id
 JOIN invoices i ON i.order_id=o.id WHERE s.id=$1 AND j.id=$2 AND i.id=$3`, job.ServiceID, job.ID, invoiceID).Scan(&serviceVersion, &serviceState, &jobState, &reviewState, &invoiceState); err != nil {
				t.Fatal(err)
			}
			if serviceState != "provisioning" || jobState != "waiting_manual" || reviewState != "approved" || invoiceState != "paid" {
				t.Fatalf("manual browser fixture not deliverable: service=%s job=%s review=%s invoice=%s", serviceState, jobState, reviewState, invoiceState)
			}
			return job.ServiceID, job.ID, serviceVersion
		}
		waitingID, waitingJobID, _ := waitingService("浏览器验收·待库存交付", "manual-browser-waiting")
		terminatedID, createJobID, serviceVersion := waitingService("浏览器验收·已终止待回收", "manual-browser-release")
		deliveryPath := fmt.Sprintf("/api/v1/admin/services/%d/delivery", terminatedID)
		expect(t, request(h, "POST", deliveryPath, map[string]any{"job_id": createJobID, "manual_resource_id": releaseID, "resource_expected_version": 1, "expected_version": serviceVersion, "note": "隔离验收已核对实际库存交付"}, staff, ""), http.StatusOK)
		termination := request(h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", terminatedID), map[string]any{"action": "terminate"}, staff, "manual-browser-terminate")
		expect(t, termination, http.StatusAccepted)
		terminateJob, err := db.ClaimProvisionJob(t.Context())
		if err != nil || terminateJob == nil || terminateJob.Action != "terminate" || terminateJob.ID != int64(data(t, termination)["job_id"].(float64)) {
			t.Fatal("browser manual termination job missing", err)
		}
		if err = db.ProcessProvisionJob(t.Context(), *terminateJob); err != nil {
			t.Fatal(err)
		}
		expect(t, request(h, "POST", deliveryPath, map[string]any{"job_id": terminateJob.ID, "note": "隔离验收确认实际机器已终止，库存尚未释放"}, staff, ""), http.StatusOK)
		var terminatedState string
		if err = db.Pool.QueryRow(t.Context(), "SELECT status FROM services WHERE id=$1", terminatedID).Scan(&terminatedState); err != nil || terminatedState != "terminated" {
			t.Fatal("browser release fixture service was not really terminated", terminatedState, err)
		}
		bound := request(h, "GET", fmt.Sprintf("%s/%d", manualResourceEndpoint, releaseID), nil, staff, "")
		expect(t, bound, http.StatusOK)
		if data(t, bound)["service_id"] != float64(terminatedID) || data(t, bound)["binding_id"] == nil {
			t.Fatal("browser release fixture lost its real inventory binding")
		}

		// Setup used the helper's full administrator role. Browser management is
		// exercised with this explicit, narrower set as well as the super account.
		contentRole(t, db, staffID, "resources.view", "resources.manage", "services.view", "services.manage", "customers.view")
		expect(t, request(h, "GET", manualResourceEndpoint, nil, reader, ""), http.StatusOK)
		expect(t, request(h, "PATCH", fmt.Sprintf("%s/%d", manualResourceEndpoint, availableID), map[string]any{"expected_version": 1, "notes": "reader cannot change inventory"}, reader, "manual-browser-reader-denied"), http.StatusForbidden)
		t.Logf("manual browser fixture: customer=%d waiting_service=%d waiting_job=%d terminated_service=%d available_resource=%d release_resource=%d disabled_resource=%d", customerID, waitingID, waitingJobID, terminatedID, availableID, releaseID, disabledID)
	})
}
