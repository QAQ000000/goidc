package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

func TestAdminCreateOptionsExactCustomerScope(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "create-context-root", "super_admin")
	staffID, staff := user(t, db, h, "create-context-staff", "admin")
	ownerID, customer := user(t, db, h, "create-context-owner", "customer")
	otherID, _ := user(t, db, h, "create-context-other", "customer")
	// These actions must work without granting access to the full customer file.
	contentRole(t, db, staffID, "orders.manage", "invoices.view", "invoices.manage", "tickets.manage")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE administrators SET customer_scope='owned',customer_ownerless=false,sales_enabled=true WHERE id=$1`, staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET sales_owner_id=$2 WHERE id=$1`, ownerID, staffID); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		"/api/v1/admin/orders/create-options?kind=customers",
		"/api/v1/admin/invoice-customers?kind=customers",
		"/api/v1/admin/tickets/create-options?kind=customers",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			selected := path + fmt.Sprintf("&customer_id=%d", ownerID)
			w := request(h, "GET", selected, nil, staff, "")
			expect(t, w, 200)
			page := data(t, w)
			items := page["items"].([]any)
			if page["total"] != float64(1) || len(items) != 1 || items[0].(map[string]any)["id"] != float64(ownerID) {
				t.Fatal("selected customer was ignored", page)
			}
			for _, forbidden := range []string{"balance_cents", "admin_note", "password_hash", "phone_number", "sales_owner_id"} {
				if strings.Contains(w.Body.String(), forbidden) {
					t.Fatal("creation options leaked customer profile field", forbidden)
				}
			}
			w = request(h, "GET", selected+"&offset=1&limit=1", nil, staff, "")
			expect(t, w, 200)
			if page = data(t, w); page["total"] != float64(1) || len(page["items"].([]any)) != 0 {
				t.Fatal("incorrect selected options pagination", page)
			}
			for _, suffix := range []string{"&q=no-such-customer", "&q=" + fmt.Sprint(otherID) + "-unmatched"} {
				w = request(h, "GET", selected+suffix, nil, staff, "")
				expect(t, w, 200)
				if data(t, w)["total"] != float64(0) {
					t.Fatal("search broadened selected customer")
				}
			}
			for _, filter := range []string{"", "0", "-1", "%2B1", "1.0", "1e2", "%201", "abc", "9223372036854775808", fmt.Sprint(ownerID) + "&customer_id=" + fmt.Sprint(otherID)} {
				w = request(h, "GET", path+"&customer_id="+filter, nil, staff, "")
				expect(t, w, 400)
				if !strings.Contains(w.Body.String(), "invalid_customer") {
					t.Fatal("missing strict customer error", w.Body.String())
				}
			}
			for _, id := range []int64{otherID, staffID, 9223372036854775807} {
				expect(t, request(h, "GET", path+fmt.Sprintf("&customer_id=%d", id), nil, staff, ""), 404)
			}
			expect(t, request(h, "GET", selected, nil, "", ""), 401)
			expect(t, request(h, "GET", selected, nil, customer, ""), 401)
			expect(t, request(h, "GET", "/api/v1/admin/customers/"+fmt.Sprint(ownerID), nil, staff, ""), 403)
			w = request(h, "GET", path, nil, root, "")
			expect(t, w, 200)
			if data(t, w)["total"] != float64(2) {
				t.Fatal("omitted customer filter changed global options")
			}
		})
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET status='disabled' WHERE id=$1`, ownerID); err != nil {
		t.Fatal(err)
	}
	for index, path := range paths {
		w := request(h, "GET", path+fmt.Sprintf("&customer_id=%d", ownerID), nil, staff, "")
		expect(t, w, 200)
		want := float64(0)
		if index == 1 { // Invoice options intentionally retain inactive customers.
			want = 1
		}
		if data(t, w)["total"] != want {
			t.Fatal("changed action-specific inactive customer rule", path)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET sales_owner_id=NULL WHERE id=$1`, ownerID); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		expect(t, request(h, "GET", path+fmt.Sprintf("&customer_id=%d", ownerID), nil, staff, ""), 404)
	}
	contentRole(t, db, staffID, "audit.view")
	for _, path := range paths {
		expect(t, request(h, "GET", path, nil, staff, ""), 403)
	}
}
