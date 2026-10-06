package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/store"
)

type natRemote struct {
	mu                        sync.Mutex
	items                     map[string][]provision.NATItem
	mode                      string
	writes                    atomic.Int32
	entered, released         chan struct{}
	listEntered, listReleased chan struct{}
}

func newNATRemote() *natRemote {
	return &natRemote{items: map[string][]provision.NATItem{
		"acl": {{ID: "9007199254740993", Name: "SSH", Type: "tcp", ExteriorPort: 21022, InteriorPort: 22}},
		"web": {{ID: "9007199254740994", Domain: "example.test", ExteriorPort: 80, InteriorPort: 8080}},
	}}
}

func (p *natRemote) handler(w http.ResponseWriter, r *http.Request) {
	kind, path := "acl", "/api/nat_acl"
	if strings.HasPrefix(r.URL.Path, "/api/nat_web") {
		kind, path = "web", "/api/nat_web"
	}
	if r.URL.Path == path+"/42" && r.Method == "GET" {
		if p.listEntered != nil {
			close(p.listEntered)
			<-p.listReleased
		}
		p.mu.Lock()
		items := append([]provision.NATItem{}, p.items[kind]...)
		p.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": items})
		return
	}
	if strings.HasPrefix(r.URL.Path, path) {
		p.writes.Add(1)
		if p.entered != nil {
			close(p.entered)
			<-p.released
		}
		p.mu.Lock()
		mode := p.mode
		if mode != "rejected" {
			if r.Method == "POST" {
				r.ParseForm()
				item := provision.NATItem{ID: "9007199254740995", Name: r.PostForm.Get("name"), Type: r.PostForm.Get("type"), Domain: r.PostForm.Get("domain"), InteriorPort: 8081, ExteriorPort: 21023}
				if kind == "web" {
					item.ExteriorPort = 80
				}
				p.items[kind] = append(p.items[kind], item)
			} else {
				id := strings.TrimPrefix(r.URL.Path, path+"/")
				rows := []provision.NATItem{}
				for _, item := range p.items[kind] {
					if item.ID != id {
						rows = append(rows, item)
					}
				}
				p.items[kind] = rows
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
		fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"nat-vm","public_ip":[{"ip":"192.0.2.42"}]}}`)
		return
	}
	fmt.Fprint(w, `{"code":0,"data":{"name":"nat-vm","public_ip":[{"ip":"192.0.2.42"}]}}`)
}

func natFixture(t *testing.T) (nokvmFixture, *natRemote) {
	t.Helper()
	remote := newNATRemote()
	f := nokvmFixtureSetup(t, remote.handler, true, func(d *provision.Delivery) { d.Resources.NATACL = 2; d.Resources.NATWeb = 2 })
	f.process(t)
	return f, remote
}
func natPathFor(f nokvmFixture, kind string, admin bool) string {
	prefix := ""
	if admin {
		prefix = "/admin"
	}
	return fmt.Sprintf("/api/v1%s/services/%d/nat/%s", prefix, f.sid, kind)
}
func natBody(kind, operation, id string) map[string]any {
	b := map[string]any{"operation": operation, "point_id": id, "risk_acknowledged": true}
	if operation == "create" {
		b["interior_port"] = 8081
		if kind == "acl" {
			b["name"] = "SSH new"
			b["type"] = "tcp"
		} else {
			b["domain"] = "new.example.test"
			b["exterior_port"] = 80
		}
	}
	return b
}
func natAge(t *testing.T, f nokvmFixture) {
	t.Helper()
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET created_at=now()-interval '2 minutes' WHERE service_id=$1 AND action LIKE 'nat_%'", f.sid); err != nil {
		t.Fatal(err)
	}
}

func TestNOKVMNATFourActionsAndFinance(t *testing.T) {
	f, remote := natFixture(t)
	before := f.powerInvariant(t)
	var credentials []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT credentials_encrypted FROM services WHERE id=$1", f.sid).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"acl", "web"} {
		for _, op := range []string{"create", "delete"} {
			natAge(t, f)
			id := ""
			if op == "delete" {
				if kind == "acl" {
					id = "9007199254740993"
				} else {
					id = "9007199254740994"
				}
			}
			path, body := natPathFor(f, kind, false), natBody(kind, op, id)
			key := "nat-lifecycle-" + kind + "-" + op
			queued := request(f.h, "POST", path, body, f.customer, key)
			expect(t, queued, 202)
			job := f.process(t)
			var status string
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "waiting_verification" {
				t.Fatal(status, err)
			}
			expect(t, request(f.h, "POST", path, body, f.customer, key), 200)
			changed := natBody(kind, op, id)
			changed["interior_port"] = 9090
			expect(t, request(f.h, "POST", path, changed, f.customer, key), 409)
			verify := store.DeliveryVerification{JobID: job.ID, Resolution: "applied", Note: "已核验供应商控制台实际 NAT 资源"}
			endpoint := fmt.Sprintf("/api/v1/admin/services/%d/delivery-verification", f.sid)
			expect(t, request(f.h, "POST", endpoint, verify, f.root, ""), 200)
			if f.powerInvariant(t) != before {
				t.Fatal("NAT changed finance/billing/stock")
			}
		}
	}
	if remote.writes.Load() != 4 {
		t.Fatal("unexpected supplier writes", remote.writes.Load())
	}
	var after []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT credentials_encrypted FROM services WHERE id=$1", f.sid).Scan(&after); err != nil || string(credentials) != string(after) {
		t.Fatal("NAT changed credentials", err)
	}
}

func TestNOKVMNATPermissionsQuotaAndPayload(t *testing.T) {
	f, remote := natFixture(t)
	path := natPathFor(f, "acl", false)
	readerID, reader := user(t, f.db, f.h, "nat-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", natPathFor(f, "acl", true), nil, reader, ""), 200)
	expect(t, request(f.h, "POST", natPathFor(f, "acl", true), natBody("acl", "create", ""), reader, "nat-reader-denied"), 403)
	expect(t, request(f.h, "GET", path, nil, f.other, ""), 404)
	expect(t, request(f.h, "POST", path, natBody("acl", "delete", "9007199254740994"), f.customer, "nat-other-host-point"), 404)
	expect(t, request(f.h, "POST", path, natBody("acl", "delete", "999"), f.customer, "nat-foreign-point"), 404)
	b := natBody("acl", "create", "")
	b["risk_acknowledged"] = false
	expect(t, request(f.h, "POST", path, b, f.customer, "nat-no-ack"), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/actions", f.sid), map[string]string{"action": "nat_acl_delete"}, f.customer, "nat-generic-bypass"), 409)
	remote.mu.Lock()
	remote.items["acl"] = append(remote.items["acl"], provision.NATItem{ID: "9007199254740996", Name: "full", Type: "udp", ExteriorPort: 21024, InteriorPort: 25})
	remote.mu.Unlock()
	expect(t, request(f.h, "POST", path, natBody("acl", "create", ""), f.customer, "nat-quota-full"), 409)
	queued := request(f.h, "POST", path, natBody("acl", "delete", "9007199254740993"), f.customer, "nat-immutable-payload")
	expect(t, queued, 202)
	id := int64(data(t, queued)["job_id"].(float64))
	for _, q := range []string{"UPDATE provision_jobs SET request_payload='{}' WHERE id=$1", "UPDATE provision_jobs SET request_fingerprint=repeat('a',64) WHERE id=$1", "UPDATE provision_jobs SET action='sync' WHERE id=$1"} {
		if _, err := f.db.Pool.Exec(t.Context(), q, id); err == nil {
			t.Fatal("mutable NAT request")
		}
	}
	remote.mu.Lock()
	remote.items["acl"][0].Name = "changed"
	remote.mu.Unlock()
	job := f.process(t)
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "failed" || remote.writes.Load() != 0 {
		t.Fatal(status, err)
	}
}

func TestNOKVMNATConcurrentReplayUnknownAndAuditRollback(t *testing.T) {
	f, remote := natFixture(t)
	path := natPathFor(f, "web", false)
	body := natBody("web", "delete", "9007199254740994")
	var wg sync.WaitGroup
	codes := make(chan int, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- request(f.h, "POST", path, body, f.customer, "nat-concurrent-key").Code
		}()
	}
	wg.Wait()
	close(codes)
	created := 0
	for code := range codes {
		if code == 202 {
			created++
		} else if code != 200 {
			t.Fatal("concurrent result", code)
		}
	}
	if created != 1 {
		t.Fatal("multiple jobs", created)
	}
	remote.mode = "unknown"
	job := f.process(t)
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "waiting_verification" {
		t.Fatal(status, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION nat_audit_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.nokvm.verified' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER nat_audit_fail BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION nat_audit_fail()`); err != nil {
		t.Fatal(err)
	}
	verify := store.DeliveryVerification{JobID: job.ID, Resolution: "not_applied", Note: "已确认供应商未执行删除"}
	endpoint := fmt.Sprintf("/api/v1/admin/services/%d/delivery-verification", f.sid)
	expect(t, request(f.h, "POST", endpoint, verify, f.root, ""), 500)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "waiting_verification" {
		t.Fatal("audit rollback", status, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `DROP TRIGGER nat_audit_fail ON audit_events; DROP FUNCTION nat_audit_fail()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", endpoint, verify, f.root, ""), 200)
	if remote.writes.Load() != 1 {
		t.Fatal("unknown request resent", remote.writes.Load())
	}
}

func TestNOKVMNATAuthorizationAfterSupplierPreflight(t *testing.T) {
	for _, mode := range []string{"logout", "revoke_key", "disabled", "grant", "scope", "upstream"} {
		t.Run(mode, func(t *testing.T) {
			f, remote := natFixture(t)
			actor, cookie, admin := f.uid, f.customer, false
			if mode == "grant" || mode == "scope" {
				actor, cookie = user(t, f.db, f.h, "nat-preflight-admin", "admin")
				admin = true
			}
			remote.listEntered, remote.listReleased = make(chan struct{}), make(chan struct{})
			defer close(remote.listReleased)
			path := natPathFor(f, "acl", admin)
			done := make(chan int, 1)
			secret := "zjmfgo_" + strings.Repeat("a", 64)
			if mode == "revoke_key" {
				if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO api_keys(user_id,name,prefix,secret_hash) VALUES($1,'fixture','zjmfgo_1234567890123456',$2)`, actor, tokenHash(secret)); err != nil {
					t.Fatal(err)
				}
			}
			go func() {
				if mode == "revoke_key" {
					body, _ := json.Marshal(natBody("acl", "delete", "9007199254740993"))
					r := httptest.NewRequest("POST", path, bytes.NewReader(body))
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("Authorization", "Bearer "+secret)
					r.Header.Set("Idempotency-Key", "nat-preflight-key")
					w := httptest.NewRecorder()
					f.h.ServeHTTP(w, r)
					done <- w.Code
					return
				}
				done <- request(f.h, "POST", path, natBody("acl", "delete", "9007199254740993"), cookie, "nat-preflight-key").Code
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
			case code := <-done:
				want := 409
				if mode == "logout" || mode == "revoke_key" {
					want = 401
				}
				if mode == "disabled" || mode == "grant" {
					want = 403
				}
				if mode == "scope" {
					want = 404
				}
				if code != want {
					t.Fatalf("%s: got %d want %d", mode, code, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("preflight did not finish")
			}
			var jobs int
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='nat_acl_delete'", f.sid).Scan(&jobs); err != nil || jobs != 0 || remote.writes.Load() != 0 {
				t.Fatal("revoked request queued", jobs, err)
			}
		})
	}
}

func TestNOKVMNATLeaseRecoveryAndQuotaSnapshot(t *testing.T) {
	for _, mode := range []string{"lease", "quota"} {
		t.Run(mode, func(t *testing.T) {
			f, remote := natFixture(t)
			expect(t, request(f.h, "POST", natPathFor(f, "acl", false), natBody("acl", "delete", "9007199254740993"), f.customer, "nat-recovery-key"), 202)
			job, err := f.db.ClaimProvisionJob(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			writes := int32(0)
			if mode == "lease" {
				if _, err = f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET external_started_at=now(),locked_at=now()-interval '6 minutes' WHERE id=$1", job.ID); err != nil {
					t.Fatal(err)
				}
				if err = f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
					t.Fatal("expired NAT task dispatched")
				}
			} else {
				if _, err = f.db.Pool.Exec(t.Context(), `UPDATE services SET delivery_snapshot=jsonb_set(delivery_snapshot,'{resources,nat_acl_limit}','1') WHERE id=$1`, f.sid); err == nil {
					t.Fatal("purchased NAT quota changed")
				}
				if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
					t.Fatal(err)
				}
				writes = 1
			}
			var status string
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "waiting_verification" || remote.writes.Load() != writes {
				t.Fatal(status, err)
			}
		})
	}
}
