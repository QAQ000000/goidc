package httpapi

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
)

// This opt-in fixture uses testServer's disposable PG18 schema and only loopback upstreams.
// The manifest contains local test credentials; it must not be published as acceptance evidence.
func TestSupplierCustomerBrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_SUPPLIER_CUSTOMER_READY_FILE"), os.Getenv("ZJMFGO_SUPPLIER_CUSTOMER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in supplier customer browser fixture")
	}
	for _, p := range []string{ready, finish} {
		if !filepath.IsAbs(p) || !strings.HasPrefix(filepath.Clean(p), "/tmp/") {
			t.Fatal("fixture files must be absolute paths in /tmp")
		}
	}
	f := supplierSetup(t)
	f.publish(t, f.pid, "customer-operations-vm")
	sid, _ := f.purchase(t, f.pid, "customer-browser-active")
	job := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	vnc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html lang=zh><title>本地测试控制台</title><h1>本地测试控制台</h1><p>隔离供应商 VNC 入口已打开。</p></html>"))
	}))
	defer vnc.Close()
	f.consoleURL.Store(vnc.URL)
	address := os.Getenv("ZJMFGO_SUPPLIER_CUSTOMER_API_ADDR")
	if address == "" {
		address = "127.0.0.1:18101"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("API must bind explicit IPv4 loopback")
	}
	origin := os.Getenv("ZJMFGO_SUPPLIER_CUSTOMER_WEB_ORIGIN")
	if origin == "" {
		origin = "http://127.0.0.1:3101"
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(New(f.db, config.Config{PublicOrigin: origin, ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}, slog.Default()).Handler())
	server.Listener = listener
	server.Start()
	defer server.Close()
	var schema string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	unknownFile := ready + ".unknown"
	manifest, err := json.Marshal(map[string]any{"api_origin": server.URL, "web_origin": origin, "schema": schema, "service_id": sid, "active_service_id": sid, "supplier_id": f.supplierID, "owner_id": f.uid, "unknown_mode_file": unknownFile, "finish_file": finish, "accounts": map[string]any{"owner": map[string]string{"username": "supplier-customer", "password": "integration-password", "cookie": f.customer}, "other": map[string]string{"username": "supplier-other", "password": "integration-password", "cookie": f.other}, "admin": map[string]string{"username": "supplier-root", "password": "integration-password", "cookie": f.root}}})
	if err != nil {
		t.Fatal(err)
	}
	// O_EXCL avoids following an existing symlink or preserving weaker permissions.
	file, err := os.OpenFile(ready, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.Write(manifest)
	closeErr := file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	defer os.Remove(unknownFile)
	timer := time.NewTimer(20 * time.Minute)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("supplier customer browser fixture timed out")
		case <-tick.C:
			if _, err = os.Stat(finish); err == nil {
				return
			}
			_, err = os.Stat(unknownFile)
			f.actionUnknown.Store(err == nil)
			claimed, claimErr := f.db.ClaimProvisionJob(t.Context())
			if claimErr == nil {
				if err = f.db.ProcessProvisionJob(t.Context(), *claimed); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
