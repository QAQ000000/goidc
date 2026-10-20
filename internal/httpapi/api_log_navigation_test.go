package httpapi

import (
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Independent fixture for the API-log permission boundary. The shared browser
// fixture and all business databases remain untouched.
func TestAPILogNavigationBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "api-log-super", func(db *store.Store, h http.Handler) {
		apiLogReaders(t, db, h)
	})
}

func apiLogReaders(t *testing.T, db *store.Store, h http.Handler) (string, string) {
	t.Helper()
	cookies := make([]string, 2)
	for i, reader := range []struct{ name, permission string }{
		{"api-log-audit", "audit.view"},
		{"api-log-notifications", "notifications.view"},
	} {
		id, cookie := user(t, db, h, reader.name, "admin")
		if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY[$2]::text[] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`, id, reader.permission); err != nil {
			t.Fatal(err)
		}
		cookies[i] = cookie
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO admin_api_logs(actor_id,method,path,status_code,duration_ms,remote_ip) SELECT id,'GET','/api/v1/navigation-acceptance',200,3,'127.0.0.1' FROM administrators WHERE username='api-log-audit'`); err != nil {
		t.Fatal(err)
	}
	return cookies[0], cookies[1]
}

func TestAPILogNavigationHTTPPermissionBoundary(t *testing.T) {
	db, h := testServer(t)
	audit, notifications := apiLogReaders(t, db, h)
	_, super := user(t, db, h, "api-log-super", "super_admin")
	for _, tc := range []struct {
		name, cookie      string
		api, notification int
	}{
		{"audit-only", audit, 200, 403},
		{"notification-only", notifications, 403, 200},
		{"super", super, 200, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(h, "GET", "/api/v1/admin/logs/api?q=navigation-acceptance", nil, tc.cookie, "")
			expect(t, w, tc.api)
			if tc.api == 200 {
				result := data(t, w)
				if result["total"].(float64) != 1 || len(result["items"].([]any)) != 1 {
					t.Fatal("API reader did not receive the isolated log row", result)
				}
			}
			for _, kind := range []string{"sms", "inbox", "email"} {
				expect(t, request(h, "GET", "/api/v1/admin/logs/"+kind, nil, tc.cookie, ""), tc.notification)
			}
		})
	}
}
