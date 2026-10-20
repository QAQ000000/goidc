package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/store"
)

func TestNOKVMCDROMRevocationDuringPreflight(t *testing.T) {
	entered, released := make(chan struct{}), make(chan struct{})
	var block atomic.Bool
	remote := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/virtual/42":
			fmt.Fprint(w, `{"code":0,"data":{"id":42,"cd_roms_id":0}}`)
		case "/api/cd_rom/":
			if block.Load() {
				close(entered)
				<-released
			}
			fmt.Fprint(w, `{"code":0,"data":[{"id":10,"name":"Rescue","desc":"Recovery"}]}`)
		default:
			if strings.Contains(r.URL.Path, "run_status") {
				fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
			} else if r.Method == "POST" {
				fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"vm"}}`)
			} else {
				fmt.Fprint(w, `{"code":0,"data":{"name":"vm"}}`)
			}
		}
	}
	f := nokvmFixtureSetup(t, remote, true)
	f.process(t)
	block.Store(true)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/cdrom", f.sid), map[string]any{"operation": "mount", "image_id": "10", "risk_acknowledged": true}, f.customer, "cdrom-revoked-key")
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("preflight did not start")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", f.uid); err != nil {
		t.Fatal(err)
	}
	close(released)
	select {
	case response := <-done:
		expect(t, response, 401)
	case <-time.After(5 * time.Second):
		t.Fatal("preflight did not finish")
	}
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='cdrom_mount'", f.sid).Scan(&count); err != nil || count != 0 {
		t.Fatal("revoked request queued", count, err)
	}
}

func TestNOKVMCDROMHTTPPermissionsReplayAndVerification(t *testing.T) {
	var writes atomic.Int32
	var mounted atomic.Bool
	remote := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/virtual/42":
			id := 0
			if mounted.Load() {
				id = 10
			}
			fmt.Fprintf(w, `{"code":0,"data":{"id":42,"cd_roms_id":%d}}`, id)
		case "/api/cd_rom/":
			fmt.Fprint(w, `{"code":0,"data":[{"id":10,"name":"Rescue","desc":"Recovery","supplier_password":"secret"}]}`)
		case "/api/virtual_cd_rom/42/mount/10":
			writes.Add(1)
			mounted.Store(true)
			fmt.Fprint(w, `{"code":0}`)
		case "/api/virtual_cd_rom/42/unmount/10":
			writes.Add(1)
			mounted.Store(false)
			fmt.Fprint(w, `{"code":0}`)
		default:
			if strings.Contains(r.URL.Path, "run_status") {
				fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
			} else if r.Method == "POST" {
				fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"vm","public_ip":[{"ip":"192.0.2.42"}]}}`)
			} else {
				fmt.Fprint(w, `{"code":0,"data":{"name":"vm","public_ip":[{"ip":"192.0.2.42"}]}}`)
			}
		}
	}
	f := nokvmFixtureSetup(t, remote, true, func(d *provision.Delivery) { d.Resources.Snapshots = 1 })
	f.process(t)
	path := fmt.Sprintf("/api/v1/services/%d/cdrom", f.sid)
	adminPath := fmt.Sprintf("/api/v1/admin/services/%d/cdrom", f.sid)
	readerID, reader := user(t, f.db, f.h, "cdrom-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", adminPath, nil, reader, ""), 200)
	expect(t, request(f.h, "POST", adminPath, map[string]any{"operation": "mount", "image_id": "10", "risk_acknowledged": true}, reader, "cdrom-reader-key"), 403)
	expect(t, request(f.h, "GET", path, nil, f.other, ""), 404)
	expect(t, request(f.h, "POST", path, map[string]any{"operation": "mount", "image_id": "10", "risk_acknowledged": false}, f.customer, "cdrom-no-risk-key"), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/actions", f.sid), map[string]string{"action": "cdrom_mount"}, f.customer, "cdrom-bypass-key"), 409)
	before := f.powerInvariant(t)
	for _, op := range []string{"mount", "unmount"} {
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET created_at=now()-interval '2 minutes' WHERE service_id=$1 AND action IN ('cdrom_mount','cdrom_unmount')", f.sid); err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"operation": op, "image_id": "10", "risk_acknowledged": true}
		key := "cdrom-" + op + "-key"
		queued := request(f.h, "POST", path, body, f.customer, key)
		expect(t, queued, 202)
		id := int64(data(t, queued)["job_id"].(float64))
		for _, q := range []string{"UPDATE provision_jobs SET request_payload='{}' WHERE id=$1", "UPDATE provision_jobs SET idempotency_key='altered' WHERE id=$1"} {
			if _, err := f.db.Pool.Exec(t.Context(), q, id); err == nil {
				t.Fatal("mutable cdrom job")
			}
		}
		job := f.process(t)
		var status string
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "waiting_verification" {
			t.Fatal(status, err)
		}
		expect(t, request(f.h, "POST", path, body, f.customer, key), 200)
		expect(t, request(f.h, "POST", path, map[string]any{"operation": op, "image_id": "11", "risk_acknowledged": true}, f.customer, key), 409)
		verify := store.DeliveryVerification{JobID: job.ID, Resolution: "applied", Note: "现场确认光驱状态及启动顺序"}
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery-verification", f.sid), verify, f.root, ""), 200)
	}
	if writes.Load() != 2 || f.powerInvariant(t) != before {
		t.Fatal("duplicate command or financial state changed", writes.Load())
	}
	var raw []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT request_payload FROM provision_jobs WHERE service_id=$1 AND action='cdrom_mount'", f.sid).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	json.Unmarshal(raw, &payload)
	if strings.Contains(string(raw), "secret") {
		t.Fatal("supplier secret retained")
	}
}

func TestNOKVMCDROMUnknownNeverResent(t *testing.T) {
	var writes atomic.Int32
	remote := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/virtual/42":
			fmt.Fprint(w, `{"code":0,"data":{"id":42,"cd_roms_id":0}}`)
		case "/api/cd_rom/":
			fmt.Fprint(w, `{"code":0,"data":[{"id":10,"name":"Rescue","desc":"Recovery"}]}`)
		case "/api/virtual_cd_rom/42/mount/10":
			writes.Add(1)
			fmt.Fprint(w, `{"code":0,"code":0}`)
		default:
			if strings.Contains(r.URL.Path, "run_status") {
				fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
			} else if r.Method == "POST" {
				fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"vm"}}`)
			} else {
				fmt.Fprint(w, `{"code":0,"data":{"name":"vm"}}`)
			}
		}
	}
	f := nokvmFixtureSetup(t, remote, true)
	f.process(t)
	path := fmt.Sprintf("/api/v1/services/%d/cdrom", f.sid)
	expect(t, request(f.h, "POST", path, map[string]any{"operation": "mount", "image_id": "10", "risk_acknowledged": true}, f.customer, "cdrom-unknown-key"), 202)
	job := f.process(t)
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "waiting_verification" {
		t.Fatal(status, err)
	}
	if err := f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatal("unknown command resent", writes.Load())
	}
}
