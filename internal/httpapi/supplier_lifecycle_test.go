package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
)

const lifecycleSupplierProvider = "fixture_supplier_lifecycle"
const lifecycleManualSupplierProvider = "fixture_supplier_manual_lifecycle"

type lifecycleSupplierAdapter struct{ compiledSupplierAdapter }
type lifecycleManualSupplierAdapter struct{ lifecycleSupplierAdapter }

func (a lifecycleManualSupplierAdapter) Capabilities() supplier.Capabilities {
	return a.compiledSupplierAdapter.Capabilities()
}

type lifecycleSupplierControl struct {
	executes          int
	unknown           bool
	status            string
	resource, product int64
	syncEntered       chan struct{}
	syncBlocked       <-chan struct{}
}

var lifecycleSupplierState = struct {
	sync.Mutex
	accounts map[string]*lifecycleSupplierControl
}{accounts: make(map[string]*lifecycleSupplierControl)}
var lifecycleSupplierRegistration sync.Once

func lifecycleSupplierCapabilities() supplier.Capabilities {
	return supplier.Capabilities{Catalog: true, Product: true, Create: true, Sync: true, Renew: true, Suspend: true, Resume: true, Terminate: true}
}

func registerLifecycleSupplier(t *testing.T) {
	t.Helper()
	lifecycleSupplierRegistration.Do(func() {
		for _, providerID := range []string{lifecycleSupplierProvider, lifecycleManualSupplierProvider} {
			caps := lifecycleSupplierCapabilities()
			if providerID == lifecycleManualSupplierProvider {
				caps.Suspend, caps.Resume, caps.Terminate = false, false, false
			}
			err := supplier.Register(supplier.Provider{ID: providerID, Name: "Fixture lifecycle supplier", IdentityFields: []string{"realm", "account"}, Capabilities: caps}, channel.Descriptor{
				ID: providerID, Name: "Fixture lifecycle supplier", Category: channel.Provision, InstanceMode: "supplier", SchemaVersion: compiledSupplierSchema,
				Fields: []channel.Field{
					{Key: "realm", Label: "Realm", Type: channel.Text, Required: true},
					{Key: "account", Label: "Account", Type: channel.Text, Required: true},
					{Key: "timeout", Label: "Timeout", Type: channel.Number, Integer: true, Default: 30},
					{Key: "catalog_key", Label: "Read key", Type: channel.Text, Required: true, Secret: true},
					{Key: "write_key", Label: "Write key", Type: channel.Text, Required: true, Secret: true},
				},
			}, func(account supplier.Account) (supplier.Adapter, error) {
				realm, _ := account.Values["realm"].(string)
				name, _ := account.Values["account"].(string)
				a := lifecycleSupplierAdapter{compiledSupplierAdapter{realm, name, account.Secrets["catalog_key"], account.Secrets["write_key"]}}
				if providerID == lifecycleManualSupplierProvider {
					return lifecycleManualSupplierAdapter{a}, nil
				}
				return a, nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	})
}

func (a lifecycleSupplierAdapter) Capabilities() supplier.Capabilities {
	return lifecycleSupplierCapabilities()
}
func (a lifecycleSupplierAdapter) Execute(ctx context.Context, in supplier.ActionRequest) (supplier.Service, error) {
	if err := ctx.Err(); err != nil {
		return supplier.Service{}, err
	}
	lifecycleSupplierState.Lock()
	c := lifecycleSupplierState.accounts[a.key()]
	c.executes++
	unknown, status, resource, product := c.unknown, c.status, c.resource, c.product
	lifecycleSupplierState.Unlock()
	if unknown {
		return supplier.Service{}, &supplier.Error{Code: "fixture_lifecycle_timeout", Unknown: true}
	}
	compiledSupplierState.Lock()
	defer compiledSupplierState.Unlock()
	base, err := a.control(false)
	if err != nil {
		return supplier.Service{}, err
	}
	remote, ok := base.resources[in.UpstreamID]
	if !ok {
		return supplier.Service{}, &supplier.Error{Code: "fixture_resource_missing"}
	}
	switch in.Action {
	case "suspend":
		remote.Status = "Suspended"
	case "resume":
		remote.Status = "Active"
	case "terminate":
		remote.Status = "Terminated"
	default:
		return supplier.Service{}, &supplier.Error{Code: "fixture_unsupported_action"}
	}
	if status != "" {
		remote.Status = status
	}
	if resource != 0 {
		remote.UpstreamID = resource
	}
	if product != 0 {
		remote.ProductID = product
	}
	base.resources[in.UpstreamID] = remote
	return remote, nil
}
func (a lifecycleSupplierAdapter) Sync(ctx context.Context, id int64) (supplier.Service, error) {
	lifecycleSupplierState.Lock()
	c := lifecycleSupplierState.accounts[a.key()]
	entered, blocked := c.syncEntered, c.syncBlocked
	lifecycleSupplierState.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		case <-ctx.Done():
			return supplier.Service{}, ctx.Err()
		}
	}
	if blocked != nil {
		select {
		case <-blocked:
		case <-ctx.Done():
			return supplier.Service{}, ctx.Err()
		}
	}
	return a.compiledSupplierAdapter.Sync(ctx, id)
}

type supplierLifecycleFixture struct {
	*compiledSupplierFixture
	sid, upstream int64
	accountKey    string
}

func newSupplierLifecycleFixture(t *testing.T, supported bool) *supplierLifecycleFixture {
	t.Helper()
	registerLifecycleSupplier(t)
	f := &supplierLifecycleFixture{compiledSupplierFixture: newCompiledSupplierFixture(t)}
	f.accountKey = f.realm + "/lifecycle"
	compiledSupplierState.Lock()
	compiledSupplierState.accounts[f.accountKey] = &compiledSupplierControl{catalogKey: "lifecycle-catalog-private", writeKey: "lifecycle-write-private", nextResource: 70, resources: make(map[int64]supplier.Service)}
	compiledSupplierState.Unlock()
	lifecycleSupplierState.Lock()
	lifecycleSupplierState.accounts[f.accountKey] = &lifecycleSupplierControl{}
	lifecycleSupplierState.Unlock()
	t.Cleanup(func() {
		compiledSupplierState.Lock()
		delete(compiledSupplierState.accounts, f.accountKey)
		compiledSupplierState.Unlock()
		lifecycleSupplierState.Lock()
		delete(lifecycleSupplierState.accounts, f.accountKey)
		lifecycleSupplierState.Unlock()
	})
	in := f.input("lifecycle")
	in["provider"] = lifecycleManualSupplierProvider
	if supported {
		in["provider"] = lifecycleSupplierProvider
	}
	w := request(f.h, "POST", "/api/v1/admin/suppliers", in, f.root, "")
	expect(t, w, 201)
	f.supplierID = int64(data(t, w)["id"].(float64))
	f.pid = f.importAndPublish(t, f.supplierID, "lifecycle-vm")
	f.sid, _ = f.purchase(t, f.pid, "supplier-lifecycle-purchase")
	if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
		t.Fatal(err)
	}
	var upstream string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT upstream_id FROM services WHERE id=$1", f.sid).Scan(&upstream); err != nil {
		t.Fatal(err)
	}
	f.upstream, _ = strconv.ParseInt(upstream, 10, 64)
	if f.upstream < 1 {
		t.Fatal("fixture was not delivered")
	}
	return f
}
func (f *supplierLifecycleFixture) path() string {
	return fmt.Sprintf("/api/v1/admin/services/%d", f.sid)
}
func (f *supplierLifecycleFixture) prepare(t *testing.T, action string) string {
	t.Helper()
	before, remote := "active", "Active"
	if action == "resume" {
		before, remote = "suspended", "Suspended"
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status=$2 WHERE id=$1", f.sid, before); err != nil {
		t.Fatal(err)
	}
	f.remote(func(s *supplier.Service) { s.Status = remote })
	return before
}
func (f *supplierLifecycleFixture) remote(change func(*supplier.Service)) {
	compiledSupplierState.Lock()
	defer compiledSupplierState.Unlock()
	c := compiledSupplierState.accounts[f.accountKey]
	r := c.resources[f.upstream]
	change(&r)
	c.resources[f.upstream] = r
}
func (f *supplierLifecycleFixture) control(change func(*lifecycleSupplierControl)) {
	lifecycleSupplierState.Lock()
	defer lifecycleSupplierState.Unlock()
	change(lifecycleSupplierState.accounts[f.accountKey])
}
func (f *supplierLifecycleFixture) calls() int {
	lifecycleSupplierState.Lock()
	defer lifecycleSupplierState.Unlock()
	return lifecycleSupplierState.accounts[f.accountKey].executes
}
func (f *supplierLifecycleFixture) queue(t *testing.T, action, key string) *store.ProvisionJob {
	t.Helper()
	w := request(f.h, "POST", f.path()+"/actions", map[string]any{"action": action}, f.root, key)
	expect(t, w, 202)
	id := int64(data(t, w)["job_id"].(float64))
	var j store.ProvisionJob
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id,service_id,action,attempts FROM provision_jobs WHERE id=$1", id).Scan(&j.ID, &j.ServiceID, &j.Action, &j.Attempts); err != nil {
		t.Fatal(err)
	}
	return &j
}
func (f *supplierLifecycleFixture) verify(t *testing.T, j *store.ProvisionJob, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	path := f.verificationPath(t, j)
	return request(f.h, "POST", path, store.SupplierVerificationInput{JobID: j.ID, ExpectedAttempts: j.Attempts, UpstreamID: f.upstream, Outcome: "delivered", Note: "核验测试供应商资源和目标状态"}, cookie, "")
}
func (f *supplierLifecycleFixture) verificationPath(t *testing.T, j *store.ProvisionJob) string {
	t.Helper()
	var manual bool
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT coalesce(request_payload->>'manual_lifecycle'='true',false) FROM provision_jobs WHERE id=$1", j.ID).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if manual {
		return f.path() + "/supplier-lifecycle/verify"
	}
	return f.path() + "/supplier-verification"
}
func (f *supplierLifecycleFixture) check(t *testing.T, j *store.ProvisionJob, state, service string, leased bool) {
	t.Helper()
	gotState, gotService, lease := f.state(t, j)
	if gotState != state || gotService != service || (lease != nil) != leased || (leased && *lease != j.ID) {
		t.Fatalf("state=%s service=%s lease=%v; want %s/%s leased=%v", gotState, gotService, lease, state, service, leased)
	}
}
func lifecycleTarget(action string) (string, string) {
	switch action {
	case "suspend":
		return "suspended", "Suspended"
	case "resume":
		return "active", "Active"
	default:
		return "terminated", "Terminated"
	}
}

func TestSupplierLifecycleSupportedActionsConfirmTarget(t *testing.T) {
	for _, action := range []string{"suspend", "resume", "terminate"} {
		t.Run(action, func(t *testing.T) {
			f := newSupplierLifecycleFixture(t, true)
			before := f.prepare(t, action)
			key := "supplier-lifecycle-supported-" + action
			queued := f.queue(t, action, key)
			f.check(t, queued, "pending", before, false)
			j := f.claim(t)
			if j.ID != queued.ID {
				t.Fatal("claimed another job")
			}
			if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			target, _ := lifecycleTarget(action)
			f.check(t, j, "succeeded", target, false)
			expect(t, request(f.h, "POST", f.path()+"/actions", map[string]any{"action": action}, f.root, key), 200)
			if f.calls() != 1 {
				t.Fatal("lifecycle replay dispatched twice")
			}
			if err := f.db.ProcessProvisionJob(t.Context(), *j); err == nil {
				t.Fatal("completed job was processed again")
			}
			if f.calls() != 1 {
				t.Fatal("stale worker dispatched completed action")
			}
		})
	}
}

func TestSupplierLifecycleRejectsUnconfirmedTargetAndIdentity(t *testing.T) {
	for _, action := range []string{"suspend", "resume", "terminate"} {
		for _, wrong := range []string{"status", "resource", "product"} {
			t.Run(action+"/"+wrong, func(t *testing.T) {
				f := newSupplierLifecycleFixture(t, true)
				before := f.prepare(t, action)
				f.control(func(c *lifecycleSupplierControl) {
					switch wrong {
					case "status":
						c.status = "Pending"
					case "resource":
						c.resource = f.upstream + 1
					case "product":
						c.product = 74
					}
				})
				f.queue(t, action, "supplier-lifecycle-bad-"+action+"-"+wrong)
				j := f.claim(t)
				if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
					t.Fatal(err)
				}
				f.check(t, j, "waiting_verification", before, true)
				expect(t, f.verify(t, j, f.root), 409)
				f.check(t, j, "waiting_verification", before, true)
				if f.calls() != 1 {
					t.Fatal("verification resent lifecycle command")
				}
			})
		}
	}
}

func TestSupplierLifecycleUnknownRetainsLeaseAndVerifiesWithoutResend(t *testing.T) {
	for _, action := range []string{"suspend", "resume", "terminate"} {
		t.Run(action, func(t *testing.T) {
			f := newSupplierLifecycleFixture(t, true)
			before := f.prepare(t, action)
			f.control(func(c *lifecycleSupplierControl) { c.unknown = true })
			f.queue(t, action, "supplier-lifecycle-unknown-"+action)
			j := f.claim(t)
			if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
				t.Fatal(err)
			}
			f.check(t, j, "waiting_verification", before, true)
			expect(t, request(f.h, "POST", f.path()+"/supplier-lifecycle/verify", store.SupplierVerificationInput{JobID: j.ID, ExpectedAttempts: j.Attempts, UpstreamID: f.upstream, Outcome: "delivered", Note: "未明结果必须保留租约核验"}, f.root, ""), 409)
			if err := f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
				t.Fatal(err)
			}
			if next, err := f.db.ClaimProvisionJob(t.Context()); err == nil {
				t.Fatalf("unknown write reclaimed: %+v", next)
			}
			expect(t, f.verify(t, j, f.root), 409)
			target, remote := lifecycleTarget(action)
			f.remote(func(s *supplier.Service) { s.Status = remote })
			expect(t, f.verify(t, j, f.root), 200)
			f.check(t, j, "succeeded", target, false)
			expect(t, f.verify(t, j, f.root), 404)
			if f.calls() != 1 {
				t.Fatal("verification or recovery resent unknown action")
			}
		})
	}
}

func TestSupplierLifecycleManualActionsRequireTrustedSync(t *testing.T) {
	for _, action := range []string{"suspend", "resume", "terminate"} {
		t.Run(action, func(t *testing.T) {
			f := newSupplierLifecycleFixture(t, false)
			before := f.prepare(t, action)
			key := "supplier-lifecycle-manual-" + action
			j := f.queue(t, action, key)
			f.check(t, j, "waiting_manual", before, false)
			var manual, dispatched bool
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT request_payload->>'manual_lifecycle'='true',external_started_at IS NOT NULL FROM provision_jobs WHERE id=$1", j.ID).Scan(&manual, &dispatched); err != nil {
				t.Fatal(err)
			}
			if j.Attempts != 0 || !manual || dispatched {
				t.Fatal("manual fallback was dispatched or lacks durable identity")
			}
			detail := request(f.h, "GET", f.path(), nil, f.root, "")
			expect(t, detail, 200)
			found := false
			for _, item := range data(t, detail)["jobs"].([]any) {
				row := item.(map[string]any)
				if int64(row["id"].(float64)) == j.ID {
					found = row["manual_lifecycle"] == true
				}
			}
			if !found || strings.Contains(detail.Body.String(), "lifecycle-write-private") || strings.Contains(detail.Body.String(), "lifecycle-catalog-private") {
				t.Fatal("manual task projection missing or leaked supplier credential")
			}
			if next, err := f.db.ClaimProvisionJob(t.Context()); err == nil {
				t.Fatalf("manual task claimed: %+v", next)
			}
			expect(t, request(f.h, "POST", f.path()+"/actions", map[string]any{"action": action}, f.root, key), 200)
			expect(t, f.verify(t, j, f.root), 409)
			f.check(t, j, "waiting_manual", before, false)
			target, remote := lifecycleTarget(action)
			f.remote(func(s *supplier.Service) { s.Status = remote })
			expect(t, f.verify(t, j, f.root), 200)
			f.check(t, j, "succeeded", target, false)
			for _, view := range []struct{ path, cookie string }{{f.path(), f.root}, {fmt.Sprintf("/api/v1/services/%d", f.sid), f.customer}} {
				response := request(f.h, "GET", view.path, nil, view.cookie, "")
				expect(t, response, 200)
				for _, secret := range []string{"manual_lifecycle_verification", "核验测试供应商资源和目标状态", tokenHash(strings.SplitN(f.root, "=", 2)[1])} {
					if strings.Contains(response.Body.String(), secret) {
						t.Fatal("service detail exposed private operator receipt")
					}
				}
			}
			expect(t, f.verify(t, j, f.root), 200)
			if f.calls() != 0 {
				t.Fatal("manual fallback sent an upstream write")
			}
		})
	}
}

func TestSupplierLifecycleNotAppliedRequiresAcknowledgement(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprintf("supported=%v", supported), func(t *testing.T) {
			f := newSupplierLifecycleFixture(t, supported)
			f.control(func(c *lifecycleSupplierControl) { c.unknown = true })
			j := f.queue(t, "terminate", "supplier-lifecycle-not-applied")
			if supported {
				j = f.claim(t)
				if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
					t.Fatal(err)
				}
			}
			input := store.SupplierVerificationInput{JobID: j.ID, ExpectedAttempts: j.Attempts, Outcome: "not_applied", Note: "核验上游资源仍正常且终止未执行"}
			path := f.verificationPath(t, j)
			expect(t, request(f.h, "POST", path, input, f.root, ""), 409)
			input.AbsenceAcknowledged = true
			expect(t, request(f.h, "POST", path, input, f.root, ""), 200)
			f.check(t, j, "failed", "active", false)
			repeatStatus := 200
			if supported {
				repeatStatus = 409
			}
			expect(t, request(f.h, "POST", path, input, f.root, ""), repeatStatus)
			want := 0
			if supported {
				want = 1
			}
			if f.calls() != want {
				t.Fatal("not-applied verification resent command")
			}
		})
	}
}

func TestSupplierLifecycleLegacyPendingUnsupportedBecomesManual(t *testing.T) {
	f := newSupplierLifecycleFixture(t, false)
	var id int64
	// This is an actual historical pending action, rather than rewriting the
	// immutable action identity of an unrelated create/sync job.
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key) VALUES($1,'terminate','supplier-lifecycle-legacy-terminate') RETURNING id`, f.sid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	j := f.claim(t)
	if j.ID != id {
		t.Fatal("claimed unrelated legacy job")
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	f.check(t, j, "waiting_manual", "active", false)
	if j.Attempts != 1 {
		t.Fatalf("legacy claim attempts=%d", j.Attempts)
	}
	var dispatched bool
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT external_started_at IS NOT NULL FROM provision_jobs WHERE id=$1", id).Scan(&dispatched); err != nil || dispatched {
		t.Fatalf("legacy manual task dispatched=%v err=%v", dispatched, err)
	}
	f.remote(func(s *supplier.Service) { s.Status = "Terminated" })
	expect(t, f.verify(t, j, f.root), 200)
	f.check(t, j, "succeeded", "terminated", false)
	expect(t, f.verify(t, j, f.root), 200)
	if f.calls() != 0 {
		t.Fatal("legacy unsupported task sent a write")
	}
}

func TestSupplierLifecyclePermissionAndAuditRollback(t *testing.T) {
	f := newSupplierLifecycleFixture(t, false)
	for _, action := range []string{"suspend", "resume", "terminate"} {
		f.prepare(t, action)
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/actions", f.sid), map[string]any{"action": action}, f.customer, "supplier-lifecycle-customer-"+action), 403)
		expect(t, request(f.h, "POST", f.path()+"/actions", map[string]any{"action": action}, f.reader, "supplier-lifecycle-reader-"+action), 403)
	}
	f.prepare(t, "terminate")
	j := f.queue(t, "terminate", "supplier-lifecycle-audit-rollback")
	f.remote(func(s *supplier.Service) { s.Status = "Terminated" })
	expect(t, f.verify(t, j, ""), 401)
	for _, cookie := range []string{f.customer, f.reader} {
		expect(t, f.verify(t, j, cookie), 403)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_supplier_lifecycle_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.entity_type='service' AND NEW.action LIKE 'supplier.%verified%' THEN RAISE EXCEPTION 'injected supplier lifecycle audit failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_supplier_lifecycle_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_supplier_lifecycle_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, f.verify(t, j, f.root), 500)
	f.check(t, j, "waiting_manual", "active", false)
	if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_supplier_lifecycle_audit ON audit_events"); err != nil {
		t.Fatal(err)
	}
	expect(t, f.verify(t, j, f.root), 200)
	f.check(t, j, "succeeded", "terminated", false)
	if f.calls() != 0 {
		t.Fatal("unauthorized/rollback verification sent write")
	}
}

func TestSupplierLifecycleVerificationRechecksPermissionAfterSync(t *testing.T) {
	f := newSupplierLifecycleFixture(t, false)
	uid, writer := user(t, f.db, f.h, "supplier-lifecycle-revoked", "admin")
	contentRole(t, f.db, uid, "services.view", "services.manage")
	j := f.queue(t, "terminate", "supplier-lifecycle-revoked-command")
	f.remote(func(s *supplier.Service) { s.Status = "Terminated" })
	entered, blocked := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(blocked) }) }
	t.Cleanup(release)
	f.control(func(c *lifecycleSupplierControl) { c.syncEntered, c.syncBlocked = entered, blocked })
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.verify(t, j, writer) }()
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("verification did not reach read-only sync")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", uid); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case w := <-done:
		expect(t, w, 403)
	case <-time.After(8 * time.Second):
		t.Fatal("revoked verification did not finish")
	}
	f.check(t, j, "waiting_manual", "active", false)
	if f.calls() != 0 {
		t.Fatal("revoked verification resent command")
	}
}

func TestSupplierLifecycleConcurrentVerificationAppliesOnce(t *testing.T) {
	f := newSupplierLifecycleFixture(t, false)
	j := f.queue(t, "terminate", "supplier-lifecycle-concurrent-command")
	f.remote(func(s *supplier.Service) { s.Status = "Terminated" })
	start := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { <-start; done <- f.verify(t, j, f.root) }()
	}
	close(start)
	counts := make(map[int]int)
	for range 2 {
		select {
		case w := <-done:
			counts[w.Code]++
			if w.Code != 200 && w.Code != 409 {
				t.Fatalf("unexpected concurrent verification: %d %s", w.Code, w.Body.String())
			}
		case <-time.After(8 * time.Second):
			t.Fatal("concurrent verification stalled")
		}
	}
	if counts[200] != 2 || counts[409] != 0 {
		t.Fatalf("verification completion counts=%v", counts)
	}
	f.check(t, j, "succeeded", "terminated", false)
	var audits int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE entity_type='service' AND entity_id=$1 AND action LIKE 'supplier.%verified%'", strconv.FormatInt(f.sid, 10)).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || f.calls() != 0 {
		t.Fatalf("duplicate verification audit=%d writes=%d", audits, f.calls())
	}
	var leak bool
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM audit_events WHERE payload::text LIKE '%lifecycle-%private%') OR EXISTS(SELECT 1 FROM provision_jobs WHERE request_payload::text LIKE '%lifecycle-%private%' OR result::text LIKE '%lifecycle-%private%')").Scan(&leak); err != nil {
		t.Fatal(err)
	}
	if leak {
		t.Fatal("lifecycle audit/result leaked supplier credentials")
	}
	var hashLeaked bool
	hash := tokenHash(strings.SplitN(f.root, "=", 2)[1])
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM audit_events WHERE strpos(payload::text,$1)>0) OR EXISTS(SELECT 1 FROM provision_jobs WHERE strpos(result::text,$1)>0 OR strpos(request_payload::text,$1)>0)`, hash).Scan(&hashLeaked); err != nil || hashLeaked {
		t.Fatalf("session credential leaked=%v err=%v", hashLeaked, err)
	}
}

func TestSupplierLifecycleVerificationRechecksSessionAfterSync(t *testing.T) {
	for _, supported := range []bool{false, true} {
		for _, invalidate := range []string{"logout", "expiry"} {
			t.Run(fmt.Sprintf("supported=%v/%s", supported, invalidate), func(t *testing.T) {
				f := newSupplierLifecycleFixture(t, supported)
				uid, writer := user(t, f.db, f.h, "supplier-lifecycle-session", "admin")
				contentRole(t, f.db, uid, "services.view", "services.manage")
				f.control(func(c *lifecycleSupplierControl) { c.unknown = true })
				j := f.queue(t, "terminate", "supplier-lifecycle-session-command")
				if supported {
					j = f.claim(t)
					if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
						t.Fatal(err)
					}
				}
				f.remote(func(s *supplier.Service) { s.Status = "Terminated" })
				entered, blocked := make(chan struct{}, 1), make(chan struct{})
				var once sync.Once
				release := func() { once.Do(func() { close(blocked) }) }
				t.Cleanup(release)
				f.control(func(c *lifecycleSupplierControl) { c.syncEntered, c.syncBlocked = entered, blocked })
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- f.verify(t, j, writer) }()
				select {
				case <-entered:
				case <-time.After(8 * time.Second):
					t.Fatal("verification did not reach upstream read")
				}
				if invalidate == "logout" {
					expect(t, request(f.h, "POST", "/api/v1/auth/logout", map[string]any{}, writer, ""), 200)
				} else if _, err := f.db.Pool.Exec(t.Context(), "UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id=$1", uid); err != nil {
					t.Fatal(err)
				}
				release()
				select {
				case w := <-done:
					expect(t, w, 401)
				case <-time.After(8 * time.Second):
					t.Fatal("invalidated session verification stalled")
				}
				state := "waiting_manual"
				if supported {
					state = "waiting_verification"
				}
				f.check(t, j, state, "active", supported)
				want := 0
				if supported {
					want = 1
				}
				if f.calls() != want {
					t.Fatal("invalid session verification resent upstream write")
				}
			})
		}
	}
}

func TestSupplierLifecycleRejectsForgedSystemActionKeys(t *testing.T) {
	f := newSupplierLifecycleFixture(t, true)
	for _, key := range []string{
		fmt.Sprintf("overdue-suspend-service-%d-invoice-123", f.sid),
		fmt.Sprintf("paid-resume-service-%d-suspension-123", f.sid),
	} {
		expect(t, request(f.h, "POST", f.path()+"/actions", map[string]any{"action": "suspend"}, f.root, key), 409)
	}
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='suspend'", f.sid).Scan(&count); err != nil || count != 0 {
		t.Fatalf("forged action jobs=%d err=%v", count, err)
	}
	if f.calls() != 0 {
		t.Fatal("forged system action sent an upstream write")
	}
}

func TestSupplierLifecycleFailedResourceTerminationReleasesInventoryOnce(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprintf("supported=%v", supported), func(t *testing.T) {
			f := newSupplierLifecycleFixture(t, supported)
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status='failed',inventory_released=false WHERE id=$1", f.sid); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET stock=4 WHERE id=$1", f.pid); err != nil {
				t.Fatal(err)
			}
			j := f.queue(t, "terminate", "supplier-lifecycle-failed-termination")
			if supported {
				j = f.claim(t)
				if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
					t.Fatal(err)
				}
			} else {
				f.remote(func(s *supplier.Service) { s.Status = "Terminated" })
				expect(t, f.verify(t, j, f.root), 200)
				expect(t, f.verify(t, j, f.root), 200)
			}
			f.check(t, j, "succeeded", "terminated", false)
			var released bool
			var stock int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT inventory_released,(SELECT stock FROM products WHERE id=$2) FROM services WHERE id=$1", f.sid, f.pid).Scan(&released, &stock); err != nil || !released || stock != 5 {
				t.Fatalf("released=%v stock=%d err=%v", released, stock, err)
			}
			_ = f.db.ProcessProvisionJob(t.Context(), *j)
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT stock FROM products WHERE id=$1", f.pid).Scan(&stock); err != nil || stock != 5 {
				t.Fatalf("duplicate inventory release: stock=%d err=%v", stock, err)
			}
		})
	}
}
