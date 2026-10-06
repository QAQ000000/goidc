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
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/provision"
	"github.com/jackc/pgx/v5"
)

func TestNOKVMResourcesBrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_BROWSER_READY_FILE"), os.Getenv("ZJMFGO_BROWSER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in isolated browser fixture")
	}
	for _, path := range []string{ready, finish} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("use fresh fixture paths")
		}
	}
	remote := newNATRemote()
	var mounted atomic.Bool
	var cdromWrites atomic.Int32
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/virtual/42":
			image := 0
			if mounted.Load() {
				image = 10
			}
			fmt.Fprintf(w, `{"code":0,"data":{"id":42,"cd_roms_id":%d,"name":"browser-resources","public_ip":[{"ip":"192.0.2.42"}]}}`, image)
		case "/api/cd_rom/":
			fmt.Fprint(w, `{"code":0,"data":[{"id":10,"name":"Rescue ISO","desc":"Recovery image"}]}`)
		case "/api/virtual_cd_rom/42/mount/10":
			cdromWrites.Add(1)
			mounted.Store(true)
			fmt.Fprint(w, `{"code":0}`)
		case "/api/virtual_cd_rom/42/unmount/10":
			cdromWrites.Add(1)
			mounted.Store(false)
			fmt.Fprint(w, `{"code":0}`)
		default:
			remote.handler(w, r)
		}
	}, true, func(d *provision.Delivery) { d.Resources.NATACL = 2; d.Resources.NATWeb = 2 })
	f.process(t)
	before := f.powerInvariant(t)
	var credentials []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT credentials_encrypted FROM services WHERE id=$1", f.sid).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	readerID, _ := user(t, f.db, f.h, "resources-browser-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	fileID := downloadID(t, f.h, f.root, downloadInput())
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
				if _, err := f.db.Pool.Exec(ctx, `UPDATE provision_jobs SET created_at=now()-interval '2 minutes' WHERE service_id=$1 AND (action LIKE 'nat_%' OR action LIKE 'cdrom_%') AND status='succeeded' AND created_at>now()-interval '1 minute'`, f.sid); err != nil {
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
	manifest, _ := json.Marshal(map[string]any{"api_origin": server.URL, "service_id": f.sid, "customer": "nokvm-customer", "admin": "nokvm-root", "reader": "resources-browser-reader", "download_id": fileID})
	if err := os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	t.Log("resources browser fixture ready")
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
			var after []byte
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT credentials_encrypted,
		(SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND (action LIKE 'nat_%' OR action LIKE 'cdrom_%') AND status='succeeded'),
		(SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND status IN ('pending','running','waiting_verification')) FROM services WHERE id=$1`, f.sid).Scan(&after, &completed, &pending); err != nil {
				t.Fatal(err)
			}
			if remote.writes.Load() != 4 || cdromWrites.Load() != 2 || completed != 6 || pending != 0 || mounted.Load() || string(credentials) != string(after) || f.powerInvariant(t) != before {
				t.Fatal("resource browser invariant", remote.writes.Load(), cdromWrites.Load(), completed, pending)
			}
			t.Log("six resource writes verified; unchanged finance, stock, billing and credentials; no pending jobs")
			return
		}
	}
}
