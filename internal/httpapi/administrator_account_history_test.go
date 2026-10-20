package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAdministratorAccountHistoryHTTPSuperSessionBoundary(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "account-history-root", "super_admin")
	staffID, staff := user(t, db, h, "account-history-staff", "admin")
	customerID, customer := user(t, db, h, "account-history-customer", "customer")
	path := fmt.Sprintf("/api/v1/admin/account-history/%d", staffID)
	w := request(h, http.MethodGet, path+"?limit=1&offset=0", nil, root, "")
	expect(t, w, http.StatusOK)
	if w.Header().Get("Cache-Control") != "no-store" || data(t, w)["read_only"] != true {
		t.Fatal("history response must be private and read-only")
	}
	for _, name := range []string{"orders", "invoices", "services", "ledger"} {
		page := data(t, w)[name].(map[string]any)
		if page["total"] != float64(0) || len(page["items"].([]any)) != 0 {
			t.Fatalf("new administrator unexpectedly has %s history", name)
		}
	}
	for _, forbidden := range []string{"password", "mfa_", "token", "checkout_url", "console", "capabilities"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("history response exposed %q", forbidden)
		}
	}
	// Even a normal operator with every business grant, including audit.view,
	// cannot use the separate staff history capability.
	expect(t, request(h, http.MethodGet, path, nil, staff, ""), http.StatusForbidden)
	expect(t, request(h, http.MethodGet, path, nil, customer, ""), http.StatusUnauthorized)
	expect(t, request(h, http.MethodGet, fmt.Sprintf("/api/v1/admin/account-history/%d", customerID), nil, root, ""), http.StatusNotFound)
	expect(t, request(h, http.MethodGet, "/api/v1/admin/account-history/0", nil, root, ""), http.StatusBadRequest)
	expect(t, request(h, http.MethodPost, path, map[string]any{}, root, ""), http.StatusMethodNotAllowed)
	key := request(h, http.MethodPost, "/api/v1/api-keys", map[string]any{"name": "history-key-boundary"}, root, "")
	expect(t, key, http.StatusCreated)
	expect(t, requestBearer(h, http.MethodGet, path, nil, data(t, key)["secret"].(string)), http.StatusForbidden)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE administrators SET status='disabled' WHERE id=$1`, rootID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, http.MethodGet, path, nil, root, ""), http.StatusUnauthorized)
}
