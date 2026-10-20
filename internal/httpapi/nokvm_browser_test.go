package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
)

func TestNOKVMBrowserFixture(t *testing.T) {
	ready, finish := browserFixturePaths(t)
	var creates atomic.Int32
	var mu sync.Mutex
	calls := []string{}
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Method == "POST" {
			id := 41 + creates.Add(1)
			if id == 43 {
				fmt.Fprint(w, `{"message":"fixture ambiguous create response"}`)
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"id":%d,"name":"browser-vm-%d","public_ip":[{"ip":"192.0.2.42"}]}}`, id, id)
		} else if strings.Contains(r.URL.Path, "run_status") {
			fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
		} else if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/virtual/") {
			fmt.Fprint(w, `{"code":0,"data":{"name":"browser-synced","ip_list":[{"ip":"192.0.2.43"}],"sys_pwd":"supplier-secret"}}`)
		} else {
			fmt.Fprint(w, `{"code":0}`)
		}
	}, false)
	readerID, _ := user(t, f.db, f.h, "nokvm-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view','services.view','invoices.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.uid), map[string]any{"amount_cents": 100000}, f.root, "nokvm-browser-topup"), 201)
	origin := os.Getenv("ZJMFGO_BROWSER_ORIGIN")
	if origin == "" {
		origin = "http://localhost:3099"
	}
	server := httptest.NewServer(New(f.db, config.Config{PublicOrigin: origin, PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", NOKVMProfiles: f.db.Provisioners, ProvisionEncryptionKey: f.db.ProvisionEncryptionKey}, slog.Default()).Handler())
	defer server.Close()
	stop := startBrowserProvisionWorker(t, f.db, nil)
	defer stop()
	var schema string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	writeBrowserJSON(t, ready, map[string]any{"api_origin": server.URL, "schema": schema, "username": "nokvm-root", "product_id": f.pid, "service_id": f.sid, "invoice_id": f.invoice})
	waitBrowserFinish(t, ready, finish, 20*time.Minute)
	mu.Lock()
	evidence, _ := json.Marshal(map[string]any{"creates": creates.Load(), "calls": calls})
	mu.Unlock()
	t.Log(string(evidence))
	if creates.Load() != 2 {
		t.Errorf("expected exactly two create requests, got %d", creates.Load())
	}
}
