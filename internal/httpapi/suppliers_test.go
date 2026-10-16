package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
)

type supplierFixture struct {
	db                                     *store.Store
	h                                      http.Handler
	root, customer, reader, other          string
	supplierID, pid, uid                   int64
	in                                     store.SupplierImportInput
	remote                                 *httptest.Server
	creates, pays                          atomic.Int64
	renews, renewalPays                    atomic.Int64
	renewalDueUnchanged                    atomic.Bool
	uncertain                              atomic.Bool
	remoteProduct                          atomic.Int64
	blocked                                chan struct{}
	entered                                chan struct{}
	once                                   sync.Once
	actions                                atomic.Int64
	consoleCalls                           atomic.Int64
	consoleEntered, consoleBlocked         chan struct{}
	consoleUnknown                         atomic.Bool
	actionUnknown                          atomic.Bool
	systemPassword, systemOS, consoleURL   atomic.Value
	syncFailure                            atomic.Bool
	invoiceOnly                            atomic.Bool
	paymentResponseIncomplete              atomic.Bool
	remoteStatus                           atomic.Value
	remoteSuspendReason, remoteSuspendType atomic.Value
	syncEntered, syncBlocked               chan struct{}
	syncOnce                               sync.Once
}

func supplierSetup(t *testing.T) *supplierFixture {
	t.Helper()
	f := &supplierFixture{}
	f.db, _ = testServer(t)
	f.remote = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zjmf_api_login" && r.Header.Get("Authorization") != "Bearer supplier-private-jwt" {
			t.Errorf("missing supplier auth")
		}
		switch r.URL.Path {
		case "/zjmf_api_login":
			if r.PostFormValue("password") != "supplier-private-password" {
				t.Errorf("wrong password")
			}
			fmt.Fprint(w, `{"status":200,"jwt":"supplier-private-jwt"}`)
		case "/cart/all":
			fmt.Fprint(w, `{"status":200,"data":{"products":[{"products":[{"id":7,"name":"Upstream VM","stock_control":1,"qty":5}]}],"currency":"CNY"}}`)
		case "/cart/get_product_config":
			fmt.Fprint(w, `{"status":200,"data":{"products":{"id":7,"name":"Upstream VM","stock_control":1,"qty":5,"location_version":1,"pay_type":"{\"pay_type\":\"recurring\"}"},"product_pricings":[{"code":"CNY","monthly":"16.00","msetupfee":"1.00","quarterly":"40.00","qsetupfee":"2.00"}],"config_groups":[],"customfields":[],"advanced":[]}}`)
		case "/user_info":
			fmt.Fprint(w, `{"status":200,"user":{"currency":1}}`)
		case "/cart/clear":
			fmt.Fprint(w, `{"status":200}`)
		case "/cart/add_to_shop":
			if r.PostFormValue("pid") != "7" || len(r.PostFormValue("downstream_token")) != 32 {
				t.Errorf("bad supplier cart identity")
			}
			fmt.Fprint(w, `{"status":200}`)
		case "/cart/settle":
			f.creates.Add(1)
			if f.invoiceOnly.Load() {
				fmt.Fprint(w, `{"status":200,"data":{"invoiceid":90}}`)
				return
			}
			if f.entered != nil {
				f.once.Do(func() { close(f.entered) })
				<-f.blocked
			}
			if f.uncertain.Load() {
				fmt.Fprint(w, `{"status":200,"data":`)
				return
			}
			fmt.Fprint(w, `{"status":200,"data":{"hostid":[42],"invoiceid":90}}`)
		case "/host/renewpage":
			fmt.Fprintf(w, `{"status":200,"data":{"host":{"productid":7,"billingcycle":"monthly","amount":"16.00","nextduedate":%d},"currency":{"code":"CNY"}}}`, f.renewalDue())
		case "/host/renew":
			if r.PostFormValue("hostid") != "42" || r.PostFormValue("billingcycles") != "monthly" {
				t.Error("bad renewal request")
			}
			f.renews.Add(1)
			fmt.Fprint(w, `{"status":200,"data":{"invoice_id":91}}`)
		case "/get_invoices_detail":
			if r.URL.Query().Get("id") == "91" {
				status, total := "Unpaid", "16.00"
				if f.renewalPays.Load() > 0 {
					status, total = "Paid", "0.00"
				}
				fmt.Fprintf(w, `{"status":200,"data":{"detail":{"id":91,"subtotal":"16.00","total":%q,"status":%q},"currency":{"code":"CNY"},"invoice_items":[{"type":"renew","rel_id":42}]}}`, total, status)
				return
			}
			status, total := "Unpaid", "17.00"
			if f.pays.Load() > 0 {
				status, total = "Paid", "0.00"
			}
			fmt.Fprintf(w, `{"status":200,"data":{"detail":{"id":90,"subtotal":"17.00","total":%q,"credit":"0.00","status":%q},"currency":{"code":"CNY"},"invoice_items":[{"type":"setup","rel_id":42,"amount":"1.00"},{"type":"host","rel_id":42,"amount":"16.00"}]}}`, total, status)
		case "/apply_credit":
			if r.PostFormValue("invoiceid") == "91" {
				if r.PostFormValue("enough") != "1" {
					t.Error("unsafe renewal payment")
				}
				f.renewalPays.Add(1)
				fmt.Fprint(w, `{"status":1001}`)
				return
			}
			f.pays.Add(1)
			if f.paymentResponseIncomplete.Load() {
				fmt.Fprint(w, `{"status":1001}`)
				return
			}
			fmt.Fprint(w, `{"status":1001,"data":{"hostid":[42]}}`)
		case "/host/header":
			if f.syncFailure.Load() {
				fmt.Fprint(w, `{"status":200,"data":{"host_data":{"id":42,"productid":7,"domainstatus":"Active","nextduedate":"not-a-date"}}}`)
				return
			}
			productID := f.remoteProduct.Load()
			if productID == 0 {
				productID = 7
			}
			password, _ := f.systemPassword.Load().(string)
			status, _ := f.remoteStatus.Load().(string)
			if status == "" {
				status = "Active"
			}
			if f.syncEntered != nil {
				f.syncOnce.Do(func() { close(f.syncEntered) })
				<-f.syncBlocked
			}
			osName, _ := f.systemOS.Load().(string)
			reason, _ := f.remoteSuspendReason.Load().(string)
			reasonType, _ := f.remoteSuspendType.Load().(string)
			json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": map[string]any{"host_data": map[string]any{"id": 42, "productid": productID, "domainstatus": status, "domain": "vm.example", "nextduedate": f.renewalDue(), "username": "root", "password": password, "os": osName, "suspendreason": reason, "suspendreason_type": reasonType}, "cloud_os": []map[string]any{{"id": 17, "name": "Debian 13", "group": "Linux"}, {"id": 18, "name": "Windows Server 2025", "group": "Windows"}}}})
		case "/provision/default", "/provision/custom/42":
			if r.Method != http.MethodPost || r.PostFormValue("id") != "42" || r.PostFormValue("is_api") != "1" {
				t.Error("invalid supplier action identity")
			}
			if r.PostFormValue("func") == "vnc" {
				f.consoleCalls.Add(1)
				if f.consoleEntered != nil {
					close(f.consoleEntered)
					<-f.consoleBlocked
				}
				if f.consoleUnknown.Load() {
					fmt.Fprint(w, `{"status":200,"data":`)
					return
				}
				u, _ := f.consoleURL.Load().(string)
				if u == "" {
					u = "https://console.example/vnc?token=private-console-token"
				}
				json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": map[string]any{"url": u, "pass": "private-console-password"}})
			} else {
				f.actions.Add(1)
				if f.actionUnknown.Load() {
					fmt.Fprint(w, `{"status":200,"data":`)
					return
				}
				if r.URL.Path == "/provision/custom/42" && r.PostFormValue("func") != "exitRescue" {
					t.Error("invalid custom function")
				}
				if r.PostFormValue("func") == "reinstall" && (r.PostFormValue("os") != "17" || r.PostFormValue("format_data_disk") != "0") {
					t.Error("invalid reinstall arguments")
				}
				fmt.Fprint(w, `{"status":200}`)
			}
		case "/cart/hostinfo":
			productID := f.remoteProduct.Load()
			if productID == 0 {
				productID = 7
			}
			fmt.Fprintf(w, `{"status":200,"data":{"hosts":[{"id":42,"productid":%d,"domainstatus":"Active","domain":"vm.example","nextduedate":1800000000}]}}`, productID)
		default:
			t.Errorf("unexpected supplier endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.remote.Close)
	f.h = New(f.db, config.Config{PublicOrigin: "http://localhost:3088", PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}, slog.New(slog.NewTextHandler(os.Stderr, nil))).Handler()
	_, f.root = user(t, f.db, f.h, "supplier-root", "super_admin")
	f.uid, f.customer = user(t, f.db, f.h, "supplier-customer", "customer")
	rid, reader := user(t, f.db, f.h, "supplier-reader", "admin")
	f.reader = reader
	contentRole(t, f.db, rid, "products.view", "services.view")
	_, f.other = user(t, f.db, f.h, "supplier-other", "customer")
	w := request(f.h, "POST", "/api/v1/admin/suppliers", store.SupplierInput{Name: "Supplier", Provider: supplier.ProviderZJMF, BaseURL: f.remote.URL, Username: "account", Password: "supplier-private-password", Active: enabled()}, f.root, "")
	expect(t, w, 201)
	f.supplierID = int64(data(t, w)["id"].(float64))
	p, err := f.db.SupplierProduct(t.Context(), f.supplierID, 7)
	if err != nil {
		t.Fatal(err)
	}
	f.in = store.SupplierImportInput{UpstreamProductID: 7, ExpectedSupplierVersion: 1, ExpectedUpstreamRevision: p.Revision, Name: "Local VM", Slug: "local-vm", BillingCycle: "monthly", PriceCents: 2500, MaximumCostCents: 1700}
	f.pid = f.importProduct(t, f.in)
	return f
}
func (f *supplierFixture) importProduct(t *testing.T, in store.SupplierImportInput) int64 {
	t.Helper()
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/suppliers/%d/import", f.supplierID), in, f.root, "")
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}
func (f *supplierFixture) publish(t *testing.T, pid int64, slug string) {
	t.Helper()
	in := productInput(slug, nil)
	in.Name = "Local VM"
	in.ProvisionMode = "supplier"
	in.ExpectedRevision = 1
	in.PriceCents = 2500
	in.SetupFeeCents = 0
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/admin/products/%d", pid), in, f.root, ""), 200)
}
func (f *supplierFixture) purchase(t *testing.T, pid int64, key string) (int64, int64) {
	t.Helper()
	v, i := add(t, f.h, f.customer, pid, 1)
	w := request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{i}}, f.customer, key)
	expect(t, w, 201)
	invoice := int64(data(t, w)["invoice_id"].(float64))
	var sid int64
	err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", int64(data(t, w)["order_id"].(float64))).Scan(&sid)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.uid), map[string]any{"amount_cents": 10000}, f.root, key+"-credit"), 201)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, key+"-pay"), 201)
	return sid, invoice
}
func (f *supplierFixture) claim(t *testing.T) *store.ProvisionJob {
	t.Helper()
	j, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func (f *supplierFixture) state(t *testing.T, j *store.ProvisionJob) (string, string, *int64) {
	t.Helper()
	var state, svc string
	var running *int64
	err := f.db.Pool.QueryRow(t.Context(), "SELECT j.status,s.status,a.running_job_id FROM provision_jobs j JOIN services s ON s.id=j.service_id JOIN supplier_accounts a ON a.id=$2 WHERE j.id=$1", j.ID, f.supplierID).Scan(&state, &svc, &running)
	if err != nil {
		t.Fatal(err)
	}
	return state, svc, running
}

func TestSupplierManagementImportCheckoutAndDelivery(t *testing.T) {
	f := supplierSetup(t)
	catalog := request(f.h, "GET", "/api/v1/catalog", nil, "", "")
	expect(t, catalog, 200)
	if len(listData(t, catalog)) != 0 {
		t.Fatal("import not inactive")
	}
	path := fmt.Sprintf("/api/v1/admin/suppliers/%d", f.supplierID)
	expect(t, request(f.h, "POST", "/api/v1/admin/suppliers", store.SupplierInput{Name: "Duplicate", Provider: supplier.ProviderZJMF, BaseURL: f.remote.URL, Username: "account", Password: "supplier-private-password", Active: enabled()}, f.root, ""), 409)
	expect(t, request(f.h, "PUT", path, store.SupplierInput{Name: "Retarget", Provider: supplier.ProviderZJMF, BaseURL: f.remote.URL, Username: "another-account", Active: enabled(), ExpectedVersion: 1}, f.root, ""), 409)
	for _, cookie := range []string{f.root, f.reader} {
		w := request(f.h, "GET", "/api/v1/admin/suppliers", nil, cookie, "")
		expect(t, w, 200)
		if strings.Contains(w.Body.String(), "supplier-private") {
			t.Fatal("credentials leaked")
		}
	}
	expect(t, request(f.h, "POST", path+"/catalog", nil, f.reader, ""), 403)
	expect(t, request(f.h, "POST", path+"/import", f.in, f.reader, ""), 403)
	expect(t, request(f.h, "GET", "/api/v1/admin/suppliers", nil, f.customer, ""), 403)
	expect(t, request(f.h, "PUT", path, store.SupplierInput{Name: "Renamed", Provider: supplier.ProviderZJMF, BaseURL: f.remote.URL, Username: "account", Active: enabled(), ExpectedVersion: 1}, f.root, ""), 200)
	expect(t, request(f.h, "PUT", path, store.SupplierInput{Name: "Stale", Provider: supplier.ProviderZJMF, BaseURL: f.remote.URL, Username: "account", Active: enabled(), ExpectedVersion: 1}, f.root, ""), 409)
	expect(t, request(f.h, "POST", path+"/catalog", nil, f.root, ""), 200)
	f.publish(t, f.pid, "local-vm")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/pricing", f.pid), map[string]any{"billing_cycle": "quarterly", "price_cents": 5000, "active": true}, f.root, ""), 409)
	sid, invoice := f.purchase(t, f.pid, "supplier-order-one")
	j := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	state, svc, running := f.state(t, j)
	if state != "succeeded" || svc != "active" || running != nil || f.creates.Load() != 1 || f.pays.Load() != 1 {
		t.Fatal(state, svc, running, f.creates.Load(), f.pays.Load())
	}
	var due int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT extract(epoch FROM next_due_at)::bigint FROM services WHERE id=$1", sid).Scan(&due); err != nil || due != 1800000000 {
		t.Fatal("upstream due date", due, err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "supplier-active-refund"), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/cancellation", sid), map[string]any{"type": "immediate", "reason": "取消上游服务"}, f.customer, "supplier-cancel"), 409)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err == nil {
		t.Fatal("claimed job replay permitted")
	}
	if f.creates.Load() != 1 {
		t.Fatal("duplicate purchase")
	}
	for _, p := range []string{"/api/v1/services", fmt.Sprintf("/api/v1/services/%d", sid), "/api/v1/orders", "/api/v1/cart"} {
		w := request(f.h, "GET", p, nil, f.customer, "")
		expect(t, w, 200)
		if strings.Contains(w.Body.String(), "supplier-private") || strings.Contains(w.Body.String(), "custom_fields_encrypted") || strings.Contains(w.Body.String(), "delivery_snapshot") {
			t.Fatal("source secrets leaked", p)
		}
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", sid), map[string]any{}, f.customer, "supplier-renew"), 201)
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/services/%d/auto-renew", sid), map[string]any{"enabled": true}, f.customer, ""), 200)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", sid), map[string]any{"action": "terminate"}, f.root, "supplier-terminate"), 409)
	_, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_product_bindings SET upstream_product_id=8 WHERE product_id=$1", f.pid)
	if err == nil {
		t.Fatal("binding mutable")
	}
	_, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET delivery_snapshot='{}' WHERE id=$1", sid)
	if err == nil {
		t.Fatal("snapshot mutable")
	}
}

func TestSupplierPeriodicSyncCanReuseCompletedJob(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	f.purchase(t, f.pid, "supplier-periodic")
	initial := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *initial); err != nil {
		t.Fatal(err)
	}
	if got, err := f.db.EnqueuePeriodicSupplierSyncs(t.Context()); err != nil || got != 1 {
		t.Fatalf("first periodic enqueue: rows=%d err=%v", got, err)
	}
	first := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET updated_at=now()-interval '6 minutes' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := f.db.EnqueuePeriodicSupplierSyncs(t.Context()); err != nil || got != 1 {
		t.Fatalf("rearm periodic enqueue: rows=%d err=%v", got, err)
	}
	second := f.claim(t)
	if second.ID != first.ID {
		t.Fatalf("periodic job was not reused: first=%d second=%d", first.ID, second.ID)
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *second); err != nil {
		t.Fatal(err)
	}
	state, service, running := f.state(t, second)
	if state != "succeeded" || service != "active" || running != nil {
		t.Fatalf("periodic reuse failed: state=%s service=%s running=%v", state, service, running)
	}
}

func TestSupplierProductMismatchAndDuplicateResourceRequireVerification(t *testing.T) {
	for _, mismatch := range []bool{true, false} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			f := supplierSetup(t)
			f.publish(t, f.pid, "local-vm")
			if mismatch {
				f.remoteProduct.Store(8)
			}
			f.purchase(t, f.pid, "supplier-first")
			first := f.claim(t)
			if err := f.db.ProcessProvisionJob(t.Context(), *first); err != nil {
				t.Fatal(err)
			}
			job := first
			if !mismatch {
				in := f.in
				in.Slug = "duplicate-host"
				pid := f.importProduct(t, in)
				f.publish(t, pid, in.Slug)
				f.purchase(t, pid, "supplier-second")
				job = f.claim(t)
				if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
					t.Fatal(err)
				}
			}
			state, _, running := f.state(t, job)
			if state != "waiting_verification" || running == nil || *running != job.ID {
				t.Fatal(state, running)
			}
		})
	}
}

func TestSupplierUnknownOutcomeAccountBlockAndReconciliation(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	sid, _ := f.purchase(t, f.pid, "supplier-unknown")
	f.uncertain.Store(true)
	j := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	state, svc, running := f.state(t, j)
	if state != "waiting_verification" || svc != "provisioning" || running == nil || *running != j.ID {
		t.Fatal(state, svc, running)
	}
	in := f.in
	in.Slug = "local-vm-two"
	pid := f.importProduct(t, in)
	f.publish(t, pid, in.Slug)
	f.purchase(t, pid, "supplier-blocked")
	second := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *second); err != nil {
		t.Fatal(err)
	}
	state, _, _ = f.state(t, second)
	if state != "pending" || f.creates.Load() != 1 {
		t.Fatal("account not blocked", state, f.creates.Load())
	}
	if err := f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	verifyPath := fmt.Sprintf("/api/v1/admin/services/%d/supplier-verification", sid)
	vin := store.SupplierVerificationInput{JobID: j.ID, ExpectedAttempts: j.Attempts, Outcome: "not_applied", Note: "核验上游账户没有资源和账单"}
	expect(t, request(f.h, "POST", verifyPath, vin, f.reader, ""), 403)
	expect(t, request(f.h, "POST", verifyPath, vin, f.root, ""), 409)
	vin.AbsenceAcknowledged = true
	expect(t, request(f.h, "POST", verifyPath, vin, f.root, ""), 200)
	state, svc, running = f.state(t, j)
	if state != "failed" || svc != "failed" || running != nil {
		t.Fatal(state, svc, running)
	}
	expect(t, request(f.h, "POST", verifyPath, vin, f.root, ""), 409)
}

func TestSupplierAccountSerializesConcurrentPurchases(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	f.purchase(t, f.pid, "supplier-concurrent-one")
	in := f.in
	in.Slug = "local-vm-concurrent"
	pid := f.importProduct(t, in)
	f.publish(t, pid, in.Slug)
	f.purchase(t, pid, "supplier-concurrent-two")
	first, second := f.claim(t), f.claim(t)
	f.blocked = make(chan struct{})
	f.entered = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- f.db.ProcessProvisionJob(context.Background(), *first) }()
	<-f.entered
	if err := f.db.ProcessProvisionJob(t.Context(), *second); err != nil {
		close(f.blocked)
		t.Fatal(err)
	}
	state, _, _ := f.state(t, second)
	if state != "pending" || f.creates.Load() != 1 {
		close(f.blocked)
		t.Fatal(state, f.creates.Load())
	}
	close(f.blocked)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSupplierCredentialsAreOwnerBound(t *testing.T) {
	f := supplierSetup(t)
	var raw []byte
	err := f.db.Pool.QueryRow(t.Context(), "SELECT credentials_encrypted FROM supplier_accounts WHERE id=$1", f.supplierID).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "supplier-private-password") {
		t.Fatal("plaintext storage")
	}
	// JSON metadata never emits the provider account password.
	out, _ := json.Marshal(supplier.Account{Password: "supplier-private-password"})
	if strings.Contains(string(out), "supplier-private-password") {
		t.Fatal("account DTO secret leak")
	}
}

func TestSupplierPeriodicSyncAfterRemoteDeletionReleasesOnce(t *testing.T) {
	for _, remoteStatus := range []string{"Deleted", "deleted"} {
		t.Run(remoteStatus, func(t *testing.T) {
			f := supplierSetup(t)
			f.publish(t, f.pid, "local-vm")
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET stock=1 WHERE id=$1", f.pid); err != nil {
				t.Fatal(err)
			}
			sid, invoice := f.purchase(t, f.pid, "remote-delete-purchase")
			if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
				t.Fatal(err)
			}
			check := func(wantState string, wantStock int, wantReleased bool) {
				t.Helper()
				var state, invoiceStatus string
				var stock int
				var released bool
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT s.status,p.stock,s.inventory_released,i.status
FROM services s JOIN products p ON p.id=s.product_id JOIN invoices i ON i.id=$2 WHERE s.id=$1`, sid, invoice).Scan(&state, &stock, &released, &invoiceStatus); err != nil {
					t.Fatal(err)
				}
				if state != wantState || stock != wantStock || released != wantReleased || invoiceStatus != "paid" {
					t.Fatal(state, stock, released, invoiceStatus)
				}
			}
			// Refund/cancellation is handled remotely; no local termination is sent.
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/actions", sid), map[string]any{"action": "terminate"}, f.root, "remote-delete-no-local-terminate"), 409)
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "remote-delete-active-refund"), 409)
			check("active", 0, false)
			// A refund still being processed leaves the actual resource active.
			if n, err := f.db.EnqueuePeriodicSupplierSyncs(t.Context()); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			first := f.claim(t)
			if err := f.db.ProcessProvisionJob(t.Context(), *first); err != nil {
				t.Fatal(err)
			}
			check("active", 0, false)
			f.remoteStatus.Store(remoteStatus)
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET updated_at=now()-interval '6 minutes' WHERE id=$1", first.ID); err != nil {
				t.Fatal(err)
			}
			if n, err := f.db.EnqueuePeriodicSupplierSyncs(t.Context()); err != nil || n != 1 {
				t.Fatal(n, err)
			}
			second := f.claim(t)
			if second.ID != first.ID {
				t.Fatal("periodic synchronization must reuse its job")
			}
			if err := f.db.ProcessProvisionJob(t.Context(), *second); err != nil {
				t.Fatal(err)
			}
			state, service, lease := f.state(t, second)
			if state != "succeeded" || service != "terminated" || lease != nil {
				t.Fatal(state, service, lease)
			}
			check("terminated", 1, true)
			id := strconv.FormatInt(sid, 10)
			values := url.Values{"id": {id}, "host_id": {"42"}, "domainstatus": {remoteStatus}, "rand_str": {"repeat-delete"}}
			values.Set("signature", supplierCallbackSign(id, supplierCallbackToken(sid), "repeat-delete"))
			expect(t, supplierCallbackRequest(f.h, "/api/host/sync", values), 200)
			check("terminated", 1, true)
			for _, staleStatus := range []string{"Active", "Pending", "Suspended", "Cancelled"} {
				values.Set("domainstatus", staleStatus)
				values.Set("suspendreason", "stale-reason")
				expect(t, supplierCallbackRequest(f.h, "/api/host/sync", values), 200)
				check("terminated", 1, true)
			}
			if n, err := f.db.EnqueuePeriodicSupplierSyncs(t.Context()); err != nil || n != 0 {
				t.Fatal(n, err)
			}
			if f.creates.Load() != 1 || f.pays.Load() != 1 {
				t.Fatal("state synchronization must not repeat upstream purchasing/payment")
			}
		})
	}
}

func (f *supplierFixture) renewalDue() int64 {
	if f.renewalPays.Load() > 0 && !f.renewalDueUnchanged.Load() {
		return 1802678400
	}
	return 1800000000
}
