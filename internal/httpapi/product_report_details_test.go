package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

func TestProductIncomeGroupsQuantitiesFiltersPaginationAndScope(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "product-group-staff", "admin")
	uid, _ := user(t, db, h, "product-group-client", "customer")
	foreign, _ := user(t, db, h, "product-group-foreign", "customer")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID)
	exec("UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, uid)
	var category int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO product_categories(name,slug,sort_order,active) VALUES('历史分类','report-category',1,false) RETURNING id").Scan(&category); err != nil {
		t.Fatal(err)
	}
	first, second, hidden := product(t, db, 100), product(t, db, 100), product(t, db, 100)
	exec("UPDATE products SET category_id=$1,active=false WHERE id=$2", category, first)
	makeOrder := func(owner int64, key string, products []int64, quantities []int, amounts []int64, net int64) {
		t.Helper()
		var order int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES($1,$2,'paid',$3,$1,'fixture') RETURNING id`, key, owner, net).Scan(&order); err != nil {
			t.Fatal(err)
		}
		for n, pid := range products {
			exec(`INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,$3,'monthly',$4,100,0,$5)`, order, pid, fmt.Sprintf("历史商品%d", pid), quantities[n], amounts[n])
		}
		exec(`INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at) VALUES($1,$2,$3,'paid',$4,'2024-02-01T00:00:00+08')`, key, owner, order, net)
	}
	makeOrder(uid, "product-group-visible", []int64{first, second}, []int{3, 1}, []int64{300, 100}, 399)
	makeOrder(foreign, "product-group-invisible", []int64{hidden}, []int{9}, []int64{900}, 900)
	path := "/api/v1/admin/reports/product-income/groups?year=2024&month=2"
	response := request(h, "GET", path+"&limit=1", nil, staff, "")
	expect(t, response, 200)
	result := data(t, response)
	if result["total"] != float64(2) || len(result["groups"].([]any)) != 1 {
		t.Fatalf("classification pagination: %s", response.Body.String())
	}
	summary := result["summary"].(map[string]any)
	if summary["total_amount_cents"] != float64(399) || summary["new_quantity"] != float64(4) {
		t.Fatalf("summary must cover full filtered set: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), fmt.Sprintf("历史商品%d", hidden)) {
		t.Fatal("foreign customer product leaked")
	}
	response = request(h, "GET", path+fmt.Sprintf("&product_id=%d", first), nil, staff, "")
	expect(t, response, 200)
	result = data(t, response)
	rows := result["groups"].([]any)
	item := rows[0].(map[string]any)["products"].([]any)[0].(map[string]any)
	if item["new_order_amount_cents"] != float64(299) || item["new_quantity"] != float64(3) || item["new_order_count"] != float64(1) {
		t.Fatalf("filter reallocated discount or counted units as orders: %s", response.Body.String())
	}
	if rows[0].(map[string]any)["category_name"] != "历史分类" {
		t.Fatal("inactive historical category missing")
	}
	response = request(h, "GET", path+"&offset=100", nil, staff, "")
	expect(t, response, 200)
	if data(t, response)["total"] != float64(2) || len(data(t, response)["groups"].([]any)) != 0 {
		t.Fatal("empty last page lost total")
	}
	response = request(h, "GET", path+"&limit=1&format=csv", nil, staff, "")
	expect(t, response, 400)
	if errorData(t, response)["code"] != "invalid_report_format" || response.Header().Get("Content-Disposition") != "" {
		t.Fatalf("removed export returned a download: %s", response.Body.String())
	}
	for _, filter := range []string{"category_id=-1", "product_id=0", "product_id=nope"} {
		expect(t, request(h, "GET", path+"&"+filter, nil, staff, ""), 400)
	}
	expect(t, request(h, "GET", path, nil, "", ""), 401)
	exec("UPDATE admin_roles SET permissions=array_remove(permissions,'reports.view') WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID)
	expect(t, request(h, "GET", path+"&format=csv", nil, staff, ""), 403)
}

func TestProductIncomeQuantitiesPreserveRenewalItemsAndCreditUnits(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "product-quantity-customer", "customer")
	adminID, admin := user(t, db, h, "product-quantity-admin", "super_admin")
	pid := product(t, db, 100)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var order, item, service int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash) VALUES('quantity-order',$1,100,'quantity-order','fixture') RETURNING id`, uid).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'quantity','monthly',1,100,0,100) RETURNING id`, order, pid).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle) VALUES($1,$2,$3,$4,'quantity','quantity.test','monthly') RETURNING id`, uid, order, item, pid).Scan(&service); err != nil {
		t.Fatal(err)
	}
	var renewal, credit int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES('quantity-renew',$1,'paid',200,'2024-02-15T00:00:00+08') RETURNING id`, uid).Scan(&renewal); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		exec(`INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) VALUES($1,'renew','renew',100,$2)`, renewal, service)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES('quantity-credit',$1,'paid',500,'2024-01-15T00:00:00+08') RETURNING id`, uid).Scan(&credit); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) VALUES($1,'credit','service',500,$2)`, credit, service)
	exec(`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key) VALUES($1,$2,500,0,1,'quantity-credit')`, credit, uid)
	for n := 0; n < 2; n++ {
		exec(`INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after) VALUES($1,$2,$3,250,$4,'2024-02-16T00:00:00+08',$4,1,2,0)`, credit, uid, adminID, fmt.Sprintf("quantity-credit-%d", n))
	}
	response := request(h, "GET", "/api/v1/admin/reports/product-income?year=2024&month=2", nil, admin, "")
	expect(t, response, 200)
	rows := data(t, response)["products"].([]any)
	if len(rows) != 1 {
		t.Fatalf("unexpected products: %s", response.Body.String())
	}
	row := rows[0].(map[string]any)
	for field, want := range map[string]float64{"new_order_amount_cents": 500, "new_order_count": 1, "new_quantity": 1, "renewal_amount_cents": 200, "renewal_order_count": 1, "renewal_quantity": 2} {
		if row[field] != want {
			t.Fatalf("%s=%v want %v: %s", field, row[field], want, response.Body.String())
		}
	}
}
