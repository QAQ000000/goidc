package httpapi

import (
	"fmt"
	"testing"
)

func TestAdminCustomerAndCertificationPages(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "pagination-root", "super_admin")
	owner, staff := user(t, db, h, "pagination-owner", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true,customer_scope='owned' WHERE id=$1", owner); err != nil {
		t.Fatal(err)
	}
	var group int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO customer_groups(name) VALUES('pagination-group') RETURNING id").Scan(&group); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		id, _ := user(t, db, h, fmt.Sprintf("pagination-customer-%d", i), "customer")
		if i < 2 {
			if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", owner, id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(t.Context(), "INSERT INTO customer_group_members(customer_id,group_id) VALUES($1,$2)", id, group); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Pool.Exec(t.Context(), "INSERT INTO certification_profiles(user_id,status) VALUES($1,'pending')", id); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		path, cookie string
		total, count int
	}{
		{"/admin/customers/page?q=pagination-customer&limit=1&offset=1", root, 3, 1},
		{fmt.Sprintf("/admin/customers/page?group_id=%d&sales_owner_id=%d&status=active&limit=1", group, owner), root, 2, 1},
		{"/admin/customers/page?group_id=none&sales_owner_id=none&status=disabled", root, 1, 1},
		{"/admin/customers/page?q=pagination-customer&limit=1&offset=100", root, 3, 0},
		{"/admin/customers/page?q=pagination-customer", staff, 2, 2},
		{"/admin/certifications/page?q=pagination-customer&status=pending&limit=1&offset=1", root, 3, 1},
		{"/admin/certifications/page?q=pagination-customer-2", root, 1, 1},
		{"/admin/certifications/page?status=approved", root, 0, 0},
		{"/admin/certifications/page?q=pagination-customer&status=pending&limit=1&offset=1", staff, 2, 1},
		{"/admin/certifications/page?q=pagination-customer-2", staff, 0, 0},
		{"/admin/certifications/page?q=pagination-customer&limit=1&offset=100", staff, 2, 0},
	} {
		w := request(h, "GET", "/api/v1"+tc.path, nil, tc.cookie, "")
		expect(t, w, 200)
		result := data(t, w)
		if int(result["total"].(float64)) != tc.total || len(result["items"].([]any)) != tc.count {
			t.Fatalf("%s: %v", tc.path, result)
		}
	}
	for _, path := range []string{"/admin/customers/page?group_id=bad", "/admin/customers/page?sales_owner_id=-1", "/admin/customers/page?status=bad", "/admin/certifications/page?status=bad"} {
		expect(t, request(h, "GET", "/api/v1"+path, nil, root, ""), 400)
	}
	expect(t, request(h, "GET", "/api/v1/admin/customers/page", nil, "", ""), 401)
	if len(listData(t, request(h, "GET", "/api/v1/admin/customers?q=pagination-customer&limit=1", nil, root, ""))) != 1 {
		t.Fatal("legacy customers array changed")
	}
	if len(listData(t, request(h, "GET", "/api/v1/admin/certifications?limit=1", nil, root, ""))) != 1 {
		t.Fatal("legacy certifications array changed")
	}
}
