package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func orderVersion(t *testing.T, db *store.Store, id int64) int64 {
	t.Helper()
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM orders WHERE id=$1", id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func orderVersions(t *testing.T, db *store.Store, ids ...int64) map[int64]int64 {
	t.Helper()
	versions := make(map[int64]int64, len(ids))
	for _, id := range ids {
		versions[id] = orderVersion(t, db, id)
	}
	return versions
}

type mutationOrderFixture struct {
	db                             *store.Store
	h                              http.Handler
	root, customer, staff          string
	customerID, staffID, productID int64
}

func newMutationOrderFixture(t *testing.T) mutationOrderFixture {
	t.Helper()
	db, h := testServer(t)
	_, root := user(t, db, h, "order-mutation-root", "super_admin")
	uid, customer := user(t, db, h, "order-mutation-customer", "customer")
	staffID, staff := user(t, db, h, "order-mutation-staff", "admin")
	pid := product(t, db, 12)
	return mutationOrderFixture{db: db, h: h, root: root, customer: customer, staff: staff, customerID: uid, staffID: staffID, productID: pid}
}

func (f mutationOrderFixture) order(t *testing.T, key string, quantity int) (int64, int64) {
	t.Helper()
	v, item := add(t, f.h, f.customer, f.productID, quantity)
	w := request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, f.customer, key)
	expect(t, w, 201)
	return int64(data(t, w)["order_id"].(float64)), int64(data(t, w)["invoice_id"].(float64))
}

func (f mutationOrderFixture) paidReviewOrder(t *testing.T, key string) (int64, int64) {
	t.Helper()
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET approval_required=true WHERE id=$1", f.productID); err != nil {
		t.Fatal(err)
	}
	id, invoice := f.order(t, key, 1)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.customerID), map[string]any{"amount_cents": 5000}, f.root, key+"-credit"), 201)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, key+"-payment"), 201)
	return id, invoice
}

func TestOrderMutationsNotesAndCancellationVersionIdempotency(t *testing.T) {
	f := newMutationOrderFixture(t)
	id, invoice := f.order(t, "mutation-cancel-order", 2)
	path := fmt.Sprintf("/api/v1/admin/orders/%d", id)
	v := orderVersion(t, f.db, id)
	expect(t, request(f.h, "POST", path+"/notes", map[string]any{"notes": "missing version"}, f.root, "mutation-missing-version"), 409)
	expect(t, request(f.h, "POST", path+"/notes", map[string]any{"notes": "missing key", "expected_version": v}, f.root, ""), 409)
	expect(t, request(f.h, "POST", path+"/cancel", map[string]any{}, f.root, "mutation-missing-cancel-version"), 409)
	body := map[string]any{"notes": "核对订单", "expected_version": v}
	notes := request(f.h, "POST", path+"/notes", body, f.root, "mutation-notes-key")
	expect(t, notes, 200)
	next := orderVersion(t, f.db, id)
	if next != v+1 {
		t.Fatalf("notes version=%d want=%d", next, v+1)
	}
	replay := request(f.h, "POST", path+"/notes", body, f.root, "mutation-notes-key")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true {
		t.Fatal("notes replay flag missing")
	}
	expect(t, request(f.h, "POST", path+"/notes", map[string]any{"notes": "different", "expected_version": v}, f.root, "mutation-notes-key"), 409)
	expect(t, request(f.h, "POST", path+"/notes", map[string]any{"notes": "stale", "expected_version": v}, f.root, "mutation-stale-notes"), 409)
	expect(t, request(f.h, "POST", path+"/cancel", map[string]any{"expected_version": v}, f.root, "mutation-stale-cancel"), 409)
	var status, storedNotes string
	var stock int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT o.status,o.notes,p.stock FROM orders o CROSS JOIN products p WHERE o.id=$1 AND p.id=$2", id, f.productID).Scan(&status, &storedNotes, &stock); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || storedNotes != "核对订单" || stock != 10 || orderVersion(t, f.db, id) != next {
		t.Fatalf("stale changed state status=%s notes=%s stock=%d", status, storedNotes, stock)
	}
	cancelBody := map[string]any{"expected_version": next}
	cancel := request(f.h, "POST", path+"/cancel", cancelBody, f.root, "mutation-cancel-key")
	expect(t, cancel, 201)
	if data(t, cancel)["stock_units_released"] != float64(2) {
		t.Fatalf("stock release: %s", cancel.Body.String())
	}
	replayedCancel := request(f.h, "POST", path+"/cancel", cancelBody, f.root, "mutation-cancel-key")
	expect(t, replayedCancel, 200)
	if data(t, replayedCancel)["replayed"] != true {
		t.Fatal("cancel replay flag missing")
	}
	expect(t, request(f.h, "POST", path+"/cancel", map[string]any{"expected_version": next + 1}, f.root, "mutation-cancel-key"), 409)
	expect(t, request(f.h, "POST", path+"/cancel", map[string]any{"expected_version": orderVersion(t, f.db, id)}, f.root, "mutation-cancel-new-key"), 409)
	var auditCount int
	var invoiceStatus string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT p.stock,i.status,(SELECT count(*) FROM audit_events WHERE entity_id=$3 AND action='order.cancelled') FROM products p CROSS JOIN invoices i WHERE p.id=$1 AND i.id=$2", f.productID, invoice, fmt.Sprint(id)).Scan(&stock, &invoiceStatus, &auditCount); err != nil {
		t.Fatal(err)
	}
	if stock != 12 || invoiceStatus != "cancelled" || auditCount != 1 {
		t.Fatalf("cancel state stock=%d invoice=%s audits=%d", stock, invoiceStatus, auditCount)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE entity_id=$1 AND action='order.notes.updated'", fmt.Sprint(id)).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("notes replay appended %d audit records", auditCount)
	}
}

func TestOrderMutationsReviewStaleAndReplay(t *testing.T) {
	f := newMutationOrderFixture(t)
	id, _ := f.paidReviewOrder(t, "mutation-review-order")
	path := fmt.Sprintf("/api/v1/admin/orders/%d/review", id)
	v := orderVersion(t, f.db, id)
	expect(t, request(f.h, "POST", path, map[string]any{"decision": "approved", "note": "missing version"}, f.root, "mutation-review-missing"), 409)
	expect(t, request(f.h, "POST", path, map[string]any{"decision": "approved", "note": "stale", "expected_version": v - 1}, f.root, "mutation-review-stale"), 409)
	var review string
	var jobs int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT review_status,(SELECT count(*) FROM provision_jobs WHERE service_id IN (SELECT id FROM services WHERE order_id=$1)) FROM orders WHERE id=$1", id).Scan(&review, &jobs); err != nil {
		t.Fatal(err)
	}
	if review != "pending" || jobs != 0 || orderVersion(t, f.db, id) != v {
		t.Fatalf("stale review changed state review=%s jobs=%d", review, jobs)
	}
	body := map[string]any{"decision": "approved", "note": "审核通过", "expected_version": v}
	expect(t, request(f.h, "POST", path, body, f.root, "mutation-review-key"), 201)
	replay := request(f.h, "POST", path, body, f.root, "mutation-review-key")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true {
		t.Fatal("review replay flag missing")
	}
	expect(t, request(f.h, "POST", path, map[string]any{"decision": "rejected", "note": "审核通过", "expected_version": v}, f.root, "mutation-review-key"), 409)
	expect(t, request(f.h, "POST", path, map[string]any{"decision": "approved", "expected_version": orderVersion(t, f.db, id)}, f.root, "mutation-review-new-key"), 409)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id IN (SELECT id FROM services WHERE order_id=$1)", id).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("review created %d jobs", jobs)
	}
	var auditCount int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE entity_id=$1 AND action='order.review.approved'", fmt.Sprint(id)).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("review replay appended %d audit records", auditCount)
	}
}

func TestOrderMutationsDeleteRetainsHistoryAndRejectsPaid(t *testing.T) {
	f := newMutationOrderFixture(t)
	id, invoice := f.order(t, "mutation-delete-pending", 1)
	path := fmt.Sprintf("/api/v1/admin/orders/%d", id)
	v := orderVersion(t, f.db, id)
	expect(t, request(f.h, "DELETE", path, map[string]any{"reason": "缺少版本"}, f.root, "mutation-delete-missing-version"), 409)
	expect(t, request(f.h, "DELETE", path, map[string]any{"reason": "缺少幂等键", "expected_version": v}, f.root, ""), 409)
	expect(t, request(f.h, "DELETE", path, map[string]any{"reason": "重复订单", "expected_version": v + 1}, f.root, "mutation-delete-stale"), 409)
	if orderVersion(t, f.db, id) != v {
		t.Fatal("stale deletion changed version")
	}
	body := map[string]any{"reason": "重复订单", "expected_version": v}
	expect(t, request(f.h, "DELETE", path, body, f.root, "mutation-delete-key"), 200)
	replay := request(f.h, "DELETE", path, body, f.root, "mutation-delete-key")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true {
		t.Fatal("delete replay flag missing")
	}
	expect(t, request(f.h, "DELETE", path, map[string]any{"reason": "不同原因", "expected_version": v}, f.root, "mutation-delete-key"), 409)
	var deleted bool
	var orderRows, invoiceRows, itemRows, serviceRows, stock int
	var invoiceStatus string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT deleted_at IS NOT NULL,(SELECT count(*) FROM orders WHERE id=$1),(SELECT count(*) FROM invoices WHERE id=$2),(SELECT count(*) FROM order_items WHERE order_id=$1),(SELECT count(*) FROM services WHERE order_id=$1),(SELECT stock FROM products WHERE id=$3),(SELECT status FROM invoices WHERE id=$2) FROM orders WHERE id=$1`, id, invoice, f.productID).Scan(&deleted, &orderRows, &invoiceRows, &itemRows, &serviceRows, &stock, &invoiceStatus); err != nil {
		t.Fatal(err)
	}
	if !deleted || orderRows != 1 || invoiceRows != 1 || itemRows != 1 || serviceRows != 1 || stock != 12 || invoiceStatus != "cancelled" {
		t.Fatalf("deletion removed history deleted=%t orders=%d invoices=%d items=%d services=%d stock=%d invoice=%s", deleted, orderRows, invoiceRows, itemRows, serviceRows, stock, invoiceStatus)
	}
	var auditCount int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE entity_id=$1 AND action='order.deleted'", fmt.Sprint(id)).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("delete replay appended %d audit records", auditCount)
	}
	expect(t, request(f.h, "GET", path, nil, f.root, ""), 404)
	for _, listPath := range []string{"/api/v1/admin/orders", "/api/v1/orders"} {
		cookie := f.root
		if listPath == "/api/v1/orders" {
			cookie = f.customer
		}
		w := request(f.h, "GET", listPath, nil, cookie, "")
		expect(t, w, 200)
		for _, row := range listData(t, w) {
			if row["id"] == float64(id) {
				t.Fatalf("deleted order visible in %s", listPath)
			}
		}
	}
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/invoices/%d", invoice), nil, f.root, ""), 200)
	paid, _ := f.paidReviewOrder(t, "mutation-delete-paid")
	expect(t, request(f.h, "DELETE", fmt.Sprintf("/api/v1/admin/orders/%d", paid), map[string]any{"reason": "不能删除已付", "expected_version": orderVersion(t, f.db, paid)}, f.root, "mutation-delete-paid-key"), 409)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT deleted_at IS NOT NULL FROM orders WHERE id=$1", paid).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Fatal("paid order deleted")
	}
}

func TestOrderMutationsDeferredNoInvoiceCanCancelDelete(t *testing.T) {
	f := newMutationOrderFixture(t)
	for _, action := range []string{"cancel", "delete"} {
		var id int64
		if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash) VALUES('DEFERRED-'||gen_random_uuid()::text,$1,0,$2,'deferred-fixture') RETURNING id`, f.customerID, "deferred-"+action).Scan(&id); err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/api/v1/admin/orders/%d", id)
		method := "DELETE"
		body := map[string]any{"expected_version": orderVersion(t, f.db, id), "reason": "取消未出账单订单"}
		if action == "cancel" {
			method = "POST"
			path += "/cancel"
			delete(body, "reason")
		}
		w := request(f.h, method, path, body, f.root, "mutation-deferred-"+action)
		if action == "cancel" {
			expect(t, w, 201)
		} else {
			expect(t, w, 200)
		}
		var status string
		var invoices int
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT status,(SELECT count(*) FROM invoices WHERE order_id=$1) FROM orders WHERE id=$1", id).Scan(&status, &invoices); err != nil {
			t.Fatal(err)
		}
		if status != "cancelled" || invoices != 0 {
			t.Fatalf("deferred state=%s invoices=%d", status, invoices)
		}
	}
}

func TestOrderMutationsReplayRespectsRevokedPermissionAndScope(t *testing.T) {
	f := newMutationOrderFixture(t)
	id, _ := f.order(t, "mutation-scope-order", 1)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true,customer_scope='owned',customer_ownerless=false WHERE id=$1", f.staffID); err != nil {
		t.Fatal(err)
	}
	salesAssignment(t, f.h, f.root, f.customerID, f.staffID, 1)
	path := fmt.Sprintf("/api/v1/admin/orders/%d/notes", id)
	body := map[string]any{"notes": "销售备注", "expected_version": orderVersion(t, f.db, id)}
	expect(t, request(f.h, "POST", path, body, f.staff, "mutation-scope-notes"), 200)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['orders.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", f.staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, body, f.staff, "mutation-scope-notes"), 403)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['orders.view','orders.manage'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", f.staffID); err != nil {
		t.Fatal(err)
	}
	salesAssignment(t, f.h, f.root, f.customerID, nil, 2)
	expect(t, request(f.h, "POST", path, body, f.staff, "mutation-scope-notes"), 404)
	current := orderVersion(t, f.db, id)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/cancel", id), map[string]any{"expected_version": current}, f.staff, "mutation-scope-cancel"), 404)
	expect(t, request(f.h, "DELETE", fmt.Sprintf("/api/v1/admin/orders/%d", id), map[string]any{"expected_version": current, "reason": "无权删除订单"}, f.staff, "mutation-scope-delete"), 404)
	if orderVersion(t, f.db, id) != current {
		t.Fatal("revoked scope changed order")
	}
}

func TestOrderMutationsBulkReviewDeleteAndPartialConflict(t *testing.T) {
	f := newMutationOrderFixture(t)
	first, _ := f.paidReviewOrder(t, "mutation-bulk-review-one")
	second, _ := f.paidReviewOrder(t, "mutation-bulk-review-two")
	versions := orderVersions(t, f.db, first, second)
	versions[second]--
	body := map[string]any{"order_ids": []int64{first, second}, "expected_versions": versions, "decision": "approved", "note": "批量审核"}
	w := request(f.h, "POST", "/api/v1/admin/orders/bulk-review", body, f.root, "mutation-bulk-review-key")
	expect(t, w, 200)
	if data(t, w)["succeeded"] != float64(1) || data(t, w)["failed"] != float64(1) {
		t.Fatalf("bulk review result %s", w.Body.String())
	}
	var review string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT review_status FROM orders WHERE id=$1", second).Scan(&review); err != nil {
		t.Fatal(err)
	}
	if review != "pending" {
		t.Fatal("stale bulk order reviewed")
	}
	deleteOne, _ := f.order(t, "mutation-bulk-delete-one", 1)
	deleteTwo, _ := f.order(t, "mutation-bulk-delete-two", 1)
	deleteVersions := orderVersions(t, f.db, deleteOne, deleteTwo)
	deleteVersions[deleteTwo]++
	deleteBody := map[string]any{"order_ids": []int64{deleteOne, deleteTwo}, "expected_versions": deleteVersions, "reason": "批量删除重复订单"}
	del := request(f.h, "POST", "/api/v1/admin/orders/bulk-delete", deleteBody, f.root, "mutation-bulk-delete-key")
	expect(t, del, 200)
	if data(t, del)["succeeded"] != float64(1) || data(t, del)["failed"] != float64(1) {
		t.Fatalf("bulk delete result %s", del.Body.String())
	}
	var deleted bool
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT deleted_at IS NOT NULL FROM orders WHERE id=$1", deleteTwo).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Fatal("stale bulk order deleted")
	}
	replay := request(f.h, "POST", "/api/v1/admin/orders/bulk-delete", deleteBody, f.root, "mutation-bulk-delete-key")
	expect(t, replay, 200)
	if data(t, replay)["succeeded"] != float64(1) || data(t, replay)["failed"] != float64(1) {
		t.Fatalf("bulk replay result %s", replay.Body.String())
	}
}

func TestOrderMutationsBulkPayloadBindingAndDuplicatePreflight(t *testing.T) {
	f := newMutationOrderFixture(t)
	first, _ := f.order(t, "mutation-batch-bind-one", 1)
	second, _ := f.order(t, "mutation-batch-bind-two", 1)
	third, _ := f.order(t, "mutation-batch-bind-three", 1)
	firstVersion := orderVersion(t, f.db, first)
	secondVersion := orderVersion(t, f.db, second)
	thirdVersion := orderVersion(t, f.db, third)
	duplicate := map[string]any{"order_ids": []int64{first, second, second}, "expected_versions": orderVersions(t, f.db, first, second)}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders/bulk-cancel", duplicate, f.root, "mutation-batch-duplicate"), 409)
	if orderVersion(t, f.db, first) != firstVersion || orderVersion(t, f.db, second) != secondVersion {
		t.Fatal("duplicate batch partially mutated before rejection")
	}
	body := map[string]any{"order_ids": []int64{first}, "expected_versions": map[int64]int64{first: firstVersion}}
	w := request(f.h, "POST", "/api/v1/admin/orders/bulk-cancel", body, f.root, "mutation-batch-bound-key")
	expect(t, w, 200)
	if data(t, w)["succeeded"] != float64(1) {
		t.Fatalf("batch result %s", w.Body.String())
	}
	replay := request(f.h, "POST", "/api/v1/admin/orders/bulk-cancel", body, f.root, "mutation-batch-bound-key")
	expect(t, replay, 200)
	if data(t, replay)["succeeded"] != float64(1) {
		t.Fatalf("batch replay %s", replay.Body.String())
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders/bulk-cancel", map[string]any{"order_ids": []int64{first, third}, "expected_versions": map[int64]int64{first: firstVersion, third: thirdVersion}}, f.root, "mutation-batch-bound-key"), 409)
	if orderVersion(t, f.db, third) != thirdVersion {
		t.Fatal("changed batch payload mutated newly added order")
	}

	deleteBody := map[string]any{"order_ids": []int64{second}, "expected_versions": map[int64]int64{second: secondVersion}, "reason": "批量删除重复订单"}
	expect(t, request(f.h, "POST", "/api/v1/admin/orders/bulk-delete", deleteBody, f.root, "mutation-batch-delete-bound"), 200)
	expect(t, request(f.h, "POST", "/api/v1/admin/orders/bulk-delete", map[string]any{"order_ids": []int64{third}, "expected_versions": map[int64]int64{third: thirdVersion}, "reason": "不同批次原因"}, f.root, "mutation-batch-delete-bound"), 409)
	if orderVersion(t, f.db, third) != thirdVersion {
		t.Fatal("changed delete batch mutated new order")
	}
}
