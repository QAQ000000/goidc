package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/store"
)

type passwordRemote struct {
	mu               sync.Mutex
	password         string
	mode             string
	resets           atomic.Int32
	entered, release chan struct{}
}

func (p *passwordRemote) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PUT" {
		count := p.resets.Add(1)
		if p.entered != nil {
			close(p.entered)
			<-p.release
		}
		r.ParseForm()
		p.mu.Lock()
		if p.mode != "rejected" && p.mode != "mismatch" {
			p.password = r.PostForm.Get("password")
		}
		mode := p.mode
		if mode == "browser" && count == 2 {
			mode = "unknown"
		}
		p.mu.Unlock()
		switch mode {
		case "rejected":
			fmt.Fprint(w, `{"code":8}`)
		case "unknown":
			fmt.Fprint(w, `{"message":"fixture-private-token"}`)
		default:
			fmt.Fprint(w, `{"code":0}`)
		}
		return
	}
	if r.Method == "POST" {
		r.ParseForm()
		p.mu.Lock()
		p.password = r.PostForm.Get("sys_pwd")
		p.mu.Unlock()
		fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"password-vm-42","public_ip":[{"ip":"192.0.2.42"}]}}`)
	} else if strings.Contains(r.URL.Path, "run_status") {
		fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
	} else {
		p.mu.Lock()
		password := p.password
		p.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"name": "password-vm-42", "sys_pwd": password}})
	}
}

func passwordFixture(t *testing.T, mode string) (nokvmFixture, *passwordRemote, provision.Credentials) {
	t.Helper()
	remote := &passwordRemote{mode: mode}
	f := nokvmFixtureSetup(t, remote.handler, true)
	f.process(t)
	var sealed []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT credentials_encrypted FROM services WHERE id=$1", f.sid).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	credentials, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.sid, sealed)
	if err != nil {
		t.Fatal(err)
	}
	return f, remote, credentials
}

func TestNOKVMPasswordLifecyclePermissionsReplayAndRedaction(t *testing.T) {
	const password = "fixture-new-A&+=%123!"
	f, remote, previous := passwordFixture(t, "confirmed")
	before := f.powerInvariant(t)
	path := fmt.Sprintf("/api/v1/services/%d", f.sid)
	body := map[string]string{"password": password}
	readerID, reader := user(t, f.db, f.h, "password-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	staffID, staff := user(t, f.db, f.h, "password-scope", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/reset-password", body, f.other, "password-other-user"), 404)
	adminPath := strings.Replace(path, "/services/", "/admin/services/", 1)
	expect(t, request(f.h, "POST", adminPath+"/reset-password", body, reader, "password-reader-user"), 403)
	expect(t, request(f.h, "POST", adminPath+"/reset-password", body, staff, "password-scope-user"), 404)
	for _, invalid := range []string{"short", strings.Repeat("a", 129), "spaces in password"} {
		expect(t, request(f.h, "POST", path+"/reset-password", map[string]string{"password": invalid}, f.customer, "password-invalid-key"), 409)
	}
	queued := request(f.h, "POST", path+"/reset-password", body, f.customer, "password-reset-request")
	expect(t, queued, 202)
	if queued.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable reset")
	}
	jobID := int64(data(t, queued)["job_id"].(float64))
	expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, ""), 409)
	expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.other, ""), 404)
	expect(t, request(f.h, "POST", path+"/reset-password", body, f.customer, "password-reset-request"), 200)
	expect(t, request(f.h, "POST", path+"/reset-password", map[string]string{"password": "fixture-another-123!"}, f.customer, "password-reset-request"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET request_fingerprint=repeat('a',64) WHERE id=$1", jobID); err == nil {
		t.Fatal("mutable fingerprint")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET request_credentials_encrypted=decode('aa','hex') WHERE id=$1", jobID); err == nil {
		t.Fatal("mutable encrypted request")
	}
	for _, route := range []string{path, adminPath} {
		detail := request(f.h, "GET", route, nil, map[bool]string{true: f.root, false: f.customer}[route == adminPath], "")
		expect(t, detail, 200)
		if bytes.Contains(detail.Body.Bytes(), []byte(password)) || bytes.Contains(detail.Body.Bytes(), []byte("request_credentials")) || bytes.Contains(detail.Body.Bytes(), []byte("request_fingerprint")) {
			t.Fatal("reset request leaked")
		}
	}
	f.process(t)
	credential := request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, "")
	expect(t, credential, 200)
	if data(t, credential)["password"] != password || credential.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("new credentials not delivered")
	}
	var sealed []byte
	var pendingNull, confirmed bool
	var audit string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT s.credentials_encrypted,j.request_credentials_encrypted IS NULL,(j.result->>'password_confirmed')::boolean,(SELECT string_agg(payload::text,'') FROM audit_events) FROM services s JOIN provision_jobs j ON j.id=$2 WHERE s.id=$1`, f.sid, jobID).Scan(&sealed, &pendingNull, &confirmed, &audit); err != nil {
		t.Fatal(err)
	}
	current, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.sid, sealed)
	if err != nil || !pendingNull || !confirmed || current.Password != password || current.VNCPassword != previous.VNCPassword || strings.Contains(audit, password) || strings.Contains(audit, previous.Password) {
		t.Fatal("credential promotion or redaction", err)
	}
	expect(t, request(f.h, "POST", path+"/reset-password", body, f.customer, "password-reset-request"), 200)
	expect(t, request(f.h, "POST", path+"/reset-password", body, f.customer, "password-reset-cooldown"), 409)
	if remote.resets.Load() != 1 || f.powerInvariant(t) != before {
		t.Fatal("duplicate reset or changed billing")
	}
}

func TestNOKVMPasswordUnknownVerificationRejectionAndRollback(t *testing.T) {
	const password = "fixture-reset-123!"
	for _, mode := range []string{"rejected", "unknown", "mismatch", "audit"} {
		t.Run(mode, func(t *testing.T) {
			f, remote, previous := passwordFixture(t, mode)
			before := f.powerInvariant(t)
			path := fmt.Sprintf("/api/v1/services/%d", f.sid)
			w := request(f.h, "POST", path+"/reset-password", map[string]string{"password": password}, f.customer, "password-result-"+mode)
			expect(t, w, 202)
			if mode == "audit" {
				if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reset_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.nokvm.completed' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER reset_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reset_reject_audit()`); err != nil {
					t.Fatal(err)
				}
			}
			job, err := f.db.ClaimProvisionJob(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = f.db.ProcessProvisionJob(t.Context(), *job)
			if mode == "audit" {
				if err == nil {
					t.Fatal("audit did not roll back")
				}
				if _, err = f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET locked_at=now()-interval '6 minutes' WHERE id=$1", job.ID); err != nil {
					t.Fatal(err)
				}
				if err = f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var status string
			var pendingNull bool
			var sealed []byte
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT j.status,j.request_credentials_encrypted IS NULL,s.credentials_encrypted FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE j.id=$1", job.ID).Scan(&status, &pendingNull, &sealed); err != nil {
				t.Fatal(err)
			}
			old, err := provision.Unseal(f.db.ProvisionEncryptionKey, f.sid, sealed)
			if err != nil || old != previous {
				t.Fatal("unconfirmed credentials replaced", err)
			}
			if mode == "rejected" {
				if status != "failed" || !pendingNull {
					t.Fatal(status, pendingNull)
				}
				expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, ""), 200)
			} else {
				if status != "waiting_verification" || pendingNull {
					t.Fatal(status, pendingNull)
				}
				expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, ""), 409)
				expect(t, request(f.h, "POST", path+"/actions", map[string]string{"action": "terminate"}, f.customer, "password-unknown-terminate"), 409)
				resolution := "applied"
				if mode == "mismatch" {
					resolution = "not_applied"
				}
				verify := fmt.Sprintf("/api/v1/admin/services/%d/delivery-verification", f.sid)
				body := store.DeliveryVerification{JobID: job.ID, Resolution: resolution, Note: "已通过供应商控制台核对密码重置结果"}
				sensitive := body
				sensitive.Note = "核验新密码：" + password
				expect(t, request(f.h, "POST", verify, sensitive, f.root, ""), 409)
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 200)
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 200)
				credential := request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, "")
				expect(t, credential, 200)
				want := password
				if mode == "mismatch" {
					want = previous.Password
				}
				if data(t, credential)["password"] != want {
					t.Fatal("verified password mismatch")
				}
				if err = f.db.Pool.QueryRow(t.Context(), "SELECT request_credentials_encrypted IS NULL FROM provision_jobs WHERE id=$1", job.ID).Scan(&pendingNull); err != nil || !pendingNull {
					t.Fatal("pending cipher retained", err)
				}
			}
			if remote.resets.Load() != 1 || f.powerInvariant(t) != before {
				t.Fatal("repeated reset or billing changed")
			}
		})
	}
}

func TestNOKVMPasswordCancellationNetworkLocksAndModes(t *testing.T) {
	f, remote, _ := passwordFixture(t, "confirmed")
	remote.entered, remote.release = make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-remote.release:
		default:
			close(remote.release)
		}
	})
	path := fmt.Sprintf("/api/v1/services/%d", f.sid)
	body := map[string]string{"password": "fixture-cancel-123!"}
	for _, mode := range []string{"manual", "simulation"} {
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode=$2 WHERE id=$1", f.sid, mode); err != nil {
			t.Fatal(err)
		}
		expect(t, request(f.h, "POST", path+"/reset-password", body, f.customer, "password-mode-"+mode), 409)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode='nokvm',status='suspended' WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/reset-password", body, f.customer, "password-suspended"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/reset-password", body, f.customer, "password-cancel-command"), 202)
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.db.ProcessProvisionJob(ctx, *job) }()
	select {
	case <-remote.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reset not sent")
	}
	lockCtx, lockCancel := context.WithTimeout(t.Context(), time.Second)
	defer lockCancel()
	tx, err := f.db.Pool.Begin(lockCtx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(lockCtx, "SELECT id FROM services WHERE id=$1 FOR UPDATE NOWAIT", f.sid)
	tx.Rollback(lockCtx)
	if err != nil {
		t.Fatal("network holds lock", err)
	}
	expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, ""), 409)
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("duplicate dispatch")
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel stuck")
	}
	close(remote.release)
	var status string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "waiting_verification" || remote.resets.Load() != 1 {
		t.Fatal(status, remote.resets.Load(), err)
	}
}
