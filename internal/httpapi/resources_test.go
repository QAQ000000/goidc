package httpapi

import "testing"

func TestAdminResourcesPageContractAndPermission(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "resources-contract-root", "super_admin")
	_, customer := user(t, db, h, "resources-contract-customer", "customer")
	adminID, resourceViewer := user(t, db, h, "resources-contract-viewer", "admin")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['resources.view']::text[] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`, adminID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/resources", nil, "", ""), 401)
	expect(t, request(h, "GET", "/api/v1/admin/resources", nil, customer, ""), 403)
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
	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY[]::text[] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`, adminID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/resources", nil, resourceViewer, ""), 403)
}
