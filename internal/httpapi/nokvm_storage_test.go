package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type storageRemote struct {
	mu                        sync.Mutex
	points                    map[string][]provision.StoragePoint
	mode                      string
	writes                    atomic.Int32
	entered, released         chan struct{}
	listEntered, listReleased chan struct{}
}

func TestNOKVMStorageAuthorizationAfterPreflight(t *testing.T) {
	for _, mode := range []string{"logout", "revoke_key", "disabled", "grant", "scope", "upstream"} {
		t.Run(mode, func(t *testing.T) {
			f, remote := storageFixture(t)
			actor, cookie, admin := f.uid, f.customer, false
			if mode == "grant" || mode == "scope" {
				actor, cookie = user(t, f.db, f.h, "storage-preflight-admin", "admin")
				admin = true
			}
			secret := "zjmfgo_" + strings.Repeat("a", 64)
			if mode == "revoke_key" {
				if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO api_keys(user_id,name,prefix,secret_hash) VALUES($1,'fixture','zjmfgo_1234567890123456',$2)`, actor, tokenHash(secret)); err != nil {
					t.Fatal(err)
				}
			}
			remote.listEntered, remote.listReleased = make(chan struct{}), make(chan struct{})
			defer close(remote.listReleased)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				if mode != "revoke_key" {
					done <- request(f.h, "POST", storagePathFor(f, "snapshot", admin), storageBody("restore", "10"), cookie, "storage-preflight-key")
					return
				}
				body, _ := json.Marshal(storageBody("restore", "10"))
				r := httptest.NewRequest("POST", storagePathFor(f, "snapshot", false), bytes.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Authorization", "Bearer "+secret)
				r.Header.Set("Idempotency-Key", "storage-preflight-key")
				w := httptest.NewRecorder()
				f.h.ServeHTTP(w, r)
				done <- w
			}()
			select {
			case <-remote.listEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("preflight did not start")
			}
			var err error
			switch mode {
			case "logout":
				_, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", actor)
			case "revoke_key":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=now() WHERE user_id=$1", actor)
			case "disabled":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", actor)
			case "grant":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", actor)
			case "scope":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", actor)
			case "upstream":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET upstream_id='43' WHERE id=$1", f.sid)
			}
			if err != nil {
				t.Fatal(err)
			}
			remote.listReleased <- struct{}{}
			select {
			case w := <-done:
				want := 409
				if mode == "logout" || mode == "revoke_key" {
					want = 401
				}
				if mode == "grant" || mode == "disabled" {
					want = 403
				}
				if mode == "scope" {
					want = 404
				}
				expect(t, w, want)
			case <-time.After(5 * time.Second):
				t.Fatal("preflight did not finish")
			}
			var jobs int
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='snapshot_restore'", f.sid).Scan(&jobs); err != nil || jobs != 0 || remote.writes.Load() != 0 {
				t.Fatal("revoked request queued", jobs, err)
			}
		})
	}
}

func newStorageRemote() *storageRemote {
	return &storageRemote{points: map[string][]provision.StoragePoint{
		"snapshot": {{ID: "10", Name: "snapshot-original", CreatedAt: time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC)}},
		"backup":   {{ID: "9007199254740993", Name: "backup-original", CreatedAt: time.Date(2026, 10, 6, 5, 0, 0, 0, time.UTC)}},
	}}
}
func (p *storageRemote) handler(w http.ResponseWriter, r *http.Request) {
	kind := "snapshot"
	prefix := "/api/snapshot"
	if strings.HasPrefix(r.URL.Path, "/api/backups") {
		kind = "backup"
		prefix = "/api/backups"
	}
	if strings.HasPrefix(r.URL.Path, prefix) {
		if r.URL.Path == prefix+"/42" && r.Method == "GET" {
			if p.listEntered != nil {
				close(p.listEntered)
				<-p.listReleased
			}
			p.mu.Lock()
			rows := append([]provision.StoragePoint{}, p.points[kind]...)
			p.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": rows})
			return
		}
		p.writes.Add(1)
		if p.entered != nil {
			close(p.entered)
			<-p.released
		}
		p.mu.Lock()
		mode := p.mode
		if mode != "rejected" {
			if r.Method == "POST" {
				id := "12"
				if kind == "backup" {
					id = "9007199254740995"
				}
				p.points[kind] = append(p.points[kind], provision.StoragePoint{ID: id, Name: kind + "-new", CreatedAt: time.Date(2026, 10, 6, 6, 0, 0, 0, time.UTC)})
			}
			if r.Method == "DELETE" {
				id := strings.TrimPrefix(r.URL.Path, prefix+"/")
				rows := []provision.StoragePoint{}
				for _, point := range p.points[kind] {
					if point.ID != id {
						rows = append(rows, point)
					}
				}
				p.points[kind] = rows
			}
		}
		p.mu.Unlock()
		if mode == "unknown" {
			fmt.Fprint(w, `{"private":"supplier-secret"}`)
		} else if mode == "rejected" {
			fmt.Fprint(w, `{"code":8}`)
		} else {
			fmt.Fprint(w, `{"code":0}`)
		}
		return
	}
	if strings.Contains(r.URL.Path, "run_status") {
		fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
		return
	}
	if r.Method == "POST" {
		fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"storage-vm","public_ip":[{"ip":"192.0.2.42"}]}}`)
		return
	}
	fmt.Fprint(w, `{"code":0,"data":{"name":"storage-vm","public_ip":[{"ip":"192.0.2.42"}]}}`)
}
func storageFixture(t *testing.T) (nokvmFixture, *storageRemote) {
	t.Helper()
	remote := newStorageRemote()
	f := nokvmFixtureSetup(t, remote.handler, true, func(d *provision.Delivery) { d.Resources.Snapshots = 3; d.Resources.Backups = 2 })
	f.process(t)
	return f, remote
}
func storageBody(operation, id string) map[string]any {
	return map[string]any{"operation": operation, "point_id": id, "risk_acknowledged": true}
}
func storagePathFor(f nokvmFixture, kind string, admin bool) string {
	prefix := ""
	if admin {
		prefix = "/admin"
	}
	return fmt.Sprintf("/api/v1%s/services/%d/storage/%s", prefix, f.sid, kind)
}
func storageAge(t *testing.T, f nokvmFixture) {
	t.Helper()
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET created_at=now()-interval '2 minutes' WHERE service_id=$1 AND (action LIKE 'snapshot_%' OR action LIKE 'backup_%')", f.sid); err != nil {
		t.Fatal(err)
	}
}
func storageSync(t *testing.T, f nokvmFixture) {
	t.Helper()
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET available_at=now() WHERE service_id=$1 AND action='sync' AND status='pending'", f.sid); err != nil {
		t.Fatal(err)
	}
	f.process(t)
}

func TestNOKVMStorageSixActionsReplayAndFinance(t *testing.T) {
	f, remote := storageFixture(t)
	before := f.powerInvariant(t)
	var previous []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT credentials_encrypted FROM services WHERE id=$1", f.sid).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	old, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.sid, previous)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"snapshot", "backup"} {
		for _, op := range []string{"create", "delete", "restore"} {
			storageAge(t, f)
			id := ""
			if op == "delete" {
				id = "10"
				if kind == "backup" {
					id = "9007199254740993"
				}
			}
			if op == "restore" {
				id = "12"
				if kind == "backup" {
					id = "9007199254740995"
				}
			}
			key := "storage-lifecycle-" + kind + "-" + op
			path := storagePathFor(f, kind, false)
			body := storageBody(op, id)
			queued := request(f.h, "POST", path, body, f.customer, key)
			expect(t, queued, 202)
			job := f.process(t)
			var status string
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "waiting_verification" {
				t.Fatal(status, err)
			}
			// Replay survives a deleted remote point and bypasses cooldown/changed directory.
			expect(t, request(f.h, "POST", path, body, f.customer, key), 200)
			different := storageBody(op, "11")
			expect(t, request(f.h, "POST", path, different, f.customer, key), 409)
			credentialStatus := 200
			if op == "restore" {
				credentialStatus = 409
			}
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/credentials", f.sid), map[string]any{}, f.customer, ""), credentialStatus)
			verify := store.DeliveryVerification{JobID: job.ID, Resolution: "applied", Note: "已在供应商控制台确认实际操作完成"}
			if op == "restore" {
				verify.Password = "storage-RestoredPassword123!"
				verify.Username = "administrator"
				verify.OSName = "Windows Server 2025"
			}
			endpoint := fmt.Sprintf("/api/v1/admin/services/%d/delivery-verification", f.sid)
			expect(t, request(f.h, "POST", endpoint, verify, f.root, ""), 200)
			expect(t, request(f.h, "POST", endpoint, verify, f.root, ""), 200)
			if op == "restore" {
				storageSync(t, f)
			}
			if f.powerInvariant(t) != before {
				t.Fatal("storage changed finance/billing/stock")
			}
		}
	}
	if remote.writes.Load() != 6 {
		t.Fatal("storage replay duplicated commands", remote.writes.Load())
	}
	var sealed []byte
	var audit, os, username string
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT credentials_encrypted,installed_os->>'name',username,(SELECT string_agg(payload::text,'') FROM audit_events) FROM services WHERE id=$1`, f.sid).Scan(&sealed, &os, &username, &audit); err != nil {
		t.Fatal(err)
	}
	current, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.sid, sealed)
	if err != nil || current.Password != "storage-RestoredPassword123!" || current.VNCPassword != old.VNCPassword || os != "Windows Server 2025" || username != "administrator" || strings.Contains(audit, current.Password) {
		t.Fatal("restored credentials/system/audit invariant", err)
	}
}

func TestNOKVMStoragePermissionsQuotaAndImmutablePayload(t *testing.T) {
	f, remote := storageFixture(t)
	path := storagePathFor(f, "snapshot", false)
	readerID, reader := user(t, f.db, f.h, "storage-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", storagePathFor(f, "backup", true), nil, reader, ""), 200)
	expect(t, request(f.h, "POST", storagePathFor(f, "snapshot", true), storageBody("create", ""), reader, "storage-reader-denied"), 403)
	expect(t, request(f.h, "GET", path, nil, f.other, ""), 404)
	expect(t, request(f.h, "POST", path, storageBody("restore", "10"), f.other, "storage-other-denied"), 404)
	expect(t, request(f.h, "POST", path, storageBody("restore", "99"), f.customer, "storage-foreign-point"), 404)
	expect(t, request(f.h, "POST", path, map[string]any{"operation": "restore", "point_id": "10", "risk_acknowledged": false}, f.customer, "storage-no-risk-ack"), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/actions", f.sid), map[string]string{"action": "snapshot_restore"}, f.customer, "storage-generic-bypass"), 409)
	remote.mu.Lock()
	remote.points["snapshot"] = append(remote.points["snapshot"], provision.StoragePoint{ID: "11", Name: "snapshot-two", CreatedAt: time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC)}, provision.StoragePoint{ID: "12", Name: "snapshot-three", CreatedAt: time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC)})
	remote.mu.Unlock()
	expect(t, request(f.h, "POST", path, storageBody("create", ""), f.customer, "storage-quota-full"), 409)
	queued := request(f.h, "POST", path, storageBody("restore", "10"), f.customer, "storage-immutable-payload")
	expect(t, queued, 202)
	id := int64(data(t, queued)["job_id"].(float64))
	for _, q := range []string{"UPDATE provision_jobs SET request_payload='{}' WHERE id=$1", "UPDATE provision_jobs SET request_fingerprint=repeat('a',64) WHERE id=$1", "UPDATE provision_jobs SET action='sync' WHERE id=$1", "UPDATE provision_jobs SET idempotency_key='tampered' WHERE id=$1"} {
		if _, err := f.db.Pool.Exec(t.Context(), q, id); err == nil {
			t.Fatal("storage request mutable")
		}
	}
	remote.mu.Lock()
	remote.points["snapshot"][0].Name = "changed-remotely"
	remote.mu.Unlock()
	job := f.process(t)
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "failed" || remote.writes.Load() != 0 {
		t.Fatal("changed point dispatched", status, err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/credentials", f.sid), map[string]any{}, f.customer, ""), 200)
}

func TestNOKVMStorageUnknownRecoveryAndAuditRollback(t *testing.T) {
	for _, mode := range []string{"unknown", "rejected", "audit", "not_applied"} {
		t.Run(mode, func(t *testing.T) {
			f, remote := storageFixture(t)
			remote.mode = mode
			before := f.powerInvariant(t)
			expect(t, request(f.h, "POST", storagePathFor(f, "snapshot", false), storageBody("restore", "10"), f.customer, "storage-unknown-key"), 202)
			job := f.process(t)
			var status string
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if mode == "rejected" {
				if status != "failed" {
					t.Fatal(status)
				}
				return
			}
			if status != "waiting_verification" {
				t.Fatal(status)
			}
			body := store.DeliveryVerification{JobID: job.ID, Resolution: "applied", Note: "已核验系统恢复及实际客户机登录", Password: "storage-VerifiedPassword123!", Username: "root", OSName: "Debian 13"}
			route := fmt.Sprintf("/api/v1/admin/services/%d/delivery-verification", f.sid)
			invalid := body
			invalid.Password = ""
			expect(t, request(f.h, "POST", route, invalid, f.root, ""), 409)
			invalid = body
			invalid.Note += " " + body.Password
			expect(t, request(f.h, "POST", route, invalid, f.root, ""), 409)
			if mode == "audit" {
				if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION storage_audit_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.nokvm.verified' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER storage_audit_fail BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION storage_audit_fail()`); err != nil {
					t.Fatal(err)
				}
				expect(t, request(f.h, "POST", route, body, f.root, ""), 500)
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/credentials", f.sid), map[string]any{}, f.customer, ""), 409)
				if _, err := f.db.Pool.Exec(t.Context(), `DROP TRIGGER storage_audit_fail ON audit_events; DROP FUNCTION storage_audit_fail()`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "not_applied" {
				body.Resolution = "not_applied"
				body.Password = ""
				body.Username = ""
				body.OSName = ""
			}
			expect(t, request(f.h, "POST", route, body, f.root, ""), 200)
			storageSync(t, f)
			if remote.writes.Load() != 1 || f.powerInvariant(t) != before {
				t.Fatal("storage repeated/finance changed")
			}
		})
	}
}

func TestNOKVMStorageWorkerCancellationAndConcurrentReplay(t *testing.T) {
	f, remote := storageFixture(t)
	path := storagePathFor(f, "snapshot", false)
	var wg sync.WaitGroup
	codes := make(chan int, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- request(f.h, "POST", path, storageBody("restore", "10"), f.customer, "storage-concurrent-key").Code
		}()
	}
	wg.Wait()
	close(codes)
	count := 0
	for code := range codes {
		if code == 202 {
			count++
		} else if code != 200 {
			t.Fatalf("concurrent queue %d", code)
		}
	}
	if count != 1 {
		t.Fatal("multiple storage jobs", count)
	}
	remote.entered = make(chan struct{})
	remote.released = make(chan struct{})
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.db.ProcessProvisionJob(ctx, *job) }()
	<-remote.entered
	tx, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), "SELECT id FROM services WHERE id=$1 FOR UPDATE NOWAIT", f.sid)
	tx.Rollback(t.Context())
	if err != nil {
		t.Fatal("worker network held service lock", err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("duplicate dispatch allowed")
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled storage worker stuck")
	}
	close(remote.released)
	var status string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "waiting_verification" || remote.writes.Load() != 1 {
		t.Fatal("unknown cancellation outcome", status, err)
	}
}

func TestNOKVMStorageLeaseRecoveryAndQuotaChange(t *testing.T) {
	for _, change := range []string{"lease", "quota"} {
		t.Run(change, func(t *testing.T) {
			f, remote := storageFixture(t)
			expect(t, request(f.h, "POST", storagePathFor(f, "snapshot", false), storageBody("restore", "10"), f.customer, "storage-recovery-key"), 202)
			job, err := f.db.ClaimProvisionJob(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := "waiting_verification"
			writes := int32(0)
			if change == "lease" {
				if _, err = f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET external_started_at=now(),locked_at=now()-interval '6 minutes' WHERE id=$1", job.ID); err != nil {
					t.Fatal(err)
				}
				if err = f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
					t.Fatal("expired storage task dispatched")
				}
				want = "waiting_verification"
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/credentials", f.sid), map[string]any{}, f.customer, ""), 409)
			} else {
				if _, err = f.db.Pool.Exec(t.Context(), `UPDATE services SET delivery_snapshot=jsonb_set(delivery_snapshot,'{resources,snapshoot}','2') WHERE id=$1`, f.sid); err == nil {
					t.Fatal("purchased quota changed")
				}
				if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
					t.Fatal(err)
				}
				writes = 1
			}
			var status string
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != want || remote.writes.Load() != writes {
				t.Fatal("storage recovery", status, err)
			}
		})
	}
}
