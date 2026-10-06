package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/store"
)

type reinstallRemote struct {
	passwordRemote
	reinstalls        atomic.Int32
	entered, released chan struct{}
}

func (p *reinstallRemote) handler(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "virtual_reset_system") {
		p.reinstalls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/virtual_reset_system/42/9" {
			w.WriteHeader(404)
			return
		}
		if p.entered != nil {
			close(p.entered)
			<-p.released
		}
		switch p.mode {
		case "rejected":
			fmt.Fprint(w, `{"code":8}`)
		case "unknown":
			fmt.Fprint(w, `{"message":"fixture-private-token"}`)
		default:
			fmt.Fprint(w, `{"code":0}`)
		}
		return
	}
	p.passwordRemote.handler(w, r)
}

func reinstallFixture(t *testing.T, mode string) (nokvmFixture, *reinstallRemote, provision.Credentials) {
	t.Helper()
	remote := &reinstallRemote{passwordRemote: passwordRemote{mode: mode}}
	f := nokvmFixtureSetup(t, remote.handler, true)
	f.process(t)
	f.delivery.Templates = []provision.Template{{ID: 9, Name: "Windows Server 2025", Group: "Windows", Login: "administrator"}, {ID: 7, Name: "Debian 13", Group: "Linux", Login: "root"}}
	raw, _ := json.Marshal(f.delivery)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET delivery_config=$2 WHERE id=$1", f.pid, raw); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT credentials_encrypted FROM services WHERE id=$1", f.sid).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	previous, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.sid, sealed)
	if err != nil {
		t.Fatal(err)
	}
	return f, remote, previous
}
func reinstallBody(id int64) map[string]any {
	return map[string]any{"template_id": id, "data_loss_acknowledged": true}
}

func TestNOKVMReinstallCataloguePermissionsAndImmutableReplay(t *testing.T) {
	f, remote, _ := reinstallFixture(t, "accepted")
	before := f.powerInvariant(t)
	path := fmt.Sprintf("/api/v1/services/%d", f.sid)
	adminPath := fmt.Sprintf("/api/v1/admin/services/%d", f.sid)
	readerID, reader := user(t, f.db, f.h, "reinstall-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	staffID, staff := user(t, f.db, f.h, "reinstall-scope", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", path+"/reinstall-templates", nil, f.other, ""), 404)
	expect(t, request(f.h, "GET", adminPath+"/reinstall-templates", nil, staff, ""), 404)
	expect(t, request(f.h, "POST", adminPath+"/reinstall", reinstallBody(9), reader, "reinstall-reader-key"), 403)
	expect(t, request(f.h, "POST", adminPath+"/reinstall", reinstallBody(9), staff, "reinstall-staff-key"), 404)
	expect(t, request(f.h, "POST", path+"/reinstall", reinstallBody(9), f.other, "reinstall-other-key"), 404)
	for _, auth := range []string{f.customer, reader} {
		route := path
		if auth == reader {
			route = adminPath
		}
		w := request(f.h, "GET", route+"/reinstall-templates", nil, auth, "")
		expect(t, w, 200)
		if w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "Windows Server 2025") || strings.Contains(w.Body.String(), "profile_id") {
			t.Fatal("catalogue visibility")
		}
	}
	for _, body := range []map[string]any{{"template_id": 9, "data_loss_acknowledged": false}, reinstallBody(8), reinstallBody(0)} {
		expect(t, request(f.h, "POST", path+"/reinstall", body, f.customer, "reinstall-invalid-key"), 409)
	}
	expect(t, request(f.h, "POST", path+"/actions", map[string]string{"action": "reinstall"}, f.customer, "reinstall-bypass-key"), 409)
	w := request(f.h, "POST", path+"/reinstall", reinstallBody(9), f.customer, "reinstall-immutable-key")
	expect(t, w, 202)
	id := int64(data(t, w)["job_id"].(float64))
	expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, ""), 409)
	for _, query := range []string{"UPDATE provision_jobs SET request_payload='{}' WHERE id=$1", "UPDATE provision_jobs SET request_fingerprint=repeat('a',64) WHERE id=$1", "UPDATE provision_jobs SET action='sync' WHERE id=$1", "UPDATE provision_jobs SET idempotency_key='changed-key' WHERE id=$1"} {
		if _, err := f.db.Pool.Exec(t.Context(), query, id); err == nil {
			t.Fatal("mutable reinstall request")
		}
	}
	// Catalogue changes cannot alter an already queued request or invalidate replay.
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE products SET delivery_config=jsonb_set(delivery_config,'{templates}','[]') WHERE id=$1`, f.pid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/reinstall", reinstallBody(9), f.customer, "reinstall-immutable-key"), 200)
	expect(t, request(f.h, "POST", path+"/reinstall", reinstallBody(7), f.customer, "reinstall-immutable-key"), 409)
	f.process(t)
	var status, name, power string
	var accepted bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT j.status,j.request_payload->>'name',(j.result->>'command_accepted')::boolean,s.resource_info->>'power' FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE j.id=$1`, id).Scan(&status, &name, &accepted, &power); err != nil || status != "waiting_verification" || name != "Windows Server 2025" || !accepted || power != "processing" {
		t.Fatal(status, name, accepted, power, err)
	}
	expect(t, request(f.h, "POST", path+"/actions", map[string]string{"action": "sync"}, f.customer, "reinstall-inflight-sync"), 409)
	expect(t, request(f.h, "POST", path+"/reset-password", map[string]string{"password": "fixture-new-password123!"}, f.customer, "reinstall-inflight-password"), 409)
	expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, ""), 409)
	if remote.reinstalls.Load() != 1 || f.powerInvariant(t) != before {
		t.Fatal("dispatch or finance invariant")
	}
}

func TestNOKVMReinstallVerificationCredentialsAndFinance(t *testing.T) {
	for _, mode := range []string{"accepted", "unknown", "not_applied", "rejected", "audit"} {
		t.Run(mode, func(t *testing.T) {
			f, remote, previous := reinstallFixture(t, mode)
			before := f.powerInvariant(t)
			path := fmt.Sprintf("/api/v1/services/%d", f.sid)
			queued := request(f.h, "POST", path+"/reinstall", reinstallBody(9), f.customer, "reinstall-lifecycle-key")
			expect(t, queued, 202)
			job := f.process(t)
			if mode == "rejected" {
				expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, ""), 200)
				var status string
				if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "failed" {
					t.Fatal(status, err)
				}
			} else {
				verify := fmt.Sprintf("/api/v1/admin/services/%d/delivery-verification", f.sid)
				body := store.DeliveryVerification{JobID: job.ID, Resolution: "applied", Note: "供应商控制台确认安装及客户机登录完成", Password: "fixture-reinstall-New123!"}
				invalid := body
				invalid.Password = ""
				expect(t, request(f.h, "POST", verify, invalid, f.root, ""), 409)
				invalid = body
				invalid.Note += " " + body.Password
				expect(t, request(f.h, "POST", verify, invalid, f.root, ""), 409)
				invalid = body
				invalid.UpstreamID = "43"
				expect(t, request(f.h, "POST", verify, invalid, f.root, ""), 409)
				if mode == "not_applied" {
					body.Resolution = "not_applied"
					body.Password = ""
				}
				if mode == "audit" {
					if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reinstall_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.nokvm.verified' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER reinstall_audit_failure BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reinstall_audit_failure()`); err != nil {
						t.Fatal(err)
					}
					expect(t, request(f.h, "POST", verify, body, f.root, ""), 500)
					expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, ""), 409)
					if _, err := f.db.Pool.Exec(t.Context(), `DROP TRIGGER reinstall_audit_failure ON audit_events; DROP FUNCTION reinstall_audit_failure()`); err != nil {
						t.Fatal(err)
					}
				}
				verified := request(f.h, "POST", verify, body, f.root, "")
				expect(t, verified, 200)
				if verified.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("cacheable verification")
				}
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 200)
				invalid = body
				invalid.Note += "changed"
				expect(t, request(f.h, "POST", verify, invalid, f.root, ""), 409)
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET available_at=now() WHERE service_id=$1 AND status='pending'", f.sid); err != nil {
					t.Fatal(err)
				}
				f.process(t)
				credential := request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, "")
				expect(t, credential, 200)
				want := body.Password
				login := "administrator"
				if mode == "not_applied" {
					want = previous.Password
					login = "root"
				}
				if data(t, credential)["password"] != want || data(t, credential)["username"] != login {
					t.Fatal("wrong verified credential")
				}
				var sealed []byte
				var installed, snapshot, audit string
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT credentials_encrypted,installed_os::text,delivery_snapshot::text,(SELECT string_agg(payload::text,'') FROM audit_events) FROM services WHERE id=$1`, f.sid).Scan(&sealed, &installed, &snapshot, &audit); err != nil {
					t.Fatal(err)
				}
				value, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.sid, sealed)
				if err != nil || value.VNCPassword != previous.VNCPassword || strings.Contains(audit, want) || strings.Contains(snapshot, "Windows Server") {
					t.Fatal("secret/snapshot invariant", err)
				}
				if mode == "not_applied" && installed != "{}" || mode != "not_applied" && !strings.Contains(installed, "Windows Server 2025") {
					t.Fatal("installed template", installed)
				}
			}
			expect(t, request(f.h, "POST", path+"/reinstall", reinstallBody(9), f.customer, "reinstall-lifecycle-key"), 200)
			expect(t, request(f.h, "POST", path+"/reinstall", reinstallBody(9), f.customer, "reinstall-cooldown-key"), 409)
			if remote.reinstalls.Load() != 1 || f.powerInvariant(t) != before {
				t.Fatal("duplicate reinstall or changed billing")
			}
		})
	}
}

func TestNOKVMReinstallModesProfileChangeAndRecovery(t *testing.T) {
	f, remote, _ := reinstallFixture(t, "unknown")
	path := fmt.Sprintf("/api/v1/services/%d", f.sid)
	for _, mode := range []string{"manual", "simulation"} {
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode=$2 WHERE id=$1", f.sid, mode); err != nil {
			t.Fatal(err)
		}
		expect(t, request(f.h, "POST", path+"/reinstall", reinstallBody(9), f.customer, "reinstall-mode-"+mode), 409)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode='nokvm',status='suspended' WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/reinstall", reinstallBody(9), f.customer, "reinstall-suspended-key"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE products SET delivery_config=jsonb_set(delivery_config,'{profile_id}','"changed"') WHERE id=$1`, f.pid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/reinstall", reinstallBody(9), f.customer, "reinstall-profile-key"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE products SET delivery_config=jsonb_set(delivery_config,'{profile_id}','"fixture"') WHERE id=$1`, f.pid); err != nil {
		t.Fatal(err)
	}
	remote.entered, remote.released = make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-remote.released:
		default:
			close(remote.released)
		}
	})
	expect(t, request(f.h, "POST", path+"/reinstall", reinstallBody(9), f.customer, "reinstall-network-key"), 202)
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.db.ProcessProvisionJob(context.Background(), *job) }()
	select {
	case <-remote.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no dispatch")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	tx, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "SELECT id FROM services WHERE id=$1 FOR UPDATE", f.sid); err != nil {
		t.Fatal("network holds service lock", err)
	}
	tx.Rollback(ctx)
	if _, err = f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET locked_at=now()-interval '6 minutes' WHERE id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	close(remote.released)
	if err = <-done; err == nil {
		t.Fatal("late result was not fenced")
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("repeated worker accepted")
	}
	expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, ""), 409)
	if remote.reinstalls.Load() != 1 {
		t.Fatal("reinstall resent")
	}
}
