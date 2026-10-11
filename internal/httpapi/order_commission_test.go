package httpapi

import (
	"strings"
	"testing"
	"time"
)

func TestAdminOrderCommissionPreviewUsesSalesRulesAndScope(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "order-commission-root", "super_admin")
	sellerID, seller := user(t, db, h, "order-commission-seller", "admin")
	contentRole(t, db, sellerID, "orders.view", "sales.view")
	ordersOnlyID, ordersOnly := user(t, db, h, "order-commission-orders-only", "admin")
	contentRole(t, db, ordersOnlyID, "orders.view")
	var groupID int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO sales_commission_groups(name,purchase_rate_bps,active) VALUES('订单提成测试组',500,true) RETURNING id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO sales_commission_tiers(threshold_cents,extra_rate_bps,active) VALUES(6000,100,true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET sales_enabled=true,customer_scope='owned',customer_ownerless=false,sales_commission_group_id=$2 WHERE id=$1`, sellerID, groupID); err != nil {
		t.Fatal(err)
	}
	customerID, _ := user(t, db, h, "order-commission-customer", "customer")
	foreignCustomerID, _ := user(t, db, h, "order-commission-foreign", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", customerID, sellerID); err != nil {
		t.Fatal(err)
	}
	makeOrder := func(name string, ownerID int64, amount int64, status string) int64 {
		t.Helper()
		orderStatus := "pending"
		var paidAt any
		if status == "paid" {
			orderStatus = "active"
			paidAt = time.Now()
		}
		var orderID int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash)
VALUES($1,$2,$3,$4,$1,'order-commission-fixture') RETURNING id`, name, ownerID, orderStatus, amount).Scan(&orderID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,idempotency_key,paid_at)
VALUES($1,$2,$3,$4,$5,$1,$6)`, "INV-"+name, ownerID, orderID, status, amount, paidAt); err != nil {
			t.Fatal(err)
		}
		return orderID
	}
	paidOrder := makeOrder("ORDER-COMMISSION-PAID", customerID, 5000, "paid")
	makeOrder("ORDER-COMMISSION-TURNOVER", customerID, 2000, "paid")
	unpaidOrder := makeOrder("ORDER-COMMISSION-UNPAID", customerID, 3000, "unpaid")
	foreignOrder := makeOrder("ORDER-COMMISSION-FOREIGN", foreignCustomerID, 5000, "paid")

	preview := request(h, "POST", "/api/v1/admin/orders/commission", map[string]any{"order_ids": []int64{paidOrder, unpaidOrder}}, seller, "")
	expect(t, preview, 200)
	result := data(t, preview)
	items := result["items"].([]any)
	if len(items) != 2 || result["total_commission_cents"] != float64(300) {
		t.Fatalf("commission preview totals: %s", preview.Body.String())
	}
	byID := map[int64]map[string]any{}
	for _, value := range items {
		item := value.(map[string]any)
		byID[int64(item["order_id"].(float64))] = item
	}
	paid := byID[paidOrder]
	if paid["status"] != "calculated" || paid["basis_cents"] != float64(5000) || paid["purchase_rate_bps"] != float64(500) || paid["tier_extra_rate_bps"] != float64(100) || paid["monthly_turnover_cents"] != float64(7000) || paid["commission_cents"] != float64(300) {
		t.Fatalf("paid order commission breakdown: %#v", paid)
	}
	unpaid := byID[unpaidOrder]
	if unpaid["status"] != "not_paid" || unpaid["basis_cents"] != float64(0) || unpaid["commission_cents"] != float64(0) {
		t.Fatalf("unpaid order got a commission: %#v", unpaid)
	}
	ordersOnlyPreview := request(h, "POST", "/api/v1/admin/orders/commission", map[string]any{"order_ids": []int64{paidOrder}}, ordersOnly, "")
	expect(t, ordersOnlyPreview, 403)
	expect(t, request(h, "POST", "/api/v1/admin/orders/commission", map[string]any{"order_ids": []int64{foreignOrder}}, seller, ""), 404)
	rootPreview := request(h, "POST", "/api/v1/admin/orders/commission", map[string]any{"order_ids": []int64{foreignOrder}}, root, "")
	expect(t, rootPreview, 200)
	if item := data(t, rootPreview)["items"].([]any)[0].(map[string]any); item["status"] != "unassigned" || item["commission_cents"] != float64(0) {
		t.Fatalf("unassigned order preview: %#v", item)
	}
	expect(t, request(h, "POST", "/api/v1/admin/orders/commission", map[string]any{"order_ids": []int64{paidOrder, paidOrder}}, seller, ""), 400)
	expect(t, request(h, "POST", "/api/v1/admin/orders/commission", map[string]any{"order_ids": []int64{0}}, seller, ""), 400)
	empty := request(h, "POST", "/api/v1/admin/orders/commission", map[string]any{"order_ids": []int64{}}, seller, "")
	expect(t, empty, 200)
	if len(data(t, empty)["items"].([]any)) != 0 {
		t.Fatalf("empty preview: %s", empty.Body.String())
	}
}

func TestAdminOrderCommissionPreviewRejectsOversizedRequest(t *testing.T) {
	db, h := testServer(t)
	id, cookie := user(t, db, h, "order-commission-limit", "admin")
	contentRole(t, db, id, "orders.view", "sales.view")
	ids := make([]int64, 101)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	response := request(h, "POST", "/api/v1/admin/orders/commission", map[string]any{"order_ids": ids}, cookie, "")
	expect(t, response, 400)
	if !strings.Contains(response.Body.String(), `"code":"invalid_order_ids"`) {
		t.Fatalf("request limit code: %s", response.Body.String())
	}
}
