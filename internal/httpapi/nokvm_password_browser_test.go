package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/provision"
	"github.com/jackc/pgx/v5"
)

func TestNOKVMPasswordBrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_BROWSER_READY_FILE"), os.Getenv("ZJMFGO_BROWSER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in isolated browser fixture")
	}
	for _, path := range []string{ready, finish} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("use fresh fixture paths")
		}
	}
	f, remote, previous := passwordFixture(t, "browser")
	before := f.powerInvariant(t)
	readerID, _ := user(t, f.db, f.h, "password-browser-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(f.db, config.Config{PublicOrigin: "http://localhost:3099", PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", NOKVMProfiles: f.db.Provisioners, ProvisionEncryptionKey: f.db.ProvisionEncryptionKey}, slog.Default()).Handler())
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
	manifest, _ := json.Marshal(map[string]any{"api_origin": server.URL, "service_id": f.sid, "customer": "nokvm-customer", "admin": "nokvm-root", "reader": "password-browser-reader"})
	if err := os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	t.Log("password browser fixture ready")
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
				if remote.resets.Load() != 2 || f.powerInvariant(t) != before {
					t.Fatal("browser password invariants", remote.resets.Load())
				}
				var sealed []byte
				var completed, pending int
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT credentials_encrypted,
 (SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='reset_password' AND status='succeeded'),
 (SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND request_credentials_encrypted IS NOT NULL)
 FROM services WHERE id=$1`, f.sid).Scan(&sealed, &completed, &pending); err != nil || completed != 2 || pending != 0 {
					t.Fatal(completed, pending, err)
				}
				current, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.sid, sealed)
				if err != nil || current.Password != "fixture-browser-Unknown123!" || current.VNCPassword != previous.VNCPassword {
					t.Fatal("browser credential confirmation", err)
				}
				var raw string
				if err = f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT string_agg(payload::text,'') FROM audit_events)||(SELECT string_agg(result::text,'') FROM provision_jobs)`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				if bytesContainPassword(raw, "fixture-browser-New123!", "fixture-browser-Unknown123!", previous.Password) {
					t.Fatal("browser leaked secret")
				}
				return
			}
		}
	}
}

func bytesContainPassword(raw string, passwords ...string) bool {
	for _, password := range passwords {
		if strings.Contains(raw, password) {
			return true
		}
	}
	return false
}
