package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func salesAssignment(t *testing.T, h http.Handler, root string, customer int64, owner any, version int64) {
	t.Helper()
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/sales-owner", customer), map[string]any{"sales_owner_id": owner, "expected_version": version}, root, ""), 200)
}

func TestCustomerScopeCommercialBoundaries(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "sales-root", "super_admin")
	staffID, staff := user(t, db, h, "sales-staff", "admin")
	otherID, _ := user(t, db, h, "sales-other", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true,customer_scope='owned' WHERE role='admin'"); err != nil {
		t.Fatal(err)
	}
	pid := product(t, db, 12)
	type resources struct {
		customer, pending, paid, invoice, service, cancellation, contact int64
		cookie                                                           string
	}
	items := []resources{}
	for index, owner := range []any{staffID, otherID, nil} {
		id, cookie := user(t, db, h, fmt.Sprintf("sales-customer-%d", index), "customer")
		salesAssignment(t, h, root, id, owner, 1)
		r := resources{customer: id, cookie: cookie}
		for n := 0; n < 2; n++ {
			v, item := add(t, h, cookie, pid, 1)
			w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, cookie, fmt.Sprintf("sales-order-%d-%d", index, n))
			expect(t, w, 201)
			orderID := int64(data(t, w)["order_id"].(float64))
			if n == 0 {
				r.pending = orderID
				continue
			}
			r.paid, r.invoice = orderID, int64(data(t, w)["invoice_id"].(float64))
			expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", id), map[string]any{"amount_cents": 2000}, root, fmt.Sprintf("sales-credit-%d", index)), 201)
			expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", r.invoice), map[string]any{}, cookie, fmt.Sprintf("sales-pay-%d", index)), 201)
			job, err := db.ClaimProvisionJob(t.Context())
			if err != nil || job == nil {
				t.Fatal("claim", err)
			}
			if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			r.service = job.ServiceID
		}
		w := request(h, "POST", fmt.Sprintf("/api/v1/services/%d/cancellation", r.service), map[string]any{"type": "end_of_billing", "reason": "scope test"}, cookie, fmt.Sprintf("sales-cancel-%d", index))
		expect(t, w, 201)
		r.cancellation = int64(data(t, w)["id"].(float64))
		w = request(h, "POST", "/api/v1/contacts", map[string]any{"username": "Contact", "email": fmt.Sprintf("contact-%d@example.test", index)}, cookie, "")
		expect(t, w, 201)
		r.contact = int64(data(t, w)["id"].(float64))
		items = append(items, r)
	}
	for _, tc := range []struct {
		scope     string
		ownerless bool
		count     int
	}{{"owned", false, 1}, {"owned", true, 2}, {"assigned", false, 2}, {"assigned", true, 3}, {"all", false, 3}} {
		t.Run(fmt.Sprintf("%s-%t", tc.scope, tc.ownerless), func(t *testing.T) {
			if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope=$2,customer_ownerless=$3 WHERE id=$1", staffID, tc.scope, tc.ownerless); err != nil {
				t.Fatal(err)
			}
			for _, list := range []struct {
				path       string
				multiplier int
			}{{"customers", 1}, {"orders", 2}, {"invoices", 2}, {"services", 2}, {"cancellations", 1}} {
				w := request(h, "GET", "/api/v1/admin/"+list.path, nil, staff, "")
				expect(t, w, 200)
				if len(listData(t, w)) != tc.count*list.multiplier {
					t.Fatalf("%s leaked/omitted resources: %s", list.path, w.Body.String())
				}
			}
		})
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	for _, r := range items[1:] {
		customerPath := fmt.Sprintf("/api/v1/admin/customers/%d", r.customer)
		for _, operation := range []struct {
			method, path string
			body         any
			key          string
		}{
			{"PATCH", customerPath, map[string]any{"display_name": "blocked"}, ""},
			{"POST", customerPath + "/password", map[string]any{"password": "changed-password"}, ""},
			{"POST", customerPath + "/balance", map[string]any{"amount_cents": 10}, "sales-blocked-credit"},
			{"PUT", customerPath + "/group", map[string]any{"group_id": nil}, ""},
			{"GET", customerPath + "/contacts", nil, ""},
			{"POST", customerPath + "/contacts", map[string]any{"email": "sales-root@example.test"}, ""},
			{"PATCH", fmt.Sprintf("%s/contacts/%d", customerPath, r.contact), map[string]any{"email": "sales-root@example.test"}, ""},
			{"DELETE", fmt.Sprintf("%s/contacts/%d", customerPath, r.contact), nil, ""},
			{"GET", fmt.Sprintf("/api/v1/admin/orders/%d", r.pending), nil, ""},
			{"POST", fmt.Sprintf("/api/v1/admin/orders/%d/notes", r.pending), map[string]any{"notes": "blocked", "expected_version": orderVersion(t, db, r.pending)}, "sales-blocked-notes"},
			{"POST", fmt.Sprintf("/api/v1/admin/orders/%d/cancel", r.pending), map[string]any{"expected_version": orderVersion(t, db, r.pending)}, "sales-blocked-order"},
			{"POST", fmt.Sprintf("/api/v1/admin/orders/%d/review", r.paid), map[string]any{"decision": "approved", "note": "scope test", "expected_version": orderVersion(t, db, r.paid)}, "sales-blocked-review"},
			{"POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", r.invoice), map[string]any{}, "sales-blocked-refund"},
			{"GET", fmt.Sprintf("/api/v1/admin/services/%d", r.service), nil, ""},
			{"POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", r.service), map[string]any{"action": "suspend"}, "sales-blocked-action"},
			{"POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", r.service), map[string]any{"job_id": 1, "note": "scope test"}, ""},
			{"POST", fmt.Sprintf("/api/v1/admin/cancellations/%d/decision", r.cancellation), map[string]any{"decision": "approved", "note": "scope test"}, ""},
		} {
			expect(t, request(h, operation.method, operation.path, operation.body, staff, operation.key), 404)
		}
		expect(t, request(h, "GET", fmt.Sprintf("/api/v1/services/%d", r.service), nil, r.cookie, ""), 200)
	}
	for _, value := range []string{"bad", "1junk", "0", "-1"} {
		expect(t, request(h, "GET", "/api/v1/admin/customers/"+value+"/contacts", nil, staff, ""), 400)
	}
	own := items[0]
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/customers/%d/contacts/%d", own.customer, own.contact), map[string]any{"username": "Updated"}, staff, ""), 200)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/notes", own.pending), map[string]any{"notes": "allowed", "expected_version": orderVersion(t, db, own.pending)}, staff, "sales-owned-notes"), 200)
	bulk := request(h, "POST", "/api/v1/admin/orders/bulk-cancel", map[string]any{"order_ids": []int64{own.pending, items[1].pending}, "expected_versions": orderVersions(t, db, own.pending, items[1].pending)}, staff, "sales-bulk-cancel")
	expect(t, bulk, 200)
	var foreignStatus string
	if err := db.Pool.QueryRow(t.Context(), "SELECT status FROM orders WHERE id=$1", items[1].pending).Scan(&foreignStatus); err != nil || foreignStatus != "pending" {
		t.Fatalf("bulk crossed scope: %s %v", foreignStatus, err)
	}
	dashboard := request(h, "GET", "/api/v1/admin/dashboard", nil, staff, "")
	expect(t, dashboard, 200)
	if data(t, dashboard)["customers"] != float64(1) || data(t, dashboard)["unpaid_cents"] != float64(0) {
		t.Fatal("dashboard scope", dashboard.Body.String())
	}
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	period := fmt.Sprintf("?year=%d&month=%d", now.Year(), now.Month())
	income := request(h, "GET", "/api/v1/admin/reports/income"+period, nil, staff, "")
	expect(t, income, 200)
	if data(t, income)["total"].(map[string]any)["income_cents"] != float64(1333) {
		t.Fatal("income scope", income.Body.String())
	}
	products := request(h, "GET", "/api/v1/admin/reports/product-income"+period, nil, staff, "")
	expect(t, products, 200)
	if data(t, products)["products"].([]any)[0].(map[string]any)["total_amount_cents"] != float64(1333) {
		t.Fatal("product income scope")
	}
	ranking := request(h, "GET", "/api/v1/admin/reports/customer-ranking", nil, staff, "")
	expect(t, ranking, 200)
	if len(listData(t, ranking)) != 1 || int64(listData(t, ranking)[0]["user_id"].(float64)) != own.customer {
		t.Fatal("ranking scope")
	}
	daily := request(h, "GET", "/api/v1/admin/reports/new-clients"+period, nil, staff, "")
	expect(t, daily, 200)
	var clients, orders, cancellations float64
	for _, day := range data(t, daily)["days"].([]any) {
		d := day.(map[string]any)
		clients += d["new_clients"].(float64)
		orders += d["new_orders"].(float64)
		cancellations += d["cancellation_requests"].(float64)
	}
	if clients != 1 || orders != 2 || cancellations != 1 {
		t.Fatalf("daily scope: %v %v %v", clients, orders, cancellations)
	}
	// Department authorization remains independent of customer ownership.
	w := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "Independent department", "body": "scope test", "priority": "normal"}, items[1].cookie, "")
	expect(t, w, 201)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/tickets/%d", int64(data(t, w)["id"].(float64))), nil, staff, ""), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['dashboard.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/customers", nil, staff, ""), 403)
}

func TestCustomerSalesAssignmentAndStaffRevocation(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "sales-config-root", "super_admin")
	staffID, staff := user(t, db, h, "sales-config-staff", "admin")
	customerID, customer := user(t, db, h, "sales-config-customer", "customer")
	path := fmt.Sprintf("/api/v1/admin/customers/%d/sales-owner", customerID)
	input := map[string]any{"sales_owner_id": staffID, "expected_version": 1}
	expect(t, request(h, "PUT", path, map[string]any{"expected_version": 1}, root, ""), 400)
	expect(t, request(h, "PUT", path, input, staff, ""), 403)
	expect(t, request(h, "PUT", path, input, customer, ""), 403)
	expect(t, request(h, "PUT", path, input, root, ""), 409)
	for _, id := range []int64{rootID, customerID, 99999} {
		expect(t, request(h, "PUT", path, map[string]any{"sales_owner_id": id, "expected_version": 1}, root, ""), 409)
	}
	key := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "sales-config"}, root, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	expect(t, requestBearer(h, "GET", "/api/v1/admin/sales-owners", nil, secret), 403)
	expect(t, requestBearer(h, "PUT", path, input, secret), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", "/api/v1/admin/sales-owners", nil, root, "")
	expect(t, w, 200)
	if len(listData(t, w)) != 1 {
		t.Fatal("eligible directory")
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); statuses <- request(h, "PUT", path, input, root, "").Code }()
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("assignment race", counts)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	var savedOwner int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT sales_owner_id FROM users WHERE id=$1", customerID).Scan(&savedOwner); err != nil || savedOwner != staffID {
		t.Fatal("disabled owner assignment lost", err)
	}
	w = request(h, "GET", "/api/v1/admin/sales-owners", nil, root, "")
	expect(t, w, 200)
	if len(listData(t, w)) != 0 {
		t.Fatal("disabled owner eligible")
	}
	expect(t, request(h, "PUT", path, map[string]any{"sales_owner_id": staffID, "expected_version": 2}, root, ""), 409)
	salesAssignment(t, h, root, customerID, nil, 2)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='active' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	var roleID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT admin_role_id FROM users WHERE id=$1", staffID).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"username": "sales-new-staff", "email": "sales-new@example.test", "display_name": "New sales", "status": "active", "admin_role_id": roleID, "password": "integration-password"}
	w = request(h, "POST", "/api/v1/admin/staff", payload, root, "")
	expect(t, w, 201)
	id := int64(data(t, w)["id"].(float64))
	login := request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "sales-new-staff", "password": "integration-password"}, "", "")
	expect(t, login, 200)
	cookie := login.Result().Cookies()[0].String()
	me := request(h, "GET", "/api/v1/me", nil, cookie, "")
	expect(t, me, 200)
	if data(t, me)["customer_scope"] != "owned" || data(t, me)["customer_ownerless"] != false || data(t, me)["sales_enabled"] != false {
		t.Fatal("staff defaults", me.Body.String())
	}
	key = request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "sales-staff"}, cookie, "")
	expect(t, key, 201)
	secret = data(t, key)["secret"].(string)
	payload["password"] = ""
	payload["expected_version"] = 1
	payload["customer_scope"] = "assigned"
	payload["customer_ownerless"] = true
	payload["sales_enabled"] = true
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", id), payload, root, ""), 200)
	expect(t, request(h, "GET", "/api/v1/me", nil, cookie, ""), 401)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, secret), 401)
	delete(payload, "customer_scope")
	delete(payload, "customer_ownerless")
	delete(payload, "sales_enabled")
	payload["expected_version"] = 2
	payload["display_name"] = "Renamed sales"
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", id), payload, root, ""), 200)
	var scope string
	var ownerless, enabled bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT customer_scope,customer_ownerless,sales_enabled FROM users WHERE id=$1", id).Scan(&scope, &ownerless, &enabled); err != nil || scope != "assigned" || !ownerless || !enabled {
		t.Fatal("omitted scope fields lost", err)
	}
}

func TestCustomerScopeServiceReplayAfterTransfer(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "sales-service-root", "super_admin")
	staffID, staff := user(t, db, h, "sales-service-staff", "admin")
	otherID, _ := user(t, db, h, "sales-service-other", "admin")
	customerID, customer := user(t, db, h, "sales-service-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true,customer_scope='owned' WHERE role='admin'"); err != nil {
		t.Fatal(err)
	}
	salesAssignment(t, h, root, customerID, staffID, 1)
	pid := product(t, db, 3)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET provision_mode='manual',approval_required=true WHERE id=$1", pid); err != nil {
		t.Fatal(err)
	}
	v, item := add(t, h, customer, pid, 1)
	w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "sales-service-order")
	expect(t, w, 201)
	orderID, invoiceID := int64(data(t, w)["order_id"].(float64)), int64(data(t, w)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 2000}, root, "sales-service-credit"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "sales-service-pay"), 201)
	reviewPath := fmt.Sprintf("/api/v1/admin/orders/%d/review", orderID)
	review := map[string]any{"decision": "approved", "note": "review scope", "expected_version": orderVersion(t, db, orderID)}
	expect(t, request(h, "POST", reviewPath, review, staff, "sales-service-review"), 201)
	job, err := db.ClaimProvisionJob(t.Context())
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	deliveryPath := fmt.Sprintf("/api/v1/admin/services/%d/delivery", job.ServiceID)
	delivery := map[string]any{"job_id": job.ID, "upstream_id": "scope-manual-resource", "username": "manual-user", "note": "confirmed resource"}
	expect(t, request(h, "POST", deliveryPath, delivery, staff, ""), 200)
	w = request(h, "POST", fmt.Sprintf("/api/v1/services/%d/cancellation", job.ServiceID), map[string]any{"type": "end_of_billing", "reason": "scope test"}, customer, "sales-service-cancellation")
	expect(t, w, 201)
	decisionPath := fmt.Sprintf("/api/v1/admin/cancellations/%d/decision", int64(data(t, w)["id"].(float64)))
	decision := map[string]any{"decision": "declined", "note": "keep service"}
	expect(t, request(h, "POST", decisionPath, decision, staff, ""), 200)
	actionPath := fmt.Sprintf("/api/v1/admin/services/%d/actions", job.ServiceID)
	expect(t, request(h, "POST", actionPath, map[string]any{"action": "suspend"}, staff, "sales-service-suspend"), 202)
	salesAssignment(t, h, root, customerID, otherID, 2)
	expect(t, request(h, "POST", reviewPath, review, staff, "sales-service-review"), 404)
	expect(t, request(h, "POST", deliveryPath, delivery, staff, ""), 404)
	expect(t, request(h, "POST", decisionPath, decision, staff, ""), 404)
	expect(t, request(h, "POST", actionPath, map[string]any{"action": "suspend"}, staff, "sales-service-suspend"), 404)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/services/%d", job.ServiceID), nil, customer, ""), 200)
}

func TestCustomerSalesMigrationExistingAdministrators(t *testing.T) {
	db, h := testServer(t)
	staffID, _ := user(t, db, h, "sales-old-staff", "admin")
	rootID, _ := user(t, db, h, "sales-old-root", "super_admin")
	if _, err := db.Pool.Exec(t.Context(), `DROP FUNCTION admin_customer_allowed(bigint,bigint); DROP FUNCTION admin_customer_owner_allowed(bigint,bigint);
ALTER TABLE users DROP COLUMN customer_scope,DROP COLUMN customer_ownerless,DROP COLUMN sales_enabled,DROP COLUMN sales_owner_id,DROP COLUMN sales_version;
DELETE FROM schema_migrations WHERE version='033_customer_sales_scope.sql';`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{staffID, rootID} {
		var scope string
		if err := db.Pool.QueryRow(t.Context(), "SELECT customer_scope FROM users WHERE id=$1", id).Scan(&scope); err != nil || scope != "all" {
			t.Fatal("existing scope lost", err)
		}
	}
}

func TestCustomerScopeFinanceReplayAfterTransfer(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "sales-replay-root", "super_admin")
	staffID, staff := user(t, db, h, "sales-replay-staff", "admin")
	otherID, _ := user(t, db, h, "sales-replay-other", "admin")
	customerID, customer := user(t, db, h, "sales-replay-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true,customer_scope='owned' WHERE role='admin'"); err != nil {
		t.Fatal(err)
	}
	salesAssignment(t, h, root, customerID, staffID, 1)
	creditPath := fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID)
	expect(t, request(h, "POST", creditPath, map[string]any{"amount_cents": 2000}, staff, "sales-replay-credit"), 201)
	pid := product(t, db, 2)
	v, item := add(t, h, customer, pid, 1)
	w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "sales-replay-order")
	expect(t, w, 201)
	invoice := int64(data(t, w)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, customer, "sales-replay-pay"), 201)
	refundPath := fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice)
	expect(t, request(h, "POST", refundPath, map[string]any{}, staff, "sales-replay-refund"), 201)
	salesAssignment(t, h, root, customerID, otherID, 2)
	expect(t, request(h, "POST", creditPath, map[string]any{"amount_cents": 2000}, staff, "sales-replay-credit"), 404)
	expect(t, request(h, "POST", refundPath, map[string]any{}, staff, "sales-replay-refund"), 404)
	expect(t, request(h, "GET", "/api/v1/invoices", nil, customer, ""), 200)
	var balance int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", customerID).Scan(&balance); err != nil || balance != 2000 {
		t.Fatal("denied replay changed balance", err)
	}
}

func TestCustomerScopeWaitingMutationRechecksTransfer(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "sales-race-staff", "admin")
	otherID, _ := user(t, db, h, "sales-race-other", "admin")
	customerID, _ := user(t, db, h, "sales-race-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", customerID, staffID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// Use the same exclusive protocol as the assignment API, not a bypassing SQL update.
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('customer-sales:'||$1::bigint::text,0))", customerID); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		result <- request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 10}, staff, "sales-race-credit").Code
	}()
	for {
		var waiting bool
		if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'SELECT pg_advisory_xact_lock_shared%customer-sales:%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("mutation never reached shared assignment lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE users SET sales_owner_id=$2,sales_version=sales_version+1 WHERE id=$1", customerID, otherID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-result:
		if status != 404 {
			t.Fatalf("waiting mutation status=%d", status)
		}
	case <-ctx.Done():
		t.Fatal("mutation did not finish")
	}
	var count int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM balance_ledger WHERE user_id=$1", customerID).Scan(&count); err != nil || count != 0 {
		t.Fatal("mutation crossed transfer", err)
	}
}
