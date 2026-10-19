package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCustomerBusinessRejectsAdministratorCredentials(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "boundary-customer", "customer")
	pid := product(t, db, 20)
	for _, role := range []string{"admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			uid, cookie := user(t, db, h, "boundary-"+strings.ReplaceAll(role, "_", "-"), role)
			keyResponse := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "boundary-key"}, cookie, "")
			expect(t, keyResponse, 201)
			secret := data(t, keyResponse)["secret"].(string)
			// Pre-existing administrator carts must not become an admission bypass.
			var item int64
			if err := db.Pool.QueryRow(t.Context(), `INSERT INTO cart_items(cart_id,product_id,quantity,product_revision,billing_cycle) SELECT c.id,p.id,1,p.revision,'monthly' FROM carts c CROSS JOIN products p WHERE c.user_id=$1 AND p.id=$2 RETURNING cart_items.id`, uid, pid).Scan(&item); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct{ method, path string }{
				{"GET", "/api/v1/cart"}, {"HEAD", "/api/v1/cart"}, {"POST", "/api/v1/cart/items"}, {"PATCH", "/api/v1/cart/items/1"}, {"DELETE", "/api/v1/cart/items/1"},
				{"POST", "/api/v1/cart/quote"}, {"POST", "/api/v1/cart/promo"}, {"DELETE", "/api/v1/cart/promo"},
				{"POST", "/api/v1/orders"}, {"GET", "/api/v1/orders"}, {"GET", "/api/v1/orders/page"}, {"GET", "/api/v1/orders/1"}, {"POST", "/api/v1/orders/1/cancel"},
				{"GET", "/api/v1/invoices"}, {"GET", "/api/v1/invoices/page"}, {"GET", "/api/v1/invoices/1"}, {"POST", "/api/v1/invoices/1/pay"}, {"POST", "/api/v1/invoices/1/payments"}, {"GET", "/api/v1/payment-providers"},
				{"GET", "/api/v1/account"}, {"GET", "/api/v1/services"}, {"GET", "/api/v1/services/page"}, {"GET", "/api/v1/services/1"}, {"POST", "/api/v1/services/1/renew"}, {"POST", "/api/v1/services/1/actions"},
				{"GET", "/api/v1/service-navigation"}, {"GET", "/api/v1/customer-files"}, {"GET", "/api/v1/contacts"}, {"POST", "/api/v1/contacts"},
				{"GET", "/api/v1/account/certification"}, {"POST", "/api/v1/account/certification"}, {"GET", "/api/v1/invoice-profiles"}, {"GET", "/api/v1/invoice-addresses"}, {"POST", "/api/v1/invoice-applications"},
				{"GET", "/api/v1/affiliate/overview"}, {"POST", "/api/v1/affiliate/apply"}, {"GET", "/api/v1/tickets"}, {"POST", "/api/v1/tickets"}, {"GET", "/api/v1/ticket-departments"}, {"POST", "/api/v1/tickets/1/ratings"},
			} {
				for _, bearer := range []bool{false, true} {
					body := map[string]any{"cart_version": 1, "item_ids": []int64{item}, "version": 1, "product_id": pid, "quantity": 1}
					var response *httptest.ResponseRecorder
					if bearer {
						response = requestBearer(h, tc.method, tc.path, body, secret)
					} else {
						response = request(h, tc.method, tc.path, body, cookie, "boundary-order-key")
					}
					if response.Code != 403 || errorData(t, response)["code"] != "customer_required" {
						t.Fatalf("%s %s bearer=%t admitted an administrator: %d %s", tc.method, tc.path, bearer, response.Code, response.Body.String())
					}
				}
			}
			for _, tc := range []struct{ method, path string }{
				{"POST", "/api/v1/invoices/1/pay-credit"}, {"POST", "/api/v1/balance/topups"}, {"GET", "/api/v1/balance/topup-options"},
				{"GET", "/api/v1/contracts"}, {"POST", "/api/v1/contracts"}, {"POST", "/api/v1/account/verification/request"},
			} {
				response := request(h, tc.method, tc.path, map[string]any{}, cookie, "boundary-session-key")
				expect(t, response, 403)
				if errorData(t, response)["code"] != "customer_required" {
					t.Fatalf("session-only customer path admitted staff: %s", response.Body.String())
				}
			}
			// A valid customer cookie must not override an administrator bearer key.
			mixed := requestFrom(h, "POST", "/api/v1/orders", map[string]any{}, "192.0.2.10:1234", customer, map[string]string{"Authorization": "Bearer " + secret})
			expect(t, mixed, 403)
			if errorData(t, mixed)["code"] != "customer_required" {
				t.Fatal(mixed.Body.String())
			}
			var orders, invoices, services, tickets, cartItems int
			if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM orders WHERE user_id=$1),(SELECT count(*) FROM invoices WHERE user_id=$1),(SELECT count(*) FROM services WHERE user_id=$1),(SELECT count(*) FROM tickets WHERE user_id=$1),(SELECT count(*) FROM cart_items WHERE id=$2)`, uid, item).Scan(&orders, &invoices, &services, &tickets, &cartItems); err != nil || orders != 0 || invoices != 0 || services != 0 || tickets != 0 || cartItems != 1 {
				t.Fatalf("denied administrator request changed business facts: %d %d %d %d %d %v", orders, invoices, services, tickets, cartItems, err)
			}
			expect(t, request(h, "GET", "/api/v1/admin/dashboard", nil, cookie, ""), 200)
			expect(t, requestBearer(h, "GET", "/api/v1/admin/dashboard", nil, secret), 200)
		})
	}
}

func TestAccountSecurityAndNotificationsRemainShared(t *testing.T) {
	db, h := testServer(t)
	for _, role := range []string{"customer", "admin", "super_admin"} {
		uid, cookie := user(t, db, h, "account-"+strings.ReplaceAll(role, "_", "-"), role)
		if role == "admin" {
			if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", uid); err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range []string{"/api/v1/me", "/api/v1/account/mfa", "/api/v1/api-keys", "/api/v1/me/notification-preferences", "/api/v1/me/notification-inbox", "/api/v1/me/notification-web-push", "/api/v1/me/ticket-notification-preferences"} {
			expect(t, request(h, "GET", path, nil, cookie, ""), 200)
		}
		password := request(h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "boundary-new-password"}, cookie, "")
		expect(t, password, 200)
		expect(t, request(h, "POST", "/api/v1/auth/logout", nil, cookie, ""), 200)
		expect(t, request(h, "GET", "/api/v1/me", nil, cookie, ""), 401)
	}
}

func TestCustomerCheckoutAndImpersonationUseCustomerIdentity(t *testing.T) {
	db, h := testServer(t)
	adminID, admin := user(t, db, h, "isolated-operator", "super_admin")
	customerID, customer := user(t, db, h, "isolated-buyer", "customer")
	pid := product(t, db, 3)
	version, item := add(t, h, customer, pid, 1)
	expect(t, request(h, "POST", "/api/v1/orders", map[string]any{"cart_version": version, "item_ids": []int64{item}}, admin, "isolated-admin-attempt"), 403)
	started := request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/impersonate", customerID), nil, admin, "")
	expect(t, started, 200)
	var parts []string
	for _, c := range started.Result().Cookies() {
		parts = append(parts, c.Name+"="+c.Value)
	}
	impersonated := strings.Join(parts, "; ")
	expect(t, request(h, "GET", "/api/v1/cart", nil, impersonated, ""), 200)
	expect(t, request(h, "GET", "/api/v1/admin/dashboard", nil, impersonated, ""), 403)
	created := request(h, "POST", "/api/v1/orders", map[string]any{"cart_version": version, "item_ids": []int64{item}}, impersonated, "isolated-customer-order")
	expect(t, created, 201)
	var owner int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT user_id FROM orders WHERE id=$1", int64(data(t, created)["order_id"].(float64))).Scan(&owner); err != nil || owner != customerID || owner == adminID {
		t.Fatalf("explicit customer session created an administrator-owned order: %d %v", owner, err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/dashboard", nil, customer, ""), 403)
	expect(t, request(h, "POST", "/api/v1/auth/impersonation/exit", nil, impersonated, ""), 200)
	expect(t, request(h, "GET", "/api/v1/cart", nil, admin, ""), 403)
	// No authenticated role is needed to read the public catalog.
	for _, cookie := range []string{"", admin, customer} {
		expect(t, request(h, http.MethodGet, "/api/v1/catalog", nil, cookie, ""), 200)
	}
}
