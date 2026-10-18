package httpapi

import (
	"fmt"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/supplier"
)

// Opt-in, isolated PG18 plus an in-memory supplier. No worker, remote supplier,
// payment provider or deployment configuration is loaded by this fixture.
func TestSupplierLifecycleBrowserFixture(t *testing.T) {
	ready, finish := browserFixturePaths(t)
	origin := os.Getenv("ZJMFGO_BROWSER_ORIGIN")
	if origin == "" {
		t.Fatal("isolated browser origin is required")
	}
	f := newSupplierLifecycleFixture(t, false)
	services := map[string]int64{"new_request": f.sid}
	// The remote is already suspended; the site still retains its last
	// confirmed Active state until the browser registers and verifies the task.
	f.remote(func(service *supplier.Service) { service.Status = "Suspended" })
	for _, action := range []string{"resume", "terminate", "not_applied"} {
		one := *f
		one.sid, _ = f.purchase(t, f.pid, "supplier-lifecycle-browser-"+action)
		if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT upstream_id FROM services WHERE id=$1", one.sid).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var err error
		one.upstream, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		jobAction, remote := action, "Terminated"
		if action == "resume" {
			one.prepare(t, action)
			remote = "Active"
		}
		if action == "not_applied" {
			jobAction, remote = "suspend", "Active"
		}
		one.queue(t, jobAction, "supplier-lifecycle-browser-manual-"+action)
		one.remote(func(service *supplier.Service) { service.Status = remote })
		services[action] = one.sid
	}
	server := httptest.NewUnstartedServer(New(f.db, config.Config{PublicOrigin: origin,
		ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}, slog.Default()).Handler())
	if address := os.Getenv("ZJMFGO_BROWSER_LISTEN_ADDR"); address != "" {
		host, _, err := net.SplitHostPort(address)
		if err != nil || host != "127.0.0.1" {
			t.Fatal("browser fixture listen address must use 127.0.0.1")
		}
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		server.Listener.Close()
		server.Listener = listener
	}
	server.Start()
	defer server.Close()
	var schema string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT current_schema()::text").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	// Session cookies are local fixture credentials and remain only in the
	// owner-readable ready file. Browser runners must not publish this file.
	writeBrowserJSON(t, ready, map[string]any{"api_origin": server.URL, "web_origin": origin, "schema": schema,
		"services": services, "session_cookie": "zjmfgo_session", "accounts": map[string]any{
			"admin": map[string]string{"cookie": f.root}, "reader": map[string]string{"cookie": f.reader},
			"customer": map[string]string{"cookie": f.customer},
		}})
	waitBrowserFinish(t, ready, finish, 15*time.Minute)
	if f.calls() != 0 {
		t.Fatalf("manual browser workflow sent %d supplier writes", f.calls())
	}
	t.Log(fmt.Sprintf("isolated supplier lifecycle browser fixture closed: %d resources", len(services)))
}
