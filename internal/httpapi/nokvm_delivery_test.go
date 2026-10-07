package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type nokvmFixture struct {
	db                            *store.Store
	h                             http.Handler
	root, customer, other         string
	uid, pid, sid, invoice, order int64
	delivery                      provision.Delivery
}

func nokvmFixtureSetup(t *testing.T, remote http.HandlerFunc, pay bool, configure ...func(*provision.Delivery)) nokvmFixture {
	t.Helper()
	db, _ := testServer(t)
	upstream := httptest.NewServer(remote)
	t.Cleanup(upstream.Close)
	adapter, err := provision.New(provision.Profile{ID: "fixture", Name: "NOKVM 测试供应商", URL: upstream.URL, Token: "fixture-private-token"})
	if err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{PublicOrigin: "http://localhost:3088", PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", NOKVMProfiles: map[string]*provision.NOKVM{"fixture": adapter}, ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}, slog.New(slog.NewTextHandler(os.Stderr, nil))).Handler()
	_, root := user(t, db, h, "nokvm-root", "super_admin")
	uid, customer := user(t, db, h, "nokvm-customer", "customer")
	_, other := user(t, db, h, "nokvm-other", "customer")
	delivery := provision.Delivery{ProfileID: "fixture", Resources: provision.Resources{AreaID: 2, Core: 2, CPUMode: "host", Memory: 2048, Disk: 40, NetOut: 100, NetIn: 100, TemplateID: 7, IPNum: 1, Login: "root"}}
	for _, change := range configure {
		change(&delivery)
	}
	input := productInput("nokvm-product", nil)
	input.ProvisionMode = "nokvm"
	input.DeliveryConfig = &delivery
	stock := 2
	input.Stock = &stock
	pid := managedProductID(t, h, root, input)
	version, item := add(t, h, customer, pid, 1)
	w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, customer, "nokvm-purchase-order")
	expect(t, w, 201)
	order := int64(data(t, w)["order_id"].(float64))
	invoice := int64(data(t, w)["invoice_id"].(float64))
	var sid int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", order).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	if pay {
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), map[string]any{"amount_cents": 100000}, root, "nokvm-customer-topup"), 201)
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, customer, "nokvm-purchase-pay"), 201)
	}
	return nokvmFixture{db: db, h: h, root: root, customer: customer, other: other, uid: uid, pid: pid, sid: sid, invoice: invoice, order: order, delivery: delivery}
}
func (f nokvmFixture) process(t *testing.T) *store.ProvisionJob {
	t.Helper()
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	return job
}
func (f nokvmFixture) action(t *testing.T, action string) {
	t.Helper()
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", f.sid), map[string]string{"action": action}, f.root, "nokvm-lifecycle-"+action), 202)
	f.process(t)
}
func (f nokvmFixture) state(t *testing.T) (string, string, int) {
	t.Helper()
	var status, upstream string
	var stock int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT s.status,s.upstream_id,p.stock FROM services s JOIN products p ON p.id=s.product_id WHERE s.id=$1", f.sid).Scan(&status, &upstream, &stock); err != nil {
		t.Fatal(err)
	}
	return status, upstream, stock
}

func TestNOKVMCheckoutLifecycleSecretsAndSnapshots(t *testing.T) {
	var creates atomic.Int32
	var initialPassword string
	var remoteCore string
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			creates.Add(1)
			r.ParseForm()
			initialPassword = r.PostForm.Get("sys_pwd")
			remoteCore = r.PostForm.Get("core")
			fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"vm-42","public_ip":[{"ip":"192.0.2.42"}],"sys_pwd":"supplier-response-secret"}}`)
		} else if strings.Contains(r.URL.Path, "run_status") {
			fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
		} else if r.Method == "GET" && r.URL.Path == "/api/virtual/42" {
			fmt.Fprint(w, `{"code":0,"data":{"name":"synced-42","ip_list":[{"ip":"192.0.2.43"}],"sys_pwd":"supplier-response-secret"}}`)
		} else {
			fmt.Fprint(w, `{"code":0,"message":"fixture-private-token"}`)
		}
	}, true)
	if creates.Load() != 0 {
		t.Fatal("called before worker")
	}
	// The catalog can change after purchase without altering the resource plan.
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE products SET delivery_config=jsonb_set(delivery_config,'{resources,core}','8') WHERE id=$1`, f.pid); err != nil {
		t.Fatal(err)
	}
	f.process(t)
	if status, id, stock := f.state(t); status != "active" || id != "42" || stock != 1 || remoteCore != "2" {
		t.Fatal(status, id, stock, remoteCore)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE services SET delivery_snapshot='{}' WHERE id=$1`, f.sid); err == nil {
		t.Fatal("mutable delivery snapshot")
	}
	path := fmt.Sprintf("/api/v1/services/%d", f.sid)
	detail := request(f.h, "GET", path, nil, f.customer, "")
	expect(t, detail, 200)
	for _, secret := range []string{initialPassword, "fixture-private-token", "supplier-response-secret", "delivery_snapshot", "credentials_encrypted"} {
		if bytes.Contains(detail.Body.Bytes(), []byte(secret)) {
			t.Fatal("service detail leaked", secret)
		}
	}
	expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.other, ""), 404)
	credential := request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, "")
	expect(t, credential, 200)
	if data(t, credential)["password"] != initialPassword || credential.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential delivery")
	}
	var secretData string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT coalesce((SELECT string_agg(payload::text,'') FROM audit_events),'') || coalesce((SELECT string_agg(result::text,'') FROM provision_jobs),'') || encode(credentials_encrypted,'hex') FROM services WHERE id=$1`, f.sid).Scan(&secretData); err != nil || strings.Contains(secretData, initialPassword) || strings.Contains(secretData, "supplier-response-secret") || strings.Contains(secretData, "fixture-private-token") {
		t.Fatal("persisted secret leak", err)
	}
	profiles := request(f.h, "GET", "/api/v1/admin/provision-profiles", nil, f.root, "")
	expect(t, profiles, 200)
	if strings.Contains(profiles.Body.String(), "token") || strings.Contains(profiles.Body.String(), "http:") {
		t.Fatal("profile secret leak")
	}
	public := request(f.h, "GET", fmt.Sprintf("/api/v1/catalog/%d", f.pid), nil, "", "")
	expect(t, public, 200)
	if bytes.Contains(public.Body.Bytes(), []byte("delivery_config")) {
		t.Fatal("public resource plan")
	}
	f.action(t, "suspend")
	if status, _, _ := f.state(t); status != "suspended" {
		t.Fatal(status)
	}
	f.action(t, "resume")
	f.action(t, "sync")
	detail = request(f.h, "GET", path, nil, f.customer, "")
	if !bytes.Contains(detail.Body.Bytes(), []byte("192.0.2.43")) {
		t.Fatal("sync resource metadata")
	}
	// Refund schedules real deletion and releases stock only after completion.
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", f.invoice), map[string]any{}, f.root, "nokvm-refund-order"), 201)
	if status, _, stock := f.state(t); status != "provisioning" || stock != 1 {
		t.Fatal("premature inventory release", status, stock)
	}
	f.process(t)
	if status, _, stock := f.state(t); status != "terminated" || stock != 2 {
		t.Fatal(status, stock)
	}
	expect(t, request(f.h, "POST", path+"/credentials", map[string]any{}, f.customer, ""), 404)
	if creates.Load() != 1 {
		t.Fatal("duplicate create", creates.Load())
	}
}

func TestNOKVMUnknownLeaseFencingAndManualVerification(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"vm-42"}}`)
	}, true)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- f.db.ProcessProvisionJob(t.Context(), *job) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("supplier not called")
	}
	// No database row lock is retained across network I/O.
	tx, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(t.Context(), "SELECT id FROM services WHERE id=$1 FOR UPDATE NOWAIT", f.sid)
	tx.Rollback(t.Context())
	if err != nil {
		t.Fatal("network held service lock", err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("replayed dispatched job")
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", f.invoice), map[string]any{}, f.root, "nokvm-unresolved-refund"), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", f.sid), map[string]string{"action": "terminate"}, f.root, "nokvm-unknown-terminate"), 409)
	if _, err = f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET locked_at=now()-interval '6 minutes' WHERE id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ClaimProvisionJob(t.Context()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("unknown job retried", err)
	}
	close(release)
	if err = <-finished; err == nil {
		t.Fatal("late worker defeated lease fence")
	}
	verify := fmt.Sprintf("/api/v1/admin/services/%d/delivery-verification", f.sid)
	input := store.DeliveryVerification{JobID: job.ID, Resolution: "applied", UpstreamID: "42", Note: "供应商控制台核验资源 42 已开通"}
	expect(t, request(f.h, "POST", verify, input, f.customer, ""), 403)
	expect(t, request(f.h, "POST", verify, input, f.root, ""), 200)
	replayed := request(f.h, "POST", verify, input, f.root, "")
	expect(t, replayed, 200)
	if data(t, replayed)["replayed"] != true {
		t.Fatal("verification replay")
	}
	input.Resolution = "not_applied"
	expect(t, request(f.h, "POST", verify, input, f.root, ""), 409)
	if status, id, stock := f.state(t); status != "active" || id != "42" || stock != 1 || calls.Load() != 1 {
		t.Fatal(status, id, stock, calls.Load())
	}
}

func TestNOKVMCompletionRollbackAndUnknownNotApplied(t *testing.T) {
	for _, failure := range []string{"invalid_response", "local_audit"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int32
			f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if failure == "invalid_response" {
					fmt.Fprint(w, `{"message":"fixture-private-token"}`)
				} else {
					fmt.Fprint(w, `{"code":0,"data":{"id":42}}`)
				}
			}, true)
			if failure == "local_audit" {
				if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_nokvm_finish() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.nokvm.completed' THEN RAISE EXCEPTION 'fixture audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_nokvm_finish BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_nokvm_finish()`); err != nil {
					t.Fatal(err)
				}
			}
			job, err := f.db.ClaimProvisionJob(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = f.db.ProcessProvisionJob(t.Context(), *job)
			if failure == "local_audit" {
				if err == nil {
					t.Fatal("ignored audit rollback")
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
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "waiting_verification" {
				t.Fatal(status, err)
			}
			if _, err = f.db.ClaimProvisionJob(t.Context()); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal("unknown retry", err)
			}
			resolution := "not_applied"
			if failure == "local_audit" {
				resolution = "applied"
			}
			input := store.DeliveryVerification{JobID: job.ID, Resolution: resolution, UpstreamID: "42", Note: "核验供应商控制台实际资源"}
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery-verification", f.sid), input, f.root, ""), 200)
			if resolution == "not_applied" {
				if status, _, stock := f.state(t); status != "failed" || stock != 1 {
					t.Fatal(status, stock)
				}
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", f.sid), map[string]string{"action": "create"}, f.root, "nokvm-create-after-verify"), 202)
			}
			if calls.Load() != 1 {
				t.Fatal("automatic duplicate", calls.Load())
			}
		})
	}
}

func TestNOKVMFixedResourcesAndPermissionBoundaries(t *testing.T) {
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"code":0,"data":{"id":42}}`) }, false)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", f.sid), map[string]string{"action": "create"}, f.root, "nokvm-unpaid-create"), 409)
	in := store.ProductOptionGroupInput{Name: "CPU", Slug: "cpu", Active: enabled(), Options: []store.ProductOptionInput{{Label: "4 cores", Value: "4", Active: enabled()}}}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", f.pid), in, f.root, ""), 409)
	duplicate := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/duplicate", f.pid), map[string]string{"name": "NOKVM copy", "slug": "nokvm-copy"}, f.root, "")
	expect(t, duplicate, 201)
	var copied []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT delivery_config FROM products WHERE id=$1", int64(data(t, duplicate)["id"].(float64))).Scan(&copied); err != nil {
		t.Fatal(err)
	}
	var config provision.Delivery
	if json.Unmarshal(copied, &config) != nil || !reflect.DeepEqual(config, f.delivery) {
		t.Fatal("duplicate delivery")
	}
	role := request(f.h, "POST", "/api/v1/admin/roles", roleInput("NOKVM 只读", "products.view", "services.view"), f.root, "")
	expect(t, role, 201)
	roleID := int64(data(t, role)["id"].(float64))
	readerID, reader := user(t, f.db, f.h, "nokvm-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET admin_role_id=$2 WHERE id=$1", readerID, roleID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", "/api/v1/admin/provision-profiles", nil, reader, ""), 200)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/credentials", f.sid), map[string]any{}, reader, ""), 403)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery-verification", f.sid), store.DeliveryVerification{JobID: 1, Resolution: "not_applied", Note: "核验测试"}, reader, ""), 403)
	if _, err := f.db.VerifyNOKVMJob(context.Background(), readerID, f.sid, store.DeliveryVerification{JobID: 1, Resolution: "not_applied", Note: "核验测试"}); err == nil {
		t.Fatal("store permission bypass")
	}
}

func TestNOKVMBillingCycleIsLocalAndResourceUpgradesAreRejected(t *testing.T) {
	var calls atomic.Int32
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"code":0,"data":{"id":42}}`)
	}, true)
	f.process(t)
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents,active) VALUES($1,'quarterly',6000,0,true)`, f.pid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET config_upgrade_enabled=true WHERE id=$1", f.pid); err != nil {
		t.Fatal(err)
	}
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	version := int64(detail["version"].(float64))
	revision := *configRevision(t, f.h, f.root, f.pid)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-quotes", f.sid), store.ConfigurationChangeInput{ExpectedVersion: version, ExpectedRevision: revision}, f.customer, ""), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/product-quotes", f.sid), store.ProductChangeInput{ConfigurationChangeInput: store.ConfigurationChangeInput{ExpectedVersion: version, ExpectedRevision: revision}}, f.customer, ""), 409)
	quote := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/billing-cycle-quotes", f.sid), store.BillingCycleChangeInput{TargetBillingCycle: "quarterly", ExpectedVersion: version, ExpectedRevision: revision}, f.customer, "")
	expect(t, quote, 201)
	change := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-changes", f.sid), map[string]any{"quote_id": data(t, quote)["id"]}, f.customer, "nokvm-cycle-submit")
	expect(t, change, 201)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(data(t, change)["invoice_id"].(float64))), map[string]any{}, f.customer, "nokvm-cycle-pay"), 201)
	f.process(t)
	var cycle string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT billing_cycle FROM services WHERE id=$1", f.sid).Scan(&cycle); err != nil || cycle != "quarterly" || calls.Load() != 1 {
		t.Fatal("local cycle transition", cycle, calls.Load(), err)
	}
}

func TestNOKVMKnownRejectionAndMissingProviderNeverSimulate(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			var calls atomic.Int32
			f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				fmt.Fprint(w, `{"code":3,"message":"fixture-private-token"}`)
			}, true)
			if missing {
				f.db.Provisioners = nil
			}
			job := f.process(t)
			var state, detail string
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT status,last_error FROM provision_jobs WHERE id=$1", job.ID).Scan(&state, &detail); err != nil || state != "failed" || strings.Contains(detail, "fixture-private-token") {
				t.Fatal(state, detail, err)
			}
			if status, id, _ := f.state(t); status == "active" || id != "" {
				t.Fatal("supplier failure simulated resource", status, id)
			}
			if missing && calls.Load() != 0 || !missing && calls.Load() != 1 {
				t.Fatal("preflight/network boundary", calls.Load())
			}
			if _, err := f.db.ClaimProvisionJob(t.Context()); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal("failed job automatically retried", err)
			}
		})
	}
}
