package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

func TestAdminResourcesPageContractAndPermission(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "resources-contract-root", "super_admin")
	_, customer := user(t, db, h, "resources-contract-customer", "customer")
	adminID, resourceViewer := user(t, db, h, "resources-contract-viewer", "admin")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['resources.view']::text[] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`, adminID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/resources", nil, "", ""), 401)
	expect(t, request(h, "GET", "/api/v1/admin/resources", nil, customer, ""), 401)
	for _, path := range []string{"/api/v1/admin/resources?supplier_id=bad", "/api/v1/admin/resources?status=unknown"} {
		expect(t, request(h, "GET", path, nil, root, ""), 400)
	}
	w := request(h, "GET", "/api/v1/admin/resources?q=missing&ip=203.0.113.10&limit=1&offset=0", nil, root, "")
	expect(t, w, 200)
	result := data(t, w)
	if result["total"].(float64) != 0 || len(result["items"].([]any)) != 0 {
		t.Fatalf("unexpected empty resource result: %v", result)
	}
	if _, ok := result["suppliers"].([]any); !ok {
		t.Fatalf("suppliers filter options missing: %v", result)
	}
	expect(t, request(h, "GET", "/api/v1/admin/resources", nil, resourceViewer, ""), 200)
	owner, _ := user(t, db, h, "resource-owned-client", "customer")
	foreign, _ := user(t, db, h, "resource-foreign-client", "customer")
	pid := product(t, db, 10)
	sid, _ := billingSchedulerFixture(t, db, owner, pid, 901, false)
	billingSchedulerFixture(t, db, foreign, pid, 902, false)
	var supplierID int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted) VALUES('snapshot supplier','fixture','https://example.test','fixture',''::bytea) RETURNING id`).Scan(&supplierID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode,unit_number,resource_info,delivery_snapshot) SELECT user_id,order_id,order_item_id,product_id,name,'snapshot.example',billing_cycle,status,provision_mode,2,'{"ips":["203.0.113.11"],"password":"secret-leak","token":"secret-token"}',jsonb_build_object('supplier_id',$2::bigint) FROM services WHERE id=$1`, sid, supplierID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `DELETE FROM services WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE administrators SET customer_scope='owned' WHERE id=$1;`, adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET sales_owner_id=$1 WHERE id=$2`, adminID, owner); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		total int
	}{
		{"", 1}, {"?ip=203.0.113.11", 1}, {"?ip=secret-leak", 0}, {fmt.Sprintf("?supplier_id=%d", supplierID), 1}, {"?offset=100", 1},
	} {
		response := request(h, "GET", "/api/v1/admin/resources"+tc.query, nil, resourceViewer, "")
		expect(t, response, 200)
		result := data(t, response)
		if result["total"] != float64(tc.total) {
			t.Fatalf("%s: %v", tc.query, result)
		}
		if strings.Contains(response.Body.String(), "secret-") {
			t.Fatal("sensitive resource field leaked")
		}
		if tc.query == "" {
			items := result["items"].([]any)
			if len(items) != 1 || items[0].(map[string]any)["supplier_id"] != float64(supplierID) {
				t.Fatal("snapshot supplier mapping missing", result)
			}
		}
	}

	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY[]::text[] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`, adminID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/resources", nil, resourceViewer, ""), 403)
}
