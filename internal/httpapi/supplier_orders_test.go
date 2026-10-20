package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

func TestSupplierOrdersSnapshotScopeAndPagination(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "supplier-list-root", "super_admin")
	aid, staff := user(t, db, h, "supplier-list-reader", "admin")
	owner, customer := user(t, db, h, "supplier-list-client", "customer")
	other, _ := user(t, db, h, "supplier-list-other", "customer")
	pid := product(t, db, 10)
	var supplier int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted) VALUES('old supplier','fixture','https://example.test','fixture',''::bytea) RETURNING id`).Scan(&supplier); err != nil {
		t.Fatal(err)
	}
	for index, uid := range []int64{owner, other} {
		sid, _ := billingSchedulerFixture(t, db, uid, pid, 700+index, false)
		var replacement int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode,unit_number,resource_info,delivery_snapshot) SELECT user_id,order_id,order_item_id,product_id,name,'supplier-'||id::text,billing_cycle,status,'supplier',2,'{"dedicated_ip":"203.0.113.7","password":"secret-leak"}',jsonb_build_object('supplier_id',$2::bigint) FROM services WHERE id=$1 RETURNING id`, sid, supplier).Scan(&replacement); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,status,idempotency_key,result) VALUES($1,'create','succeeded','cost-'||$1::bigint::text,'{"cost_cents":120,"currency":"CNY","password":"secret-leak"}')`, replacement); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) SELECT id,'supplier renewal','renew',1234,$2 FROM invoices WHERE order_id=(SELECT order_id FROM services WHERE id=$1)`, sid, replacement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE administrators SET customer_scope='owned' WHERE id=$1`, aid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET sales_owner_id=$1 WHERE id=$2`, aid, owner); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/admin/supplier-orders", "/api/v1/admin/supplier-renewals"} {
		expect(t, request(h, "GET", path, nil, "", ""), 401)
		expect(t, request(h, "GET", path, nil, customer, ""), 401)
		for _, tc := range []struct {
			cookie, query string
			total, count  int
		}{{root, "", 2, 2}, {staff, "", 1, 1}, {root, "?limit=1&offset=1", 2, 1}, {root, "?offset=100", 2, 0}, {root, fmt.Sprintf("?supplier_id=%d", supplier), 2, 2}, {root, "?supplier_id=999999", 0, 0}, {root, "?q=missing", 0, 0}, {root, fmt.Sprintf("?customer_id=%d", other), 1, 1}, {staff, fmt.Sprintf("?customer_id=%d", other), 0, 0}, {root, "?date_from=2099-01-01", 0, 0}} {
			response := request(h, "GET", path+tc.query, nil, tc.cookie, "")
			if tc.cookie == staff && tc.query == fmt.Sprintf("?customer_id=%d", other) {
				expect(t, response, 404)
				continue
			}
			expect(t, response, 200)
			result := data(t, response)
			if result["total"] != float64(tc.total) || len(result["items"].([]any)) != tc.count {
				t.Fatalf("%s: %v", path+tc.query, result)
			}
			if tc.query == "" && tc.count > 0 {
				first := result["items"].([]any)[0].(map[string]any)
				if strings.HasSuffix(path, "supplier-orders") {
					resources := first["supplier_resources"].([]any)
					if len(resources) != 1 || resources[0].(map[string]any)["cost_cents"] != float64(120) {
						t.Fatal("recorded create cost missing", first)
					}
				} else if first["cost_cents"] != nil || first["record_kind"] != "local_renewal_invoice" {
					t.Fatal("local renewal cost fabricated", first)
				}
			}
			if strings.Contains(response.Body.String(), "secret-leak") {
				t.Fatal("secret leaked")
			}
		}
		for _, query := range []string{"?supplier_id=bad", "?customer_id=bad", "?status=bad", "?date_from=bad", "?date_from=2026-10-12&date_to=2026-10-11"} {
			expect(t, request(h, "GET", path+query, nil, root, ""), 400)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY[]::text[] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`, aid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/supplier-orders", nil, staff, ""), 403)
}
