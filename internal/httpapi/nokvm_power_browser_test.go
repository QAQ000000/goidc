package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/jackc/pgx/v5"
)

func TestNOKVMPowerBrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_BROWSER_READY_FILE"), os.Getenv("ZJMFGO_BROWSER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in isolated browser fixture")
	}
	for _, path := range []string{ready, finish} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("use fresh fixture paths")
		}
	}
	f, _, calls := powerFixture(t, `{"code":0}`)
	f.action(t, "sync")
	before := f.powerInvariant(t)
	readerID, _ := user(t, f.db, f.h, "power-browser-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(f.db, config.Config{PublicOrigin: "http://localhost:3099", PaymentGatewaySecret: "integration-payment-secret", NOKVMProfiles: f.db.Provisioners, ProvisionEncryptionKey: f.db.ProvisionEncryptionKey}, slog.Default()).Handler())
	defer server.Close()
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				job, err := f.db.ClaimProvisionJob(ctx)
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				if err = f.db.ProcessProvisionJob(ctx, *job); err != nil && ctx.Err() == nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	defer func() { stop(); <-done }()
	manifest, _ := json.Marshal(map[string]any{"api_origin": server.URL, "service_id": f.sid, "customer": "nokvm-customer", "reader": "power-browser-reader"})
	if err := os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	t.Log("power browser fixture ready")
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
				stop()
				<-done
				if calls.Load() != 5 || f.powerInvariant(t) != before {
					t.Fatal("browser power invariant", calls.Load())
				}
				var unfinished int
				if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND status<>'succeeded'", f.sid).Scan(&unfinished); err != nil || unfinished != 0 {
					t.Fatal(unfinished, err)
				}
				return
			}
		}
	}
}
