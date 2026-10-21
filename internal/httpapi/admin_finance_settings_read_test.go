package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func TestAdminFinanceSettingsReadPreservesContractAndPermissions(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "finance-read-root", "super_admin")
	_, customer := user(t, db, h, "finance-read-customer", "customer")
	viewerID, viewer := user(t, db, h, "finance-read-viewer", "admin")
	writerID, writer := user(t, db, h, "finance-read-writer", "admin")
	contentRole(t, db, viewerID, "settings.view")
	contentRole(t, db, writerID, "settings.manage")
	for _, tc := range []struct {
		cookie string
		status int
	}{{"", http.StatusUnauthorized}, {customer, http.StatusUnauthorized}, {writer, http.StatusForbidden}} {
		response := request(h, "GET", "/api/v1/admin/finance-settings", nil, tc.cookie, "")
		expect(t, response, tc.status)
		if strings.Contains(response.Body.String(), "topup_enabled") {
			t.Fatal("finance settings disclosed without settings.view")
		}
	}
	updatedAt := time.Date(2026, time.October, 21, 8, 9, 10, 123456000, time.UTC)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE finance_settings SET topup_enabled=false,min_topup_cents=123,max_topup_cents=456789,max_balance_cents=900000,require_active_order=true,version=17,updated_at=$1 WHERE id=1", updatedAt); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []string{viewer, root} {
		response := request(h, "GET", "/api/v1/admin/finance-settings", nil, cookie, "")
		expect(t, response, http.StatusOK)
		fields := data(t, response)
		if len(fields) != 7 {
			t.Fatalf("finance GET contract changed fields: %#v", fields)
		}
		if fields["topup_enabled"] != false || fields["min_topup_cents"] != float64(123) || fields["max_topup_cents"] != float64(456789) || fields["max_balance_cents"] != float64(900000) || fields["require_active_order"] != true || fields["version"] != float64(17) {
			t.Fatalf("finance GET changed existing values: %#v", fields)
		}
		var body struct {
			Data store.AdminFinanceSettingsSnapshot `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || !body.Data.UpdatedAt.Equal(updatedAt) {
			t.Fatalf("finance GET changed timestamp representation: updated=%v err=%v", body.Data.UpdatedAt, err)
		}
	}
	var version, audits int64
	var savedAt time.Time
	if err := db.Pool.QueryRow(t.Context(), "SELECT version,updated_at FROM finance_settings WHERE id=1").Scan(&version, &savedAt); err != nil || version != 17 || !savedAt.Equal(updatedAt) {
		t.Fatalf("finance read changed singleton: version=%d updated=%v err=%v", version, savedAt, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='finance.settings_updated'").Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("finance read wrote business audit: count=%d err=%v", audits, err)
	}
}

func TestAdminFinanceSettingsReadMissingSingletonKeepsNotFound(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "finance-missing-root", "super_admin")
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM finance_settings WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	response := request(h, "GET", "/api/v1/admin/finance-settings", nil, root, "")
	expect(t, response, http.StatusNotFound)
	if code := errorData(t, response)["code"]; code != "not_found" {
		t.Fatalf("missing singleton changed error code: %v", code)
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM finance_settings").Scan(&count); err != nil || count != 0 {
		t.Fatalf("GET silently recreated missing singleton: count=%d err=%v", count, err)
	}
}
