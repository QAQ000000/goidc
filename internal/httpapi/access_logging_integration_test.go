package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

// Access diagnostics are intentionally eventual. Assertions wait for the
// particular event rather than relying on the writer's scheduling or a sleep.
func waitAPIAccessLog(t *testing.T, db *store.Store, predicate string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var found bool
		err := db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM admin_api_logs WHERE "+predicate+")", args...).Scan(&found)
		if err != nil {
			t.Fatalf("wait for API access event: %v", err)
		}
		if found {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("API access event did not become visible within 3 seconds")
		case <-tick.C:
		}
	}
}

func TestAPIAccessLoggingBlockedWriterDoesNotDelayRequests(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "async-log-customer", "customer")
	_, admin := user(t, db, h, "async-log-root", "super_admin")
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), "LOCK TABLE admin_api_logs IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	firstStarted := time.Now()
	first := request(h, "GET", "/api/v1/me", nil, customer, "")
	expect(t, first, http.StatusOK)
	if elapsed := time.Since(firstStarted); elapsed >= 750*time.Millisecond {
		t.Fatalf("customer response waited for access-log table: %v", elapsed)
	}
	// Prove the asynchronous INSERT really encountered the lock, rather than
	// only observing a response before the first writer tick.
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 800*time.Millisecond)
	defer cancelWait()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := db.Pool.QueryRow(waitCtx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='admin_api_logs'::regclass AND mode='RowExclusiveLock' AND NOT granted)").Scan(&blocked); err != nil {
			t.Fatalf("observe blocked access writer: %v", err)
		}
		if blocked {
			break
		}
		select {
		case <-waitCtx.Done():
			t.Fatal("access writer never attempted the locked table")
		case <-tick.C:
		}
	}
	secondStarted := time.Now()
	second := request(h, "GET", "/api/v1/admin/site-settings", nil, admin, "")
	expect(t, second, http.StatusOK)
	if elapsed := time.Since(secondStarted); elapsed >= 750*time.Millisecond {
		t.Fatalf("admin response waited behind blocked writer: %v", elapsed)
	}
	probeCtx, cancelProbe := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancelProbe()
	var available int
	if err := db.Pool.QueryRow(probeCtx, "SELECT 1").Scan(&available); err != nil || available != 1 {
		t.Fatalf("blocked access writer occupied business pool: value=%d err=%v", available, err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitAPIAccessLog(t, db, "correlation_id=$1 AND path='/api/v1/admin/site-settings' AND status_code=200", second.Header().Get("X-Request-ID"))
	waitAPIAccessLog(t, db, "correlation_id=$1 AND path='/api/v1/me' AND status_code=200", first.Header().Get("X-Request-ID"))
}

func TestAPIAccessLoggingRuntimeDiagnosticsStaySuperAdminOnly(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "async-metrics-root", "super_admin")
	_, customer := user(t, db, h, "async-metrics-customer", "customer")
	staffID, staff := user(t, db, h, "async-metrics-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['audit.view','jobs.view','settings.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cookie string
		status int
	}{{"", 401}, {customer, 401}, {staff, 403}} {
		response := request(h, "GET", "/api/v1/admin/runtime", nil, tc.cookie, "")
		expect(t, response, tc.status)
		if strings.Contains(response.Body.String(), "access_logging") {
			t.Fatal("runtime access diagnostics leaked to unauthorized caller")
		}
	}
	response := request(h, "GET", "/api/v1/admin/runtime", nil, root, "")
	expect(t, response, http.StatusOK)
	diagnostic, ok := data(t, response)["access_logging"].(map[string]any)
	if !ok || diagnostic["status"] == "disabled" || diagnostic["capacity"].(float64) != 1024 || diagnostic["retention_days"].(float64) != 30 {
		t.Fatalf("missing active bounded access diagnostics: %s", response.Body.String())
	}
	for _, metric := range []string{"queue_depth", "in_flight", "accepted", "written", "dropped", "write_failed", "cleanup_failed", "shutdown_discarded", "pruned"} {
		if _, ok := diagnostic[metric]; !ok {
			t.Fatalf("missing access metric %s", metric)
		}
	}
	// Persisted customer events are still outside the administrator log-query
	// projection, and customer sessions cannot invoke that query endpoint.
	customerRead := request(h, "GET", "/api/v1/me", nil, customer, "")
	expect(t, customerRead, http.StatusOK)
	origin := customerRead.Header().Get("X-Request-ID")
	waitAPIAccessLog(t, db, "correlation_id=$1", origin)
	filtered := request(h, "GET", "/api/v1/admin/logs/api?correlation_id="+origin, nil, root, "")
	expect(t, filtered, http.StatusOK)
	if result := data(t, filtered); result["total"].(float64) != 0 || len(result["items"].([]any)) != 0 {
		t.Fatal("customer access records leaked into administrator log query")
	}
	expect(t, request(h, "GET", "/api/v1/admin/logs/api?correlation_id="+origin, nil, customer, ""), http.StatusUnauthorized)
}
