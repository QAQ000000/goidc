package httpapi

import (
	"strings"
	"testing"
)

func TestAdminSystemLogsSearchPermissionsAndRedaction(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "system-logs-root", "super_admin")
	staffID, staff := user(t, db, h, "system-logs-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['notifications.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`, staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/logs", nil, staff, ""), 403)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['audit.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`, staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES(NULL,'system.search.fixture','search-object','123456789','{"password":"never-return-this-secret","token":"private-token"}')`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"system.search.fixture", "search-object", "123456789"} {
		w := request(h, "GET", "/api/v1/admin/logs?kind=system&q="+q+"&limit=25&offset=0", nil, staff, "")
		expect(t, w, 200)
		result := data(t, w)
		if result["total"].(float64) != 1 || len(result["items"].([]any)) != 1 {
			t.Fatalf("search %q result=%#v", q, result)
		}
		row := result["items"].([]any)[0].(map[string]any)
		if _, exists := row["payload"]; exists {
			t.Fatal("audit payload exposed")
		}
		if strings.Contains(w.Body.String(), "never-return-this-secret") || strings.Contains(w.Body.String(), "private-token") {
			t.Fatal("secret exposed")
		}
	}
	empty := request(h, "GET", "/api/v1/admin/logs?q=no-match-fixture", nil, root, "")
	expect(t, empty, 200)
	if data(t, empty)["total"].(float64) != 0 || len(data(t, empty)["items"].([]any)) != 0 {
		t.Fatal("unmatched query returned audit events")
	}
	paged := request(h, "GET", "/api/v1/admin/logs?q=system.search.fixture&offset=1", nil, staff, "")
	expect(t, paged, 200)
	if data(t, paged)["total"].(float64) != 1 || len(data(t, paged)["items"].([]any)) != 0 {
		t.Fatal("pagination lost total or ignored offset")
	}
	for _, kind := range []string{"sms", "email", "api", "login", "cron", "message", "unknown"} {
		expect(t, request(h, "GET", "/api/v1/admin/logs?kind="+kind, nil, root, ""), 400)
	}
}
