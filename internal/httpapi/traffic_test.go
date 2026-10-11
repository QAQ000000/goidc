package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func trafficService(t *testing.T, db *store.Store, h http.Handler, customer string, pid int64) int64 {
	t.Helper()
	v, item := add(t, h, customer, pid, 1)
	w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "traffic-service-create")
	expect(t, w, 201)
	oid := int64(data(t, w)["order_id"].(float64))
	var id int64
	if err := db.Pool.QueryRow(t.Context(), "UPDATE services SET status='active' WHERE order_id=$1 RETURNING id", oid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTrafficConcurrentIdempotency(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "traffic-concurrent-root", "super_admin")
	in := map[string]any{"name": "幂等流量包", "capacity_gb": 10, "price_cents": 100, "active": true, "stock": 1, "applicable_product_ids": []int64{}}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- request(h, "POST", "/api/v1/admin/traffic-products", in, root, "traffic-concurrent-create").Code
		}()
	}
	wg.Wait()
	close(codes)
	count := map[int]int{}
	for code := range codes {
		count[code]++
	}
	if count[201] != 1 || count[200] != 1 {
		t.Fatal(count)
	}
	var rows int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM traffic_products WHERE name='幂等流量包'").Scan(&rows); err != nil || rows != 1 {
		t.Fatal(rows, err)
	}
}

func TestTrafficPaymentRefundManualVerificationAndWebhook(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "traffic-pay-root", "super_admin")
	uid, customer := user(t, db, h, "traffic-pay-customer", "customer")
	pid := product(t, db, 20)
	sid := trafficService(t, db, h, customer, pid)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=50000 WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=now()+interval '12 days' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	var due string
	if err := db.Pool.QueryRow(t.Context(), "SELECT next_due_at::text FROM services WHERE id=$1", sid).Scan(&due); err != nil {
		t.Fatal(err)
	}
	created := request(h, "POST", "/api/v1/admin/traffic-products", map[string]any{"name": "支付链路流量包", "capacity_gb": 100, "price_cents": 5000, "active": true, "stock": 3, "applicable_product_ids": []int64{pid}}, root, "traffic-pay-package")
	expect(t, created, 201)
	tid := int64(data(t, created)["id"].(float64))
	purchasePath := fmt.Sprintf("/api/v1/services/%d/traffic-orders", sid)
	buy := func(version int, key string) (int64, int64) {
		w := request(h, "POST", purchasePath, map[string]any{"traffic_product_id": tid, "expected_version": version}, customer, key)
		expect(t, w, 201)
		return int64(data(t, w)["id"].(float64)), int64(data(t, w)["invoice_id"].(float64))
	}
	orderID, invoiceID := buy(1, "traffic-payment-one")
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "traffic-balance-pay"), 201)
	var fulfillment string
	if err := db.Pool.QueryRow(t.Context(), "SELECT fulfillment_status FROM traffic_orders WHERE id=$1", orderID).Scan(&fulfillment); err != nil || fulfillment != "waiting_manual" {
		t.Fatal(fulfillment, err)
	}
	refundPath := fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoiceID)
	expect(t, request(h, "POST", refundPath, map[string]any{}, root, "traffic-refund-one"), 201)
	expect(t, request(h, "POST", refundPath, map[string]any{}, root, "traffic-refund-one"), 200)
	var stock, sold int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT stock,sold_count FROM traffic_products WHERE id=$1", tid).Scan(&stock, &sold); err != nil || stock != 3 || sold != 0 {
		t.Fatal(stock, sold, err)
	}
	orderID, invoiceID = buy(3, "traffic-payment-two")
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "traffic-balance-pay-two"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/traffic-orders/%d/delivery-verification", orderID), map[string]any{"expected_version": 2, "note": "已在隔离资源端核验测试交付", "acknowledged": true}, root, "traffic-verify-two"), 200)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoiceID), map[string]any{}, root, "traffic-refund-fulfilled"), 409)
	_, invoiceID = buy(4, "traffic-payment-three")
	intent := request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoiceID), map[string]string{"provider": "mock"}, customer, "traffic-mock-intent")
	expect(t, intent, 201)
	body, _ := json.Marshal(map[string]any{"event_id": "traffic-event-three", "payment_id": data(t, intent)["provider_payment_id"], "invoice_id": invoiceID, "amount_cents": 5000, "status": "succeeded"})
	expect(t, signedWebhook(h, body, paymentSignature(body)), 201)
	var finalDue string
	if err := db.Pool.QueryRow(t.Context(), "SELECT next_due_at::text FROM services WHERE id=$1", sid).Scan(&finalDue); err != nil || finalDue != due {
		t.Fatal("traffic payment changed due date", due, finalDue, err)
	}
}
func TestTrafficProductsCRUDVersionsPermissionsAndDeleteProtection(t *testing.T) {
	db, h := testServer(t)
	readerID, reader := user(t, db, h, "traffic-reader", "admin")
	writerID, writer := user(t, db, h, "traffic-writer", "admin")
	_, customer := user(t, db, h, "traffic-customer", "customer")
	contentRole(t, db, readerID, "products.view")
	contentRole(t, db, writerID, "products.view", "products.manage")
	pid := product(t, db, 20)
	sid := trafficService(t, db, h, customer, pid)
	in := map[string]any{"name": "基础流量包", "capacity_gb": 50, "price_cents": 1999, "active": true, "stock": 5, "applicable_product_ids": []int64{pid}}
	expect(t, request(h, "POST", "/api/v1/admin/traffic-products", in, reader, "traffic-read-write"), 403)
	created := request(h, "POST", "/api/v1/admin/traffic-products", in, writer, "traffic-product-create")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	page := request(h, "GET", "/api/v1/admin/traffic-products?q=基础&active=true&limit=10&offset=0", nil, reader, "")
	expect(t, page, 200)
	if data(t, page)["total"] != float64(1) {
		t.Fatal(data(t, page))
	}
	item := data(t, page)["items"].([]any)[0].(map[string]any)
	if item["price_cents"] != float64(1999) || item["applicable_product_ids"].([]any)[0] != float64(pid) {
		t.Fatal(item)
	}
	update := map[string]any{"name": "基础流量包升级", "capacity_gb": 80, "price_cents": 2999, "active": true, "stock": 4, "applicable_product_ids": []int64{pid}, "expected_version": 1}
	path := fmt.Sprintf("/api/v1/admin/traffic-products/%d", id)
	expect(t, request(h, "PATCH", path, update, writer, "traffic-product-update"), 200)
	expect(t, request(h, "PATCH", path, update, writer, "traffic-product-update-stale"), 409)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/services/%d/traffic-orders", sid), map[string]any{"traffic_product_id": id, "expected_version": 2}, customer, "traffic-purchase-delete-protection"), 201)
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": 3}, writer, "traffic-product-delete"), 409)
	update["active"] = false
	update["expected_version"] = 3
	update["stock"] = 3
	expect(t, request(h, "PATCH", path, update, writer, "traffic-product-disable"), 200)
}

func TestTrafficOrdersPurchaseListCancelAndPermissions(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "traffic-order-root", "super_admin")
	_, customer := user(t, db, h, "traffic-order-customer", "customer")
	_, other := user(t, db, h, "traffic-order-other", "customer")
	pid := product(t, db, 20)
	sid := trafficService(t, db, h, customer, pid)
	created := request(h, "POST", "/api/v1/admin/traffic-products", map[string]any{"name": "订单流量包", "capacity_gb": 100, "price_cents": 5000, "active": true, "stock": 1, "applicable_product_ids": []int64{pid}}, root, "traffic-order-package")
	expect(t, created, 201)
	tid := int64(data(t, created)["id"].(float64))
	purchasePath := fmt.Sprintf("/api/v1/services/%d/traffic-orders", sid)
	in := map[string]any{"traffic_product_id": tid, "expected_version": 1}
	expect(t, request(h, "POST", purchasePath, in, other, "traffic-other-purchase"), 404)
	expect(t, request(h, "GET", "/api/v1/admin/traffic-orders", nil, customer, ""), 403)
	buy := request(h, "POST", purchasePath, in, customer, "traffic-real-purchase")
	expect(t, buy, 201)
	expect(t, request(h, "POST", purchasePath, in, customer, "traffic-real-purchase"), 200)
	orderID := int64(data(t, buy)["id"].(float64))
	invoiceID := int64(data(t, buy)["invoice_id"].(float64))
	expect(t, request(h, "POST", purchasePath, in, customer, "traffic-stock-exhausted"), 409)
	page := request(h, "GET", "/api/v1/admin/traffic-orders?q=订单流量包&status=pending", nil, root, "")
	expect(t, page, 200)
	if data(t, page)["total"] != float64(1) {
		t.Fatal(data(t, page))
	}
	cancelPath := fmt.Sprintf("/api/v1/admin/traffic-orders/%d/cancel", orderID)
	expect(t, request(h, "POST", cancelPath, map[string]any{"expected_version": 2}, root, "traffic-cancel-stale"), 409)
	expect(t, request(h, "POST", cancelPath, map[string]any{"expected_version": 1}, root, "traffic-cancel-real"), 200)
	expect(t, request(h, "POST", cancelPath, map[string]any{"expected_version": 1}, root, "traffic-cancel-real"), 200)
	var status string
	var stock, sold int64
	if err := db.Pool.QueryRow(context.Background(), "SELECT status FROM invoices WHERE id=$1", invoiceID).Scan(&status); err != nil || status != "cancelled" {
		t.Fatal(status, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT stock,sold_count FROM traffic_products WHERE id=$1", tid).Scan(&stock, &sold); err != nil || stock != 1 || sold != 0 {
		t.Fatal(stock, sold, err)
	}
	detail := request(h, "GET", fmt.Sprintf("/api/v1/admin/traffic-orders/%d", orderID), nil, root, "")
	expect(t, detail, http.StatusOK)
	if data(t, detail)["status"] != "cancelled" {
		t.Fatal(data(t, detail))
	}
}

// Purchases share the unique customer/key scope even across services. One
// concurrent request must win, while the other returns a conflict, not a 500.
func TestTrafficPurchaseIdempotencyAcrossServices(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "traffic-key-root", "super_admin")
	uid, customer := user(t, db, h, "traffic-key-customer", "customer")
	pid := product(t, db, 20)
	var serviceIDs []int64
	for range 2 {
		v, item := add(t, h, customer, pid, 1)
		w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, fmt.Sprintf("traffic-key-service-%d", len(serviceIDs)))
		expect(t, w, 201)
		var sid int64
		if err := db.Pool.QueryRow(t.Context(), "UPDATE services SET status='active' WHERE order_id=$1 RETURNING id", int64(data(t, w)["order_id"].(float64))).Scan(&sid); err != nil {
			t.Fatal(err)
		}
		serviceIDs = append(serviceIDs, sid)
	}
	created := request(h, "POST", "/api/v1/admin/traffic-products", map[string]any{"name": "并发跨服务流量包", "capacity_gb": 10, "price_cents": 100, "active": true, "stock": 2}, root, "traffic-key-package")
	expect(t, created, 201)
	tid := int64(data(t, created)["id"].(float64))
	in := map[string]any{"traffic_product_id": tid, "expected_version": 1}
	key := strings.Repeat("k", 128)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, sid := range serviceIDs {
		wg.Add(1)
		go func(sid int64) {
			defer wg.Done()
			codes <- request(h, "POST", fmt.Sprintf("/api/v1/services/%d/traffic-orders", sid), in, customer, key).Code
		}(sid)
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	var winningService, rows, invoices, stock, sold int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT min(service_id),count(*) FROM traffic_orders WHERE user_id=$1 AND idempotency_key=$2", uid, key).Scan(&winningService, &rows); err != nil || rows != 1 {
		t.Fatal(rows, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoices WHERE user_id=$1 AND idempotency_key LIKE 'traffic:%'", uid).Scan(&invoices); err != nil || invoices != 1 {
		t.Fatal(invoices, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT stock,sold_count FROM traffic_products WHERE id=$1", tid).Scan(&stock, &sold); err != nil || stock != 1 || sold != 1 {
		t.Fatal(stock, sold, err)
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/services/%d/traffic-orders", winningService), in, customer, key), 200)
	in["traffic_product_id"] = tid + 1
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/services/%d/traffic-orders", winningService), in, customer, key), 409)
}

func TestTrafficCancellationReplayRespectsRevokedCustomerScope(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "traffic-scope-root", "super_admin")
	staffID, staff := user(t, db, h, "traffic-scope-staff", "admin")
	uid, customer := user(t, db, h, "traffic-scope-customer", "customer")
	contentRole(t, db, staffID, "orders.view", "orders.manage", "customers.view")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true,customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	salesAssignment(t, h, root, uid, staffID, 1)
	pid := product(t, db, 20)
	sid := trafficService(t, db, h, customer, pid)
	created := request(h, "POST", "/api/v1/admin/traffic-products", map[string]any{"name": "范围撤权测试流量包", "capacity_gb": 10, "price_cents": 100, "active": true, "stock": 1}, root, "traffic-scope-package")
	expect(t, created, 201)
	tid := int64(data(t, created)["id"].(float64))
	purchase := request(h, "POST", fmt.Sprintf("/api/v1/services/%d/traffic-orders", sid), map[string]any{"traffic_product_id": tid, "expected_version": 1}, customer, "traffic-scope-purchase")
	expect(t, purchase, 201)
	oid := int64(data(t, purchase)["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/traffic-orders/%d/cancel", oid)
	in := map[string]any{"expected_version": 1}
	expect(t, request(h, "POST", path, in, staff, "traffic-scope-cancel"), 200)
	expect(t, request(h, "POST", path, in, staff, "traffic-scope-cancel"), 200)
	salesAssignment(t, h, root, uid, nil, 2)
	expect(t, request(h, "POST", path, in, staff, "traffic-scope-cancel"), 404)
	var stock, sold, audits int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT stock,sold_count FROM traffic_products WHERE id=$1", tid).Scan(&stock, &sold); err != nil || stock != 1 || sold != 0 {
		t.Fatal(stock, sold, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='traffic_order.cancelled' AND entity_id=$1", fmt.Sprint(oid)).Scan(&audits); err != nil || audits != 1 {
		t.Fatal(audits, err)
	}
}
