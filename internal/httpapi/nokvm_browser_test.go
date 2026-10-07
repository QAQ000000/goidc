package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/jackc/pgx/v5"
)

func TestNOKVMBrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_BROWSER_READY_FILE"), os.Getenv("ZJMFGO_BROWSER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in isolated browser fixture")
	}
	for _, path := range []string{ready, finish} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("use fresh fixture paths")
		}
	}
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
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view','services.view','invoices.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.uid), map[string]any{"amount_cents": 100000}, f.root, "nokvm-browser-topup"), 201)
	origin := os.Getenv("ZJMFGO_BROWSER_ORIGIN")
	if origin == "" {
		origin = "http://localhost:3099"
	}
	server := httptest.NewServer(New(f.db, config.Config{PublicOrigin: origin, PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", NOKVMProfiles: f.db.Provisioners, ProvisionEncryptionKey: f.db.ProvisionEncryptionKey}, slog.Default()).Handler())
	defer server.Close()
	ctx, stop := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				job, err := f.db.ClaimProvisionJob(ctx)
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				if err != nil {
					t.Errorf("browser worker claim: %v", err)
					return
				}
				if err = f.db.ProcessProvisionJob(ctx, *job); err != nil {
					t.Errorf("browser worker: %v", err)
					return
				}
			}
		}
	}()
	defer func() { stop(); <-workerDone }()
	var schema string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"api_origin": server.URL, "schema": schema, "username": "nokvm-root", "product_id": f.pid, "service_id": f.sid, "invoice_id": f.invoice})
	if err := os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	t.Log("NOKVM browser fixture ready")
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser fixture timeout")
		case <-tick.C:
			if _, err := os.Stat(finish); err == nil {
				mu.Lock()
				evidence, _ := json.Marshal(map[string]any{"creates": creates.Load(), "calls": calls})
				mu.Unlock()
				t.Log(string(evidence))
				if creates.Load() != 2 {
					t.Errorf("expected exactly two create requests, got %d", creates.Load())
				}
				return
			}
		}
	}
}
