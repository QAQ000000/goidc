package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/provision"
)

func TestNOKVMResourcesBrowserFixture(t *testing.T) {
	ready, finish := browserFixturePaths(t)
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
	server := httptest.NewUnstartedServer(New(f.db, config.Config{PublicOrigin: "http://localhost:3099", NOKVMProfiles: f.db.Provisioners, ProvisionEncryptionKey: f.db.ProvisionEncryptionKey}, slog.Default()).Handler())
	if addr := os.Getenv("ZJMFGO_BROWSER_API_ADDR"); addr != "" {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		server.Listener.Close()
		server.Listener = listener
	}
	server.Start()
	defer server.Close()
	stop := startBrowserProvisionWorker(t, f.db, func(ctx context.Context) error {
		if _, err := f.db.Pool.Exec(ctx, `UPDATE provision_jobs SET created_at=now()-interval '2 minutes' WHERE service_id=$1 AND (action LIKE 'nat_%' OR action LIKE 'cdrom_%') AND status='succeeded' AND created_at>now()-interval '1 minute'`, f.sid); err != nil {
			return err
		}
		return nil
	})
	defer stop()
	var schema string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	writeBrowserJSON(t, ready, map[string]any{"schema": schema, "api_origin": server.URL, "service_id": f.sid, "customer": "nokvm-customer", "admin": "nokvm-root", "reader": "resources-browser-reader", "download_id": fileID})
	waitBrowserFinish(t, ready, finish, 25*time.Minute)
	stop()
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
}
