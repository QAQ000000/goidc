package httpapi

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
)

func TestSupplierBrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_SUPPLIER_READY_FILE"), os.Getenv("ZJMFGO_SUPPLIER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in supplier browser fixture")
	}
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	f.uncertain.Store(true)
	serviceID, _ := f.purchase(t, f.pid, "supplier-browser-unknown")
	job := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	f.uncertain.Store(false)
	listener, err := net.Listen("tcp", "127.0.0.1:18101")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(New(f.db, config.Config{PublicOrigin: "http://127.0.0.1:3101", ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}, slog.Default()).Handler())
	server.Listener = listener
	server.Start()
	defer server.Close()
	var schema string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"api_origin": server.URL, "supplier_url": f.remote.URL, "schema": schema, "blocked_service_id": serviceID, "supplier_id": f.supplierID, "customer_id": f.uid})
	if err = os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	timer := time.NewTimer(30 * time.Minute)
	defer timer.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("supplier browser fixture timed out")
		case <-tick.C:
			if _, err := os.Stat(finish); err == nil {
				goto verify
			}
			claimed, err := f.db.ClaimProvisionJob(t.Context())
			if err == nil {
				if err = f.db.ProcessProvisionJob(t.Context(), *claimed); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
verify:
	var accounts, products, orders, active, pending int
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM supplier_accounts),(SELECT count(*) FROM supplier_product_bindings),(SELECT count(*) FROM orders),(SELECT count(*) FROM services WHERE status='active'),(SELECT count(*) FROM supplier_accounts WHERE running_job_id IS NOT NULL)`).Scan(&accounts, &products, &orders, &active, &pending); err != nil {
		t.Fatal(err)
	}
	if accounts != 2 || products != 2 || orders != 2 || active != 2 || pending != 0 || f.creates.Load() != 2 || f.pays.Load() != 1 {
		t.Fatalf("incomplete supplier browser acceptance accounts=%d bindings=%d orders=%d active=%d blocked=%d creates=%d pays=%d", accounts, products, orders, active, pending, f.creates.Load(), f.pays.Load())
	}
	t.Log("supplier browser acceptance: account creation, product preview/import/publish/checkout, worker delivery, unknown outcome reconciliation")
}
