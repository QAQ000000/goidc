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

func TestNOKVMStorageBrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_BROWSER_READY_FILE"), os.Getenv("ZJMFGO_BROWSER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in isolated browser fixture")
	}
	for _, path := range []string{ready, finish} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("use fresh paths")
		}
	}
	f, remote := storageFixture(t)
	before := f.powerInvariant(t)
	var original []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT credentials_encrypted FROM services WHERE id=$1", f.sid).Scan(&original); err != nil {
		t.Fatal(err)
	}
	previous, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.sid, original)
	if err != nil {
		t.Fatal(err)
	}
	readerID, _ := user(t, f.db, f.h, "storage-browser-reader", "admin")
	if _, err = f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(f.db, config.Config{PublicOrigin: "http://localhost:3099", NOKVMProfiles: f.db.Provisioners, ProvisionEncryptionKey: f.db.ProvisionEncryptionKey}, slog.Default()).Handler())
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
				// Test-only clock acceleration after verified completion, and delayed sync.
				if _, err := f.db.Pool.Exec(ctx, `UPDATE provision_jobs SET created_at=now()-interval '2 minutes' WHERE service_id=$1 AND (action LIKE 'snapshot_%' OR action LIKE 'backup_%') AND status='succeeded' AND created_at>now()-interval '1 minute'`, f.sid); err != nil {
					if ctx.Err() == nil {
						t.Error(err)
					}
					return
				}
				if _, err := f.db.Pool.Exec(ctx, `UPDATE provision_jobs SET available_at=now() WHERE service_id=$1 AND action='sync' AND status='pending'`, f.sid); err != nil {
					if ctx.Err() == nil {
						t.Error(err)
					}
					return
				}
				job, err := f.db.ClaimProvisionJob(ctx)
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				if err == nil {
					err = f.db.ProcessProvisionJob(ctx, *job)
				}
				if err != nil {
					if ctx.Err() == nil {
						t.Error(err)
					}
					return
				}
			}
		}
	}()
	defer func() { stop(); <-done }()
	manifest, _ := json.Marshal(map[string]any{"api_origin": server.URL, "service_id": f.sid, "customer": "nokvm-customer", "admin": "nokvm-root", "reader": "storage-browser-reader"})
	if err = os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	t.Log("storage browser fixture ready")
	deadline := time.NewTimer(25 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser fixture timeout")
		case <-tick.C:
			if _, err := os.Stat(finish); err != nil {
				continue
			}
			stop()
			<-done
			var completed, pending int
			var sealed []byte
			var installed, login, power, raw string
			if err = f.db.Pool.QueryRow(t.Context(), `SELECT credentials_encrypted,installed_os->>'name',username,resource_info->>'power',
		(SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND (action LIKE 'snapshot_%' OR action LIKE 'backup_%') AND status='succeeded'),
		(SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND status IN ('pending','running','waiting_verification')),
		(SELECT string_agg(payload::text,'') FROM audit_events)||(SELECT string_agg(result::text,'') FROM provision_jobs) FROM services WHERE id=$1`, f.sid).Scan(&sealed, &installed, &login, &power, &completed, &pending, &raw); err != nil {
				t.Fatal(err)
			}
			current, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.sid, sealed)
			if err != nil || remote.writes.Load() != 6 || completed != 6 || pending != 0 || installed != "Debian 13" || login != "root" || power != "on" || current.Password != "fixture-browser-Storage123!" || current.VNCPassword != previous.VNCPassword || strings.Contains(raw, current.Password) || strings.Contains(raw, previous.Password) || f.powerInvariant(t) != before {
				t.Fatal("storage browser invariant", remote.writes.Load(), completed, pending, installed, login, power, err)
			}
			t.Log("six storage commands verified; finance, billing, stock and VNC unchanged; no plaintext audit credentials")
			return
		}
	}
}
