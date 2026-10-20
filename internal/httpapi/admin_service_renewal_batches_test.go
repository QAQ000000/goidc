package httpapi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

const renewalBatchPath = "/api/v1/admin/service-renewal-batches"
const renewalBatchQuotePath = "/api/v1/admin/service-renewal-batch-quotes"

type renewalBatchFixture struct {
	adminRenewalFixture
	services []int64
}

func newRenewalBatchFixture(t *testing.T, prices ...int64) renewalBatchFixture {
	t.Helper()
	f := renewalBatchFixture{adminRenewalFixture: newAdminRenewalFixture(t, false)}
	f.services = append(f.services, f.sid)
	for _, price := range prices {
		f.services = append(f.services, addRenewalBatchService(t, f.adminRenewalFixture, price, len(f.services)))
	}
	return f
}

// Additional services go through the actual checkout, balance settlement and
// provision worker in the same customer/schema; no ownership or job rewriting.
func addRenewalBatchService(t *testing.T, f adminRenewalFixture, price int64, sequence int) int64 {
	t.Helper()
	in := productInput(fmt.Sprintf("renewal-batch-local-%d", sequence), nil)
	in.PriceCents, in.SetupFeeCents, in.ProvisionMode = price, 0, "simulation"
	pid := managedProductID(t, f.h, f.root, in)
	v, item := add(t, f.h, f.customer, pid, 1)
	w := request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, f.customer, fmt.Sprintf("renewal-batch-purchase-%d", sequence))
	expect(t, w, 201)
	order := data(t, w)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(order["invoice_id"].(float64))), map[string]any{}, f.customer, fmt.Sprintf("renewal-batch-purchase-pay-%d", sequence)), 201)
	var sid int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", int64(order["order_id"].(float64))).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil || job == nil || job.ServiceID != sid {
		t.Fatal("missing actual additional-service provision job", job, err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=date_trunc('second',now())+interval '15 days' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	return sid
}

type renewalBatchFacts struct {
	balance, invoices, items, ledger, effects, jobs, notifications, audits int64
	services                                                               string
}

func (f renewalBatchFixture) facts(t *testing.T) renewalBatchFacts {
	t.Helper()
	var out renewalBatchFacts
	err := f.db.Pool.QueryRow(t.Context(), `SELECT balance_cents,
 (SELECT count(*) FROM invoices WHERE user_id=$1),
 (SELECT count(*) FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE i.user_id=$1),
 (SELECT count(*) FROM balance_ledger WHERE user_id=$1),
 (SELECT count(*) FROM service_renewal_effects e JOIN services s ON s.id=e.service_id WHERE s.user_id=$1),
 (SELECT count(*) FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE s.user_id=$1),
 (SELECT count(*) FROM notification_events WHERE user_id=$1),
 (SELECT count(*) FROM audit_events WHERE action IN ('service.renewal.created','invoice.paid')),
 (SELECT coalesce(jsonb_agg(jsonb_build_array(id,status,version,next_due_at,recurring_price_cents,renewal_discounts_used) ORDER BY id),'[]'::jsonb)::text FROM services WHERE user_id=$1)
 FROM users WHERE id=$1`, f.uid).Scan(&out.balance, &out.invoices, &out.items, &out.ledger, &out.effects, &out.jobs, &out.notifications, &out.audits, &out.services)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f renewalBatchFixture) unchanged(t *testing.T, before renewalBatchFacts) {
	t.Helper()
	if after := f.facts(t); after != before {
		t.Fatalf("batch metadata/read/rejected call changed business facts: before=%+v after=%+v", before, after)
	}
}

func (f renewalBatchFixture) versions(t *testing.T, ids []int64) []map[string]any {
	t.Helper()
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		var version int64
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT version FROM services WHERE id=$1", id).Scan(&version); err != nil {
			t.Fatal(err)
		}
		items = append(items, map[string]any{"service_id": id, "expected_version": version})
	}
	return items
}

func renewalBatchItems(t *testing.T, value map[string]any) []map[string]any {
	t.Helper()
	raw, ok := value["items"].([]any)
	if !ok {
		t.Fatal("batch omitted item list", value)
	}
	items := make([]map[string]any, len(raw))
	for i := range raw {
		items[i], ok = raw[i].(map[string]any)
		if !ok {
			t.Fatal("batch item is not an object", raw[i])
		}
	}
	return items
}

func (f renewalBatchFixture) createBody(t *testing.T, ids []int64) map[string]any {
	t.Helper()
	w := request(f.h, "POST", renewalBatchQuotePath, map[string]any{"customer_id": f.uid, "items": f.versions(t, ids)}, f.staff, "")
	expect(t, w, 200)
	quoted := renewalBatchItems(t, data(t, w))
	items := make([]map[string]any, 0, len(quoted))
	for i, item := range quoted {
		q, ok := item["quote"].(map[string]any)
		if item["status"] != "quoted" || !ok || item["service_id"] != float64(ids[i]) {
			t.Fatal("batch quote did not preserve successful manifest order", item)
		}
		items = append(items, map[string]any{"service_id": ids[i], "expected_version": q["service_version"], "quote_hash": q["quote_hash"]})
	}
	return map[string]any{"customer_id": f.uid, "action": "create", "reason": "客户通过工单确认批量续费", "items": items}
}

func (f renewalBatchFixture) payBody(t *testing.T, ids []int64) map[string]any {
	t.Helper()
	items := make([]map[string]any, 0, len(ids))
	for i, id := range ids {
		single := f.adminRenewalFixture
		single.sid = id
		invoice := single.createRenewal(t, fmt.Sprintf("renewal-batch-original-invoice-%d", i))
		items = append(items, map[string]any{"service_id": id, "invoice_id": invoice["invoice_id"], "expected_version": invoice["version"], "amount_cents": invoice["amount_cents"]})
	}
	return map[string]any{"customer_id": f.uid, "action": "pay_balance", "reason": "客户确认使用余额批量付款", "items": items}
}

func (f renewalBatchFixture) register(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	w := request(f.h, "POST", renewalBatchPath, body, f.staff, key)
	expect(t, w, 201)
	out := data(t, w)
	id, ok := out["batch_id"].(string)
	if !ok || len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		t.Fatal("batch did not return its durable opaque identity", out)
	}
	return out
}

func renewalBatchDetailPath(batch map[string]any) string {
	return renewalBatchPath + "/" + batch["batch_id"].(string)
}

func renewalBatchHTTPWithPool(source *store.Store, pool *pgxpool.Pool) http.Handler {
	// A Store owns mutexes and journal state; a test pool needs a fresh Store,
	// not a value copy of the fixture's live storage manager.
	db := &store.Store{
		Pool: pool, SupplierDownstreamURL: source.SupplierDownstreamURL,
		ProvisionEncryptionKey:     append([]byte(nil), source.ProvisionEncryptionKey...),
		PurchaseFieldEncryptionKey: append([]byte(nil), source.PurchaseFieldEncryptionKey...),
		Provisioners:               source.Provisioners,
	}
	cfg := config.Config{PublicOrigin: "http://localhost:3088", PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", ProvisionEncryptionKey: db.ProvisionEncryptionKey}
	h := New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A regression that pins the only pool connection must release its
		// waiter on timeout, so test cleanup cannot hang closing that pool.
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (f renewalBatchFixture) execute(t *testing.T, batch map[string]any, ids []int64, key string) map[string]any {
	t.Helper()
	w := request(f.h, "POST", renewalBatchDetailPath(batch)+"/execute", map[string]any{"service_ids": ids}, f.staff, key)
	expect(t, w, 200)
	return data(t, w)
}

func assertRenewalBatchCounts(t *testing.T, out map[string]any, succeeded, failed, unresolved int) {
	t.Helper()
	if out["succeeded"] != float64(succeeded) || out["failed"] != float64(failed) || out["pending"].(float64)+out["unconfirmed"].(float64) != float64(unresolved) {
		t.Fatal("wrong durable batch progress", out)
	}
}

func TestAdminServiceRenewalBatchQuoteValidationPermissionsAndScope(t *testing.T) {
	f := newRenewalBatchFixture(t, 1000)
	body := map[string]any{"customer_id": f.uid, "items": f.versions(t, f.services)}
	before := f.facts(t)
	var receipts int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", renewalBatchQuotePath, body, f.staff, "")
	expect(t, w, 200)
	items := renewalBatchItems(t, data(t, w))
	if len(items) != 2 || items[0]["status"] != "quoted" || items[1]["status"] != "quoted" {
		t.Fatal("same-customer batch quote missing items", w.Body.String())
	}
	f.unchanged(t, before)
	var afterReceipts int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations").Scan(&afterReceipts); err != nil || afterReceipts != receipts {
		t.Fatal("read-only batch quotation wrote a receipt", receipts, afterReceipts, err)
	}
	for _, cookie := range []string{"", f.customer, f.other} {
		expect(t, request(f.h, "POST", renewalBatchQuotePath, body, cookie, ""), 401)
	}
	for _, invalid := range []map[string]any{
		{}, {"customer_id": f.uid, "items": []any{}},
		{"customer_id": f.uid, "items": []any{body["items"].([]map[string]any)[0], body["items"].([]map[string]any)[0]}},
		{"customer_id": f.uid, "items": body["items"], "amount_cents": 0},
		{"customer_id": f.uid, "items": []map[string]any{{"service_id": f.sid}}},
	} {
		expect(t, request(f.h, "POST", renewalBatchQuotePath, invalid, f.staff, ""), 400)
	}
	tooMany := make([]map[string]any, 21)
	for i := range tooMany {
		tooMany[i] = map[string]any{"service_id": i + 1, "expected_version": 1}
	}
	expect(t, request(f.h, "POST", renewalBatchQuotePath, map[string]any{"customer_id": f.uid, "items": tooMany}, f.staff, ""), 400)
	contentRole(t, f.db, f.actor, "services.view", "invoices.view", "services.manage")
	expect(t, request(f.h, "POST", renewalBatchQuotePath, body, f.staff, ""), 403)
	contentRole(t, f.db, f.actor, "services.view", "invoices.view", "services.renew", "services.renew.pay_balance")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", renewalBatchQuotePath, body, f.staff, ""), 404)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", f.uid, f.actor); err != nil {
		t.Fatal(err)
	}
	// A stale service produces an item failure without hiding the good quote.
	stale := f.versions(t, f.services)
	stale[1]["expected_version"] = int64(stale[1]["expected_version"].(int64)) + 1
	w = request(f.h, "POST", renewalBatchQuotePath, map[string]any{"customer_id": f.uid, "items": stale}, f.staff, "")
	expect(t, w, 200)
	items = renewalBatchItems(t, data(t, w))
	if items[0]["status"] != "quoted" || items[1]["status"] != "failed" {
		t.Fatal("per-item quotation failure swallowed the remaining quote", w.Body.String())
	}
	f.unchanged(t, before)
}

func TestAdminServiceRenewalBatchRegistrationManifestAndIdempotency(t *testing.T) {
	f := newRenewalBatchFixture(t, 1000)
	body := f.createBody(t, []int64{f.services[1], f.services[0]})
	before := f.facts(t)
	batch := f.register(t, body, "renewal-batch-manifest")
	f.unchanged(t, before)
	assertRenewalBatchCounts(t, batch, 0, 0, 2)
	w := request(f.h, "POST", renewalBatchPath, body, f.staff, "renewal-batch-manifest")
	expect(t, w, 200)
	if data(t, w)["batch_id"] != batch["batch_id"] {
		t.Fatal("registration retry created another batch", w.Body.String())
	}
	changed := map[string]any{"customer_id": f.uid, "action": "create", "reason": "不能覆盖原批次原因", "items": body["items"]}
	expect(t, request(f.h, "POST", renewalBatchPath, changed, f.staff, "renewal-batch-manifest"), 409)
	expect(t, request(f.h, "POST", renewalBatchPath, body, f.staff, ""), 400)
	get := request(f.h, "GET", renewalBatchDetailPath(batch), nil, f.staff, "")
	expect(t, get, 200)
	items := renewalBatchItems(t, data(t, get))
	if items[0]["service_id"] != float64(f.services[1]) || items[1]["service_id"] != float64(f.services[0]) || data(t, get)["action"] != "create" {
		t.Fatal("registered manifest order or action was rewritten", get.Body.String())
	}
	_, otherAdmin := user(t, f.db, f.h, "renewal-batch-other-admin", "admin")
	for _, cookie := range []string{f.root, otherAdmin} {
		expect(t, request(f.h, "GET", renewalBatchDetailPath(batch), nil, cookie, ""), 404)
		expect(t, request(f.h, "POST", renewalBatchDetailPath(batch)+"/execute", map[string]any{"service_ids": f.services}, cookie, "renewal-batch-other-actor"), 404)
	}
	expect(t, request(f.h, "GET", renewalBatchDetailPath(batch), nil, f.customer, ""), 401)
	var initialReceipts int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations").Scan(&initialReceipts); err != nil {
		t.Fatal(err)
	}
	firstItem := body["items"].([]map[string]any)[0]
	for _, invalid := range []map[string]any{
		{"customer_id": f.uid, "action": "unknown", "reason": "无效动作", "items": body["items"]},
		{"customer_id": f.uid, "action": "create", "items": body["items"]},
		{"customer_id": f.uid, "action": "create", "reason": "重复服务", "items": []map[string]any{firstItem, firstItem}},
		{"customer_id": f.uid, "action": "create", "reason": "不能混用建单和付款", "items": []map[string]any{{"service_id": f.sid, "expected_version": f.version(t), "quote_hash": strings.Repeat("a", 64), "amount_cents": 0}}},
		{"customer_id": f.uid, "action": "pay_balance", "reason": "不能省略明确付款金额", "items": []map[string]any{{"service_id": f.sid, "expected_version": 1, "invoice_id": 1}}},
		{"customer_id": f.uid, "action": "create", "reason": "未知请求字段", "items": body["items"], "payment_source": "manual"},
	} {
		expect(t, request(f.h, "POST", renewalBatchPath, invalid, f.staff, "renewal-batch-invalid-register"), 400)
	}
	for _, ids := range [][]int64{{}, {f.sid, f.sid}, {f.sid + 999999}} {
		expect(t, request(f.h, "POST", renewalBatchDetailPath(batch)+"/execute", map[string]any{"service_ids": ids}, f.staff, "renewal-batch-invalid-execute"), 400)
	}
	var rejectedReceipts int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations").Scan(&rejectedReceipts); err != nil || rejectedReceipts != initialReceipts {
		t.Fatal("invalid registration/execution wrote durable mutation rows", initialReceipts, rejectedReceipts, err)
	}
	f.unchanged(t, before)
	// A real service owned by another customer cannot be smuggled into a batch.
	otherID, otherCookie := user(t, f.db, f.h, "renewal-batch-outsider", "customer")
	otherProduct := productInput("renewal-batch-outsider-product", nil)
	otherPID := managedProductID(t, f.h, f.root, otherProduct)
	otherSID := downloadService(t, f.db, f.h, otherID, otherCookie, otherPID, "renewal-batch-outsider-order")
	cross := f.versions(t, []int64{f.sid, otherSID})
	before = f.facts(t)
	w = request(f.h, "POST", renewalBatchQuotePath, map[string]any{"customer_id": f.uid, "items": cross}, f.staff, "")
	if w.Code != 404 && w.Code != 409 {
		t.Fatal("mixed-customer quote was not rejected as a whole", w.Code, w.Body.String())
	}
	first := body["items"].([]map[string]any)[0]
	crossCreate := []map[string]any{first, {"service_id": otherSID, "expected_version": 1, "quote_hash": strings.Repeat("a", 64)}}
	w = request(f.h, "POST", renewalBatchPath, map[string]any{"customer_id": f.uid, "action": "create", "reason": "混合客户必须整批拒绝", "items": crossCreate}, f.staff, "renewal-batch-cross-customer")
	if w.Code != 404 && w.Code != 409 {
		t.Fatal("mixed-customer registration was not rejected as a whole", w.Code, w.Body.String())
	}
	f.unchanged(t, before)
}

func TestAdminServiceRenewalBatchPartialBalanceStableAttempts(t *testing.T) {
	f := newRenewalBatchFixture(t, 4000, 1000)
	// Manifest order differs from the execute subset and numerical IDs. The
	// first service succeeds, second is short of funds, third must still run.
	ids := []int64{f.services[0], f.services[1], f.services[2]}
	body := f.payBody(t, ids)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=5000 WHERE id=$1", f.uid); err != nil {
		t.Fatal(err)
	}
	before := f.facts(t)
	batch := f.register(t, body, "renewal-batch-partial-register")
	f.unchanged(t, before)
	out := f.execute(t, batch, []int64{ids[2], ids[1], ids[0]}, "renewal-batch-partial-first")
	assertRenewalBatchCounts(t, out, 2, 1, 0)
	items := renewalBatchItems(t, out)
	if items[0]["status"] != "succeeded" || items[1]["status"] != "failed" || items[2]["status"] != "succeeded" {
		t.Fatal("partial balance did not obey manifest order and continue", out)
	}
	after := f.facts(t)
	if after.balance != 1500 || after.ledger != before.ledger+2 || after.effects != before.effects+2 {
		t.Fatal("partial execution did not settle exactly the affordable items", before, after)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.uid), map[string]any{"amount_cents": 2500}, f.root, "renewal-batch-partial-refill"), 201)
	after = f.facts(t)
	assertRenewalBatchCounts(t, f.execute(t, batch, []int64{ids[2], ids[1], ids[0]}, "renewal-batch-partial-first"), 2, 1, 0)
	f.unchanged(t, after)
	assertRenewalBatchCounts(t, f.execute(t, batch, []int64{ids[1]}, "renewal-batch-partial-retry"), 3, 0, 0)
	finished := f.facts(t)
	if finished.balance != 0 || finished.ledger != after.ledger+1 || finished.effects != after.effects+1 {
		t.Fatal("new attempt did not settle only the failed item", after, finished)
	}
	assertRenewalBatchCounts(t, f.execute(t, batch, []int64{ids[2], ids[1], ids[0]}, "renewal-batch-partial-first"), 3, 0, 0)
	f.unchanged(t, finished)
}

func TestAdminServiceRenewalBatchConcurrentAndUnknownCommitRecovery(t *testing.T) {
	t.Run("same_batch_and_customer_payment", func(t *testing.T) {
		f := newRenewalBatchFixture(t, 1000)
		body := f.payBody(t, f.services)
		batch := f.register(t, body, "renewal-batch-concurrent-register")
		before := f.facts(t)
		start, done := make(chan struct{}), make(chan *httptest.ResponseRecorder, 3)
		for range 2 {
			go func() {
				<-start
				done <- request(f.h, "POST", renewalBatchDetailPath(batch)+"/execute", map[string]any{"service_ids": f.services}, f.staff, "renewal-batch-concurrent-execute")
			}()
		}
		invoice := body["items"].([]map[string]any)[0]["invoice_id"]
		go func() {
			<-start
			done <- request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(invoice.(float64))), map[string]any{}, f.customer, "renewal-batch-customer-race")
		}()
		close(start)
		for range 3 {
			w := awaitBalanceResponse(t, done)
			if w.Code != 200 && w.Code != 201 && w.Code != 409 {
				t.Fatal("concurrent batch/payment returned unexpected response", w.Code, w.Body.String())
			}
		}
		f.execute(t, batch, f.services, "renewal-batch-concurrent-execute")
		after := f.facts(t)
		if after.balance != before.balance-3500 || after.ledger != before.ledger+2 || after.effects != before.effects+2 {
			t.Fatal("concurrent batch/customer settlement duplicated or lost money", before, after)
		}
		f.execute(t, batch, f.services, "renewal-batch-concurrent-retry")
		f.unchanged(t, after)
	})
	// The attempt commit and the two actual balance-settlement commits lose
	// their acknowledgement. Both settled-item receipts must remain recoverable.
	for _, dropAt := range []int32{2, 4, 6} {
		t.Run(fmt.Sprintf("lost_commit_%d", dropAt), func(t *testing.T) {
			f := newRenewalBatchFixture(t, 1000)
			batch := f.register(t, f.payBody(t, f.services), "renewal-batch-unknown-register")
			before := f.facts(t)
			var commits atomic.Int32
			cfg := f.db.Pool.Config().Copy()
			cfg.ConnConfig.BuildFrontend = func(reader io.Reader, writer io.Writer) *pgproto3.Frontend {
				return pgproto3.NewFrontend(&uploadCommitAckReader{reader: reader, count: &commits, dropAt: dropAt}, writer)
			}
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			h := renewalBatchHTTPWithPool(f.db, pool)
			w := request(h, "POST", renewalBatchDetailPath(batch)+"/execute", map[string]any{"service_ids": f.services}, f.staff, "renewal-batch-unknown-execute")
			if commits.Load() < dropAt || (w.Code != 200 && w.Code != 500 && w.Code != 503) {
				t.Fatal("fixture did not lose its actual PostgreSQL COMMIT response", commits.Load(), w.Code, w.Body.String())
			}
			committed := f.facts(t)
			if dropAt == 4 && (committed.balance != before.balance-2500 || committed.effects != before.effects+1) {
				t.Fatal("first settlement COMMIT response was lost before its actual durable payment", before, committed)
			}
			if dropAt == 6 && (committed.balance != before.balance-3500 || committed.effects != before.effects+2) {
				t.Fatal("second settlement COMMIT response was lost before its actual durable payment", before, committed)
			}
			out := f.execute(t, batch, f.services, "renewal-batch-unknown-execute")
			assertRenewalBatchCounts(t, out, 2, 0, 0)
			after := f.facts(t)
			if after.balance != before.balance-3500 || after.ledger != before.ledger+2 || after.effects != before.effects+2 {
				t.Fatal("original attempt failed to recover exactly once after unknown commit", before, after)
			}
			f.execute(t, batch, f.services, "renewal-batch-unknown-execute")
			f.unchanged(t, after)
		})
	}
}

func TestAdminServiceRenewalBatchOriginalInvoiceZeroAndMixedSupplier(t *testing.T) {
	t.Run("original_invoice_and_explicit_zero", func(t *testing.T) {
		f := newRenewalBatchFixture(t, 1000)
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET renewal_discount_percent=100,renewal_promo_code='BATCH-FREE-ONCE',renewal_discount_limit=1,version=version+1 WHERE id=$1", f.services[1]); err != nil {
			t.Fatal(err)
		}
		body := f.payBody(t, f.services)
		items := body["items"].([]map[string]any)
		if items[1]["amount_cents"] != float64(0) {
			t.Fatal("zero-price fixture not explicit", items)
		}
		before := f.facts(t)
		batch := f.register(t, body, "renewal-batch-original-register")
		f.unchanged(t, before)
		// Registration cannot replace or settle either pre-existing invoice.
		assertRenewalBatchCounts(t, f.execute(t, batch, f.services, "renewal-batch-original-pay"), 2, 0, 0)
		after := f.facts(t)
		if after.invoices != before.invoices || after.balance != before.balance-2500 || after.effects != before.effects+2 {
			t.Fatal("batch payment replaced an original invoice or lost zero entitlement", before, after)
		}
		var contract, used int64
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT recurring_price_cents,renewal_discounts_used FROM services WHERE id=$1", f.services[1]).Scan(&contract, &used); err != nil || contract != 1000 || used != 1 {
			t.Fatal("batch free period cleared future contracted price", contract, used, err)
		}
	})
	t.Run("mixed_local_supplier", func(t *testing.T) {
		supplier := supplierSetup(t)
		supplier.publish(t, supplier.pid, "renewal-batch-mixed-supplier")
		sid, _ := supplier.purchase(t, supplier.pid, "renewal-batch-mixed-purchase")
		if err := supplier.db.ProcessProvisionJob(t.Context(), *supplier.claim(t)); err != nil {
			t.Fatal(err)
		}
		actor, staff := user(t, supplier.db, supplier.h, "renewal-batch-mixed-admin", "admin")
		contentRole(t, supplier.db, actor, "services.view", "invoices.view", "services.renew", "services.renew.pay_balance")
		base := adminRenewalFixture{upgradeFixture: upgradeFixture{db: supplier.db, h: supplier.h, root: supplier.root, customer: supplier.customer, uid: supplier.uid, sid: sid}, actor: actor, staff: staff}
		local := addRenewalBatchService(t, base, 1000, 1)
		f := renewalBatchFixture{adminRenewalFixture: base, services: []int64{local, sid}}
		var due time.Time
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&due); err != nil {
			t.Fatal(err)
		}
		batch := f.register(t, f.payBody(t, f.services), "renewal-batch-mixed-register")
		out := f.execute(t, batch, f.services, "renewal-batch-mixed-execute")
		assertRenewalBatchCounts(t, out, 2, 0, 0)
		items := renewalBatchItems(t, out)
		if items[0]["result"].(map[string]any)["renewal_status"] != "completed" || items[1]["result"].(map[string]any)["renewal_status"] != "pending" {
			t.Fatal("batch conflated local payment with supplier confirmation", out)
		}
		var afterDue time.Time
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&afterDue); err != nil || !afterDue.Equal(due) || supplier.renews.Load() != 0 || supplier.renewalPays.Load() != 0 {
			t.Fatal("batch fabricated an upstream extension before worker confirmation", due, afterDue, err)
		}
		if err := f.db.ProcessProvisionJob(t.Context(), *supplier.claim(t)); err != nil {
			t.Fatal(err)
		}
		if supplier.renews.Load() != 1 || supplier.renewalPays.Load() != 1 {
			t.Fatal("mixed batch did not use the existing supplier renewal worker")
		}
		f.execute(t, batch, f.services, "renewal-batch-mixed-execute")
		if supplier.renews.Load() != 1 || supplier.renewalPays.Load() != 1 {
			t.Fatal("mixed batch replay duplicated supplier renewal")
		}
	})
}

func TestAdminServiceRenewalBatchAuditRollbackAndExplicitResume(t *testing.T) {
	f := newRenewalBatchFixture(t, 1000)
	body := f.payBody(t, f.services)
	batch := f.register(t, body, "renewal-batch-audit-register")
	blockedInvoice := int64(body["items"].([]map[string]any)[1]["invoice_id"].(float64))
	if _, err := f.db.Pool.Exec(t.Context(), fmt.Sprintf(`CREATE FUNCTION reject_batch_second_payment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.action='invoice.paid' AND NEW.entity_id='%d' THEN RAISE EXCEPTION 'injected second batch payment audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_batch_second_payment BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_batch_second_payment()`, blockedInvoice)); err != nil {
		t.Fatal(err)
	}
	before := f.facts(t)
	w := request(f.h, "POST", renewalBatchDetailPath(batch)+"/execute", map[string]any{"service_ids": f.services}, f.staff, "renewal-batch-audit-first")
	if w.Code != 200 && w.Code != 500 && w.Code != 503 {
		t.Fatal("unexpected injected audit failure response", w.Code, w.Body.String())
	}
	after := f.facts(t)
	if after.balance != before.balance-2500 || after.ledger != before.ledger+1 || after.effects != before.effects+1 {
		t.Fatal("later audit failure lost first settlement or partially committed second", before, after)
	}
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", blockedInvoice).Scan(&status); err != nil || status != "unpaid" {
		t.Fatal("audit-rejected item no longer unpaid", status, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_batch_second_payment"); err != nil {
		t.Fatal(err)
	}
	// The first item's stable success key is shared by later attempts.
	out := f.execute(t, batch, f.services, "renewal-batch-audit-resume")
	assertRenewalBatchCounts(t, out, 2, 0, 0)
	finished := f.facts(t)
	if finished.balance != before.balance-3500 || finished.ledger != before.ledger+2 || finished.effects != before.effects+2 {
		t.Fatal("explicit resume duplicated an earlier successful settlement", before, finished)
	}
}

func TestAdminServiceRenewalBatchMidExecutionExpiryStopsLaterItems(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		t.Run(credential, func(t *testing.T) {
			f := newRenewalBatchFixture(t, 1000, 500)
			body := f.payBody(t, f.services)
			batch := f.register(t, body, "renewal-batch-expiry-register")
			secondInvoice := int64(body["items"].([]map[string]any)[1]["invoice_id"].(float64))
			secret, table, column, hash := adminRenewalTestCredential(t, f.adminRenewalFixture, credential)
			if _, err := f.db.Pool.Exec(t.Context(), fmt.Sprintf(`CREATE FUNCTION gate_batch_second_payment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.action='invoice.paid' AND NEW.entity_id='%d' THEN PERFORM pg_advisory_xact_lock(hashtextextended(TG_TABLE_SCHEMA||':renewal-batch-expiry',0)); END IF; RETURN NEW; END $$;
 CREATE TRIGGER gate_batch_second_payment BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION gate_batch_second_payment()`, secondInvoice)); err != nil {
				t.Fatal(err)
			}
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended(current_schema()||':renewal-batch-expiry',0))"); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '2 seconds' WHERE "+column+"=$1", hash); err != nil {
				t.Fatal(err)
			}
			before := f.facts(t)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- adminRenewalCredentialRequest(f.adminRenewalFixture, renewalBatchDetailPath(batch)+"/execute", map[string]any{"service_ids": f.services}, "renewal-batch-expiry-execute", secret)
			}()
			waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
			waitAdminRenewalCredentialExpiry(t, f.db, table, column, hash)
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 401)
			after := f.facts(t)
			if after.balance != before.balance-2500 || after.ledger != before.ledger+1 || after.effects != before.effects+1 {
				t.Fatal("expired batch did not preserve only its already committed first item", before, after)
			}
			for _, raw := range body["items"].([]map[string]any)[1:] {
				var status string
				if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", int64(raw["invoice_id"].(float64))).Scan(&status); err != nil || status != "unpaid" {
					t.Fatal("expired attempt continued into a later invoice", raw, status, err)
				}
			}
		})
	}
}

func TestAdminServiceRenewalBatchCreateExecutionAndRecovery(t *testing.T) {
	f := newRenewalBatchFixture(t, 1000)
	body := f.createBody(t, f.services)
	batch := f.register(t, body, "renewal-batch-create-register")
	before := f.facts(t)
	out := f.execute(t, batch, []int64{f.services[0]}, "renewal-batch-create-first-item")
	assertRenewalBatchCounts(t, out, 1, 0, 1)
	items := renewalBatchItems(t, out)
	first := items[0]["result"].(map[string]any)
	if first["status"] != "unpaid" || first["amount_cents"] != float64(2500) || first["customer_id"] != float64(f.uid) {
		t.Fatal("batch creation did not retain normal unpaid-invoice semantics", out)
	}
	middle := f.facts(t)
	if middle.invoices != before.invoices+1 || middle.items != before.items+1 || middle.balance != before.balance || middle.ledger != before.ledger || middle.effects != before.effects || middle.services != before.services {
		t.Fatal("creating one batch item changed customer entitlement or balance", before, middle)
	}
	// Later recovery may include the successful item, but its stable single
	// operation key must return the original invoice rather than create again.
	out = f.execute(t, batch, f.services, "renewal-batch-create-finish")
	assertRenewalBatchCounts(t, out, 2, 0, 0)
	if !reflect.DeepEqual(renewalBatchItems(t, out)[0]["result"], first) {
		t.Fatal("later attempt rewrote the first item's committed result", first, out)
	}
	finished := f.facts(t)
	if finished.invoices != before.invoices+2 || finished.balance != before.balance || finished.effects != before.effects || finished.services != before.services {
		t.Fatal("batch create recovery duplicated invoices or applied renewal early", before, finished)
	}
	// A single execute key permanently binds the selected subset.
	expect(t, request(f.h, "POST", renewalBatchDetailPath(batch)+"/execute", map[string]any{"service_ids": f.services}, f.staff, "renewal-batch-create-first-item"), 409)
	f.unchanged(t, finished)
}

func TestAdminServiceRenewalBatchProgressIgnoresBusinessRowLocks(t *testing.T) {
	f := newRenewalBatchFixture(t, 1000)
	body := f.payBody(t, f.services)
	batch := f.register(t, body, "renewal-batch-readable-register")
	progress := f.execute(t, batch, []int64{f.services[0]}, "renewal-batch-readable-first")
	assertRenewalBatchCounts(t, progress, 1, 0, 1)
	before := f.facts(t)
	for _, resource := range []struct {
		table string
		id    int64
	}{
		{"services", f.services[1]},
		{"invoices", int64(body["items"].([]map[string]any)[1]["invoice_id"].(float64))},
		{"users", f.uid},
	} {
		t.Run(resource.table, func(t *testing.T) {
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(t.Context(), "SELECT id FROM "+resource.table+" WHERE id=$1 FOR UPDATE", resource.id); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- request(f.h, "GET", renewalBatchDetailPath(batch), nil, f.staff, "")
			}()
			// The lock remains held while checking the response. Old FOR SHARE
			// progress reads either blocked on the wallet or returned a conflict.
			select {
			case w := <-done:
				expect(t, w, 200)
				out := data(t, w)
				assertRenewalBatchCounts(t, out, 1, 0, 1)
				if !reflect.DeepEqual(renewalBatchItems(t, out), renewalBatchItems(t, progress)) {
					t.Fatal("resource writer changed already committed progress", out, progress)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("batch progress waited for a business row rather than reading committed results", resource.table)
			}
			if err = gate.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.unchanged(t, before)
		})
	}
}

func TestAdminServiceRenewalBatchSingleConnectionPoolConcurrentProgress(t *testing.T) {
	f := newRenewalBatchFixture(t, 1000)
	body := f.payBody(t, f.services)
	items := body["items"].([]map[string]any)
	batches := make([]map[string]any, 0, 2)
	for i := range items {
		batches = append(batches, f.register(t, map[string]any{
			"customer_id": f.uid, "action": "pay_balance", "reason": "单连接池内独立批次并发付款", "items": []map[string]any{items[i]},
		}, fmt.Sprintf("renewal-batch-single-pool-register-%d", i)))
	}
	before := f.facts(t)
	cfg := f.db.Pool.Config().Copy()
	cfg.MaxConns, cfg.MinConns, cfg.MinIdleConns = 1, 0, 0
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := renewalBatchHTTPWithPool(f.db, pool)
	start, done := make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
	for i := range batches {
		go func(i int) {
			<-start
			done <- request(h, "POST", renewalBatchDetailPath(batches[i])+"/execute", map[string]any{"service_ids": []int64{f.services[i]}}, f.staff, fmt.Sprintf("renewal-batch-single-pool-execute-%d", i))
		}(i)
	}
	close(start)
	for range batches {
		w := awaitBalanceResponse(t, done)
		expect(t, w, 200)
		assertRenewalBatchCounts(t, data(t, w), 1, 0, 0)
	}
	after := f.facts(t)
	if after.balance != before.balance-3500 || after.ledger != before.ledger+2 || after.effects != before.effects+2 {
		t.Fatal("single-connection concurrent batches lost or duplicated settlement", before, after)
	}
	if pool.Stat().MaxConns() != 1 || pool.Stat().TotalConns() > 1 {
		t.Fatal("fixture silently expanded the business connection pool", pool.Stat().MaxConns(), pool.Stat().TotalConns())
	}
	for i, batch := range batches {
		w := request(h, "POST", renewalBatchDetailPath(batch)+"/execute", map[string]any{"service_ids": []int64{f.services[i]}}, f.staff, fmt.Sprintf("renewal-batch-single-pool-execute-%d", i))
		expect(t, w, 200)
		assertRenewalBatchCounts(t, data(t, w), 1, 0, 0)
	}
	f.unchanged(t, after)
}
