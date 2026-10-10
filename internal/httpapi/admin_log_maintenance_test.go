package httpapi

import (
	"testing"
	"time"
)

func TestAdminLogCleanupIsConfirmedIdempotentAndAudited(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "log-cleanup-root", "super_admin")
	loginPage := request(h, "GET", "/api/v1/admin/logs/admin-login?limit=10&offset=0", nil, root, "")
	expect(t, loginPage, 200)
	if got := data(t, loginPage)["total"].(float64); got < 1 {
		t.Fatalf("admin login log total=%v", got)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload,created_at)
VALUES(NULL,'cleanup.fixture','test','old','{}',now()-interval '3 days')`); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	body := map[string]any{"log_type": "audit", "before": before, "acknowledged": true}
	w := request(h, "POST", "/api/v1/admin/logs/cleanup", body, root, "log-cleanup-key")
	expect(t, w, 200)
	result := data(t, w)
	if result["deleted"].(float64) < 1 || result["replayed"].(bool) {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	replay := request(h, "POST", "/api/v1/admin/logs/cleanup", body, root, "log-cleanup-key")
	expect(t, replay, 200)
	if !data(t, replay)["replayed"].(bool) {
		t.Fatal("cleanup replay was not marked")
	}
	conflict := request(h, "POST", "/api/v1/admin/logs/cleanup", map[string]any{"log_type": "sms", "before": before, "acknowledged": true}, root, "log-cleanup-key")
	expect(t, conflict, 409)
	if no := request(h, "POST", "/api/v1/admin/logs/cleanup", map[string]any{"log_type": "audit", "before": before}, root, "log-cleanup-confirm"); no.Code != 400 {
		t.Fatalf("missing confirmation status=%d body=%s", no.Code, no.Body.String())
	}
	if no := request(h, "POST", "/api/v1/admin/logs/cleanup", map[string]any{"log_type": "audit", "before": time.Now().UTC().Format(time.RFC3339), "acknowledged": true}, root, "log-cleanup-recent"); no.Code != 400 {
		t.Fatalf("recent cleanup status=%d body=%s", no.Code, no.Body.String())
	}
	var audits int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='log.cleanup.completed'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("cleanup audit count=%d", audits)
	}
}
