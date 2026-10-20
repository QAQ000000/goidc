package httpapi

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func requireCreditRetired(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	policyErrorCode(t, w, http.StatusGone, "credit_retired")
	if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), `"data"`) {
		t.Fatal("retirement response exposed data or allowed caching", w.Body.String())
	}
}

func TestRetiredCreditHandlerDoesNotAccessStorage(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).creditRetired(w, httptest.NewRequest("POST", "/api/v1/invoices/1/pay-credit", strings.NewReader("invalid JSON")))
	requireCreditRetired(t, w)
}

func retiredCreditState(t *testing.T, db *store.Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"credit_settings", "customer_credit_accounts", "customer_credit_events", "credit_invoice_payments", "credit_repayments", "users", "invoices", "orders", "order_items", "services", "balance_ledger", "audit_events", "mutation_operations"} {
		var state string
		if err := db.Pool.QueryRow(t.Context(), "SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text FROM "+table+" r").Scan(&state); err != nil {
			t.Fatal(err)
		}
		out[table] = state
	}
	return out
}

func TestRetiredCreditRoutesRequirePortalAndNeverMutate(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "retired-credit-root", "super_admin")
	_, staff := user(t, db, h, "retired-credit-staff", "admin")
	_, customer := user(t, db, h, "retired-credit-customer", "customer")
	readerID, reader := user(t, db, h, "retired-credit-no-grants", "admin")
	contentRole(t, db, readerID, "customers.view")
	permissions := request(h, "GET", "/api/v1/admin/permissions", nil, root, "")
	expect(t, permissions, 200)
	for _, obsolete := range []string{"credit.view", "credit.manage", "contracts.view", "contracts.manage"} {
		if strings.Contains(permissions.Body.String(), `"`+obsolete+`"`) {
			t.Fatalf("retired permission remains assignable: %s", obsolete)
		}
	}
	// An obsolete config write, even directly in the DB, cannot reopen a route.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE credit_settings SET enabled=true"); err != nil {
		t.Fatal(err)
	}
	before := retiredCreditState(t, db)
	routes := []struct {
		method, path string
		admin        bool
	}{
		{"GET", "/api/v1/credit/account", false},
		{"GET", "/api/v1/credit/consumptions", false},
		{"GET", "/api/v1/credit/repayments", false},
		{"GET", "/api/v1/invoices/999999/credit", false},
		{"POST", "/api/v1/invoices/999999/pay-credit", false},
		{"GET", "/api/v1/admin/credit-settings", true},
		{"PUT", "/api/v1/admin/credit-settings", true},
		{"GET", "/api/v1/admin/credit", true},
		{"GET", "/api/v1/admin/credit/consumptions", true},
		{"GET", "/api/v1/admin/credit/repayments", true},
		{"POST", "/api/v1/admin/credit/consumptions/999999/repay", true},
		{"PUT", "/api/v1/admin/credit/999999/freeze", true},
		{"PUT", "/api/v1/admin/credit/999999/limit", true},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			allowed, wrong := []string{customer}, root
			if route.admin {
				allowed, wrong = []string{root, staff}, customer
				expect(t, request(h, route.method, route.path, map[string]any{"enabled": true}, reader, "retired-credit-request"), 403)
			}
			for _, cookie := range allowed {
				for range 2 {
					requireCreditRetired(t, request(h, route.method, route.path, map[string]any{"enabled": true, "limit_cents": 10000, "expected_version": 1}, cookie, "retired-credit-request"))
				}
			}
			for _, cookie := range []string{"", "zjmfgo_session=invalid; zjmfgo_admin_session=invalid", wrong} {
				expect(t, request(h, route.method, route.path, nil, cookie, "retired-credit-request"), 401)
			}
		})
	}
	expect(t, requestFrom(h, "POST", "/api/v1/invoices/999999/pay-credit", map[string]any{}, "192.0.2.1:1234", customer, map[string]string{"X-Requested-With": ""}), 403)
	expect(t, requestFrom(h, "POST", "/api/v1/invoices/999999/pay-credit", map[string]any{}, "192.0.2.1:1234", customer, map[string]string{"Origin": "https://invalid.example"}), 403)
	if after := retiredCreditState(t, db); !reflect.DeepEqual(before, after) {
		for table, state := range before {
			if after[table] != state {
				t.Errorf("retired credit route changed %s", table)
			}
		}
	}
}
