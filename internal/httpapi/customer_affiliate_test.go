package httpapi

import (
	"fmt"
	"testing"
)

func TestAdminCustomerAffiliateActivityEndpoint(t *testing.T) {
	db, h := testServer(t)
	adminID, admin := user(t, db, h, "customer-affiliate-admin", "super_admin")
	customerID, _ := user(t, db, h, "customer-affiliate-customer", "customer")
	response := request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d/affiliate?limit=25&offset=0", customerID), nil, admin, "")
	expect(t, response, 200)
	items := listData(t, response)
	if len(items) != 0 {
		t.Fatalf("empty customer affiliate activity=%v", items)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned' WHERE id=$1", adminID); err != nil {
		t.Fatal(err)
	}
	response = request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d/affiliate", customerID), nil, admin, "")
	// Super administrators retain full customer access regardless of scope.
	expect(t, response, 200)
	staffID, staff := user(t, db, h, "customer-affiliate-staff", "admin")
	contentRole(t, db, staffID, "customers.view")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	response = request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d/affiliate", customerID), nil, staff, "")
	expect(t, response, 404)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, customerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d/affiliate", customerID), nil, staff, ""), 200)
}
