package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

// Build orders through the same cart/admin quote APIs used by customers and
// employees. Never synthesize paid orders/services or rewrite delivery state.
type orderExternalRefundHTTPFixture struct {
	db             *store.Store
	h              http.Handler
	root, customer string
	uid            int64
	products       []int64
}

type orderExternalRefundHTTPOrder struct {
	order, invoice, payment, amount int64
	callback                        url.Values
}

func newOrderExternalRefundHTTPFixture(t *testing.T) orderExternalRefundHTTPFixture {
	t.Helper()
	db, h := testServerConfig(t, func(c *config.Config) { *c = epayTestConfig() })
	return setupOrderExternalRefundHTTPFixture(t, db, h, "")
}

func setupOrderExternalRefundHTTPFixture(t *testing.T, db *store.Store, h http.Handler, gateway string) orderExternalRefundHTTPFixture {
	t.Helper()
	cfg := epayTestConfig()
	if gateway != "" {
		cfg.EpayAPIURL = gateway
	}
	h = New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	_, root := user(t, db, h, "order-refund-root", "super_admin")
	uid, customer := user(t, db, h, "order-refund-customer", "customer")
	f := orderExternalRefundHTTPFixture{db: db, h: h, root: root, customer: customer, uid: uid}
	for i := 0; i < 2; i++ {
		p := productInput(fmt.Sprintf("order-refund-product-%d", i), nil)
		p.ApprovalRequired = true
		stock := 100
		p.Stock = &stock
		if i == 1 {
			p.PriceCents = 800
			p.SetupFeeCents = 200
		}
		f.products = append(f.products, managedProductID(t, h, root, p))
	}
	expect(t, request(h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "ORDERREFUND10", "discount_type": "percent", "value": 10}, root, ""), 201)
	// Seed a real positive balance independently of the externally paid order, so
	// an accidental positive/negative balance refund cannot hide behind zero.
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), map[string]any{"amount_cents": 12345}, root, "order-refund-initial-balance"), 201)
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/credit/%d/limit", uid), map[string]any{"limit_cents": 100000, "expected_version": 1, "note": "isolated account unchanged proof"}, root, "order-refund-initial-credit-limit"), 201)
	return f
}

func (f orderExternalRefundHTTPFixture) paid(t *testing.T, mode, key string) orderExternalRefundHTTPOrder {
	t.Helper()
	var created map[string]any
	if mode == "cart" {
		for i, p := range f.products {
			qty := 1
			if i == 0 {
				qty = 2
			}
			add(t, f.h, f.customer, p, qty)
		}
		v, _ := cart(t, f.h, f.customer)
		expect(t, request(f.h, "POST", "/api/v1/cart/promo", map[string]any{"version": v, "code": "ORDERREFUND10"}, f.customer, ""), 200)
		cartData := data(t, request(f.h, "GET", "/api/v1/cart", nil, f.customer, ""))
		ids := []int64{}
		for _, raw := range cartData["items"].([]any) {
			ids = append(ids, int64(raw.(map[string]any)["id"].(float64)))
		}
		response := request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(cartData["version"].(float64)), ItemIDs: ids}, f.customer, key+"-checkout")
		expect(t, response, 201)
		created = data(t, response)
	} else if mode == "admin" {
		input := store.AdminOrderInput{CustomerID: f.uid, PromoCode: "ORDERREFUND10", Notes: "isolated manual pending-review order"}
		for i, p := range f.products {
			qty := 1
			if i == 0 {
				qty = 2
			}
			input.Items = append(input.Items, store.AdminOrderItemInput{ProductID: p, ExpectedRevision: *configRevision(t, f.h, f.root, p), BillingCycle: "monthly", Quantity: qty})
		}
		quote := request(f.h, "POST", "/api/v1/admin/orders/quote", input, f.root, "")
		expect(t, quote, 200)
		input.QuoteHash = data(t, quote)["quote_hash"].(string)
		response := request(f.h, "POST", "/api/v1/admin/orders", input, f.root, key+"-checkout")
		expect(t, response, 201)
		created = data(t, response)
	} else {
		t.Fatalf("unsupported real creation path %q", mode)
	}
	out := orderExternalRefundHTTPOrder{order: int64(created["order_id"].(float64)), invoice: int64(created["invoice_id"].(float64)), amount: int64(created["total_cents"].(float64))}
	response := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", out.invoice), map[string]any{"provider": "epay_alipay", "expected_version": 1}, f.customer, key+"-payment")
	expect(t, response, 201)
	checkout, err := url.Parse(data(t, response)["checkout_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	out.callback = epayNotification(checkout.Query(), key+"-trade")
	callback := epayCallback(f.h, "POST", "epay_alipay", out.callback)
	if callback.Code != 200 || callback.Body.String() != "success" {
		t.Fatalf("signed settlement failed: %d", callback.Code)
	}
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT id FROM payment_transactions WHERE invoice_id=$1`, out.invoice).Scan(&out.payment); err != nil {
		t.Fatal(err)
	}
	f.assertPaidPending(t, out)
	return out
}

func (f orderExternalRefundHTTPFixture) assertPaidPending(t *testing.T, out orderExternalRefundHTTPOrder) {
	t.Helper()
	var status, review, invoiceStatus, paymentStatus string
	var services, jobs, quantity, discount, setup, total, balance, affiliate int64
	err := f.db.Pool.QueryRow(t.Context(), `SELECT o.status,o.review_status,i.status,p.status,
 (SELECT count(*) FROM services WHERE order_id=o.id AND status='pending_payment' AND provision_mode='manual' AND NOT inventory_released AND next_due_at IS NULL),
 (SELECT count(*) FROM provision_jobs WHERE service_id IN (SELECT id FROM services WHERE order_id=o.id)),
 (SELECT sum(quantity) FROM order_items WHERE order_id=o.id),o.discount_cents,
 (SELECT sum(setup_fee_cents*quantity) FROM order_items WHERE order_id=o.id),i.amount_cents,u.balance_cents,
 (SELECT count(*) FROM affiliate_turnover_entries WHERE invoice_id=i.id)+(SELECT count(*) FROM affiliate_commission_entries WHERE invoice_id=i.id)
 FROM orders o JOIN invoices i ON i.order_id=o.id JOIN payment_transactions p ON p.invoice_id=i.id JOIN users u ON u.id=i.user_id WHERE i.id=$1`, out.invoice).Scan(&status, &review, &invoiceStatus, &paymentStatus, &services, &jobs, &quantity, &discount, &setup, &total, &balance, &affiliate)
	if err != nil {
		t.Fatal(err)
	}
	if status != "paid" || review != "pending" || invoiceStatus != "paid" || paymentStatus != "succeeded" || services != 3 || quantity != 3 || jobs != 0 || discount != 520 || setup != 400 || total != 4680 || total != out.amount || balance != 12345 || affiliate != 0 {
		t.Fatalf("real creation invariant status=%s/%s/%s/%s services/jobs/quantity=%d/%d/%d discount/setup/total=%d/%d/%d balance=%d affiliate=%d", status, review, invoiceStatus, paymentStatus, services, jobs, quantity, discount, setup, total, balance, affiliate)
	}
}

func TestOrderExternalRefundRealCreationFixtures(t *testing.T) {
	for _, mode := range []string{"cart", "admin"} {
		t.Run(mode, func(t *testing.T) {
			f := newOrderExternalRefundHTTPFixture(t)
			out := f.paid(t, mode, "order-refund-real-"+mode)
			var itemTypes string
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT coalesce(string_agg(item_type||':'||amount_cents::text,',' ORDER BY id),'order_items authoritative; invoice_items empty') FROM invoice_items WHERE invoice_id=$1`, out.invoice).Scan(&itemTypes); err != nil {
				t.Fatal(err)
			}
			t.Logf("real %s signed settlement: 3 manual services pending_payment; order paid/pending; jobs=0; amount=4680; discount=520; setup=400; invoice lines=%s", mode, itemTypes)
		})
	}
}

func orderExternalRefundContextPath(invoice int64) string {
	return fmt.Sprintf("/api/v1/admin/invoices/%d/external-refund-order-context", invoice)
}
func orderExternalRefundPath(invoice int64) string {
	return fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", invoice)
}
func (f orderExternalRefundHTTPFixture) context(t *testing.T, out orderExternalRefundHTTPOrder, cookie string) map[string]any {
	t.Helper()
	w := request(f.h, "GET", orderExternalRefundContextPath(out.invoice), nil, cookie, "")
	expect(t, w, 200)
	value := data(t, w)
	if _, ok := value["order_reversal"]; !ok {
		value["order_reversal"] = nil
	}
	orderExternalRefundWhitelist(t, value, "eligible", "reason_code", "reason", "invoice_id", "invoice_version", "order_id", "order_version", "services", "order_reversal")
	services, ok := value["services"].([]any)
	if !ok {
		t.Fatal("context services must be array")
	}
	var previous float64
	for _, raw := range services {
		service := raw.(map[string]any)
		orderExternalRefundWhitelist(t, service, "id", "version", "product_id", "order_item_id", "unit_number", "status", "provision_mode", "inventory_released")
		if service["id"].(float64) <= previous {
			t.Fatal("context services not strictly id ordered")
		}
		previous = service["id"].(float64)
	}
	if raw := value["order_reversal"]; raw != nil {
		orderExternalRefundProjection(t, raw.(map[string]any), "")
	}
	return value
}
func orderExternalRefundWhitelist(t *testing.T, value map[string]any, keys ...string) {
	t.Helper()
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range value {
		if !allowed[key] {
			t.Fatalf("unexpected public projection field %s", key)
		}
	}
	for _, key := range keys {
		if _, ok := value[key]; !ok {
			t.Fatalf("missing public projection field %s", key)
		}
	}
}
func orderExternalRefundProjection(t *testing.T, value map[string]any, status string) {
	t.Helper()
	orderExternalRefundWhitelist(t, value, "order_id", "status", "service_count", "stock_units_released", "invoice_version", "order_version", "created_at", "applied_at", "released_at")
	if value["service_count"] != float64(3) || status != "" && value["status"] != status {
		t.Fatalf("order reversal projection mismatch status=%v services=%v", value["status"], value["service_count"])
	}
}
func (f orderExternalRefundHTTPFixture) input(t *testing.T, out orderExternalRefundHTTPOrder, key string) map[string]any {
	t.Helper()
	c := f.context(t, out, f.root)
	if c["eligible"] != true {
		t.Fatalf("real fixture ineligible: %v", c["reason_code"])
	}
	versions := []map[string]any{}
	for _, raw := range c["services"].([]any) {
		s := raw.(map[string]any)
		versions = append(versions, map[string]any{"id": s["id"], "version": s["version"]})
	}
	return map[string]any{"payment_id": out.payment, "amount_cents": out.amount, "method": "original_route", "provider_refund_id": key, "received_at": time.Now().UTC().Format(time.RFC3339Nano), "evidence_ref": "test://order-refund/" + key, "evidence_sha256": strings.Repeat("a", 64), "note": "隔离订单退款到账证据，无真实商户出款", "expected_version": c["invoice_version"], "expected_order_version": c["order_version"], "expected_service_versions": versions}
}
func orderExternalRefundClone(in map[string]any) map[string]any {
	encoded, _ := json.Marshal(in)
	var out map[string]any
	_ = json.Unmarshal(encoded, &out)
	return out
}
func orderExternalRefundCode(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	expect(t, w, status)
	if errorData(t, w)["code"] != code {
		t.Fatalf("error code=%v want=%s", errorData(t, w)["code"], code)
	}
}

// Snapshot domain rows only: permission/session revocations used by the tests
// are deliberately outside the business facts which must not partially mutate.
func (f orderExternalRefundHTTPFixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	rows, err := f.db.Pool.Query(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() AND (tablename IN ('orders','order_items','invoices','invoice_items','services','products','provision_jobs','payment_transactions','payment_webhook_events','balance_ledger','customer_credit_accounts','credit_invoice_payments','affiliate_turnover_entries','affiliate_commission_entries','audit_events') OR tablename LIKE 'external_refund%' OR tablename LIKE 'customer_credit%' OR tablename LIKE 'credit_%') ORDER BY tablename`)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, name := range names {
		var value string
		if err = f.db.Pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text COLLATE "C"),'[]')::text FROM `+pgx.Identifier{name}.Sanitize()+` r`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		result[name] = value
	}
	var balance string
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT balance_cents::text FROM users WHERE id=$1`, f.uid).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	result["customer_balance"] = balance
	return result
}
func orderExternalRefundUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		for table, value := range before {
			if after[table] != value {
				t.Errorf("denied/replayed operation changed %s", table)
			}
		}
		t.FailNow()
	}
}

func TestOrderExternalRefundHTTPRealCreationConfirmAndReplay(t *testing.T) {
	for _, mode := range []string{"cart", "admin"} {
		t.Run(mode, func(t *testing.T) {
			f := newOrderExternalRefundHTTPFixture(t)
			out := f.paid(t, mode, "order-refund-confirm-"+mode)
			_, reviewer := externalRefundActor(t, f.db, f.h, "order-refund-reviewer", []string{"invoices.view", "external_refunds.view", "external_refunds.review", "external_refunds.order_reverse"})
			_, reader := externalRefundActor(t, f.db, f.h, "order-refund-reader", []string{"invoices.view", "external_refunds.view"})
			c := f.context(t, out, reader)
			if c["eligible"] != true || len(c["services"].([]any)) != 3 || c["order_reversal"] != nil {
				t.Fatal("new real order context mismatch")
			}
			in := f.input(t, out, "order-refund-confirm-proof")
			original := f.snapshot(t)
			var originalPaidAt string
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT paid_at::text FROM invoices WHERE id=$1`, out.invoice).Scan(&originalPaidAt); err != nil {
				t.Fatal(err)
			}
			created := request(f.h, "POST", orderExternalRefundPath(out.invoice), in, f.root, "order-refund-confirm-create")
			expect(t, created, 201)
			refund := data(t, created)["refund"].(map[string]any)
			orderExternalRefundProjection(t, refund["order_reversal"].(map[string]any), "pending")
			if refund["reversal"] != nil {
				t.Fatal("order refund unexpectedly reserved balance")
			}
			pending := f.context(t, out, reader)
			orderExternalRefundProjection(t, pending["order_reversal"].(map[string]any), "pending")
			if pending["eligible"] != false {
				t.Fatal("held order still eligible")
			}
			f.assertPaidPending(t, out)
			reviewPath := fmt.Sprintf("/api/v1/admin/orders/%d/review", out.order)
			orderExternalRefundCode(t, request(f.h, "POST", reviewPath, map[string]any{"decision": "approved", "note": "must not deliver during refund hold", "expected_version": orderVersion(t, f.db, out.order)}, f.root, "order-refund-held-review"), 409, "external_refund_order_hold")
			// The same signed success notification must never enqueue held delivery.
			if callback := epayCallback(f.h, "POST", "epay_alipay", out.callback); callback.Code != 200 || callback.Body.String() != "success" {
				t.Fatal("pending signed success replay rejected")
			}
			f.assertPaidPending(t, out)
			heldFacts := f.snapshot(t)
			// Simulate a writer bypassing the HTTP review route: DB guards must
			// reject new delivery tasks as well as the ordinary approval API.
			if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key) SELECT id,'create','order-refund-held-job-'||id FROM services WHERE order_id=$1`, out.order); err == nil {
				t.Fatal("held order accepted direct delivery task insertion")
			}
			orderExternalRefundUnchanged(t, heldFacts, f.snapshot(t))
			confirmPath := fmt.Sprintf("%s/%.0f/confirm", orderExternalRefundPath(out.invoice), refund["id"])
			review := map[string]any{"expected_version": 1, "note": "独立员工核对订单整单退款"}
			expect(t, request(f.h, "POST", confirmPath, review, reviewer, "order-refund-confirm-final"), 201)
			after := f.snapshot(t)
			replay := request(f.h, "POST", confirmPath, review, reviewer, "order-refund-confirm-final")
			expect(t, replay, 200)
			if data(t, replay)["replayed"] != true {
				t.Fatal("confirm missing replay flag")
			}
			orderExternalRefundUnchanged(t, after, f.snapshot(t))
			expect(t, request(f.h, "POST", confirmPath, review, reviewer, "order-refund-confirm-other"), 409)
			if callback := epayCallback(f.h, "POST", "epay_alipay", out.callback); callback.Code != 200 || callback.Body.String() != "success" {
				t.Fatal("refunded signed success replay rejected")
			}
			f.assertConfirmed(t, out)
			var finalPaidAt string
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT paid_at::text FROM invoices WHERE id=$1`, out.invoice).Scan(&finalPaidAt); err != nil || finalPaidAt != originalPaidAt {
				t.Fatal("external order refund changed original paid_at")
			}
			for _, table := range []string{"payment_transactions", "payment_webhook_events", "balance_ledger", "customer_credit_accounts", "credit_invoice_payments", "customer_balance"} {
				if original[table] != f.snapshot(t)[table] {
					t.Fatalf("order refund changed original money fact %s", table)
				}
			}
			applied := f.context(t, out, reader)
			orderExternalRefundProjection(t, applied["order_reversal"].(map[string]any), "applied")
			// Creation replay remains the original pending receipt, even after final review.
			receipt := request(f.h, "POST", orderExternalRefundPath(out.invoice), in, f.root, "order-refund-confirm-create")
			expect(t, receipt, 200)
			orderExternalRefundProjection(t, data(t, receipt)["refund"].(map[string]any)["order_reversal"].(map[string]any), "pending")
			f.assertConfirmed(t, out)
		})
	}
}
func (f orderExternalRefundHTTPFixture) assertConfirmed(t *testing.T, out orderExternalRefundHTTPOrder) {
	t.Helper()
	var invoice, order, review, payment string
	var cancelled, jobs, stock1, stock2, confirmed, balance, ledger int64
	var paidAt bool
	err := f.db.Pool.QueryRow(t.Context(), `SELECT i.status,o.status,o.review_status,p.status,i.paid_at IS NOT NULL,
 (SELECT count(*) FROM services WHERE order_id=o.id AND status='cancelled' AND inventory_released),
 (SELECT count(*) FROM provision_jobs WHERE service_id IN (SELECT id FROM services WHERE order_id=o.id)),
 (SELECT stock FROM products WHERE id=$2),(SELECT stock FROM products WHERE id=$3),
 (SELECT count(*) FROM external_refunds WHERE invoice_id=i.id AND status='confirmed'),u.balance_cents,
 (SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=i.id)
 FROM invoices i JOIN orders o ON o.id=i.order_id JOIN payment_transactions p ON p.invoice_id=i.id JOIN users u ON u.id=i.user_id WHERE i.id=$1`, out.invoice, f.products[0], f.products[1]).Scan(&invoice, &order, &review, &payment, &paidAt, &cancelled, &jobs, &stock1, &stock2, &confirmed, &balance, &ledger)
	if err != nil {
		t.Fatal(err)
	}
	if invoice != "refunded" || order != "refunded" || review != "pending" || payment != "succeeded" || !paidAt || cancelled != 3 || jobs != 0 || stock1 != 100 || stock2 != 100 || confirmed != 1 || balance != 12345 || ledger != 0 {
		t.Fatalf("confirmed order boundary %s/%s/%s/%s paid=%t cancelled/jobs/stock/refunds/balance/ledger=%d/%d/%d/%d/%d/%d/%d", invoice, order, review, payment, paidAt, cancelled, jobs, stock1, stock2, confirmed, balance, ledger)
	}
	summary := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/transactions/summary?customer_id=%d", f.uid), nil, f.root, ""))
	if summary["gateway_income_cents"] != float64(out.amount) || summary["external_refund_cents"] != float64(out.amount) || summary["net_gateway_cents"] != float64(0) {
		t.Fatal("order external cash counted more than once")
	}
}

func TestOrderExternalRefundHTTPPermissionsVersionsAndReject(t *testing.T) {
	f := newOrderExternalRefundHTTPFixture(t)
	out := f.paid(t, "admin", "order-refund-permission-order")
	path := orderExternalRefundPath(out.invoice)
	all := []string{"invoices.view", "external_refunds.view", "external_refunds.create", "external_refunds.review", "external_refunds.order_reverse"}
	requesterID, requester := externalRefundActor(t, f.db, f.h, "order-refund-requester", all)
	_, reviewer := externalRefundActor(t, f.db, f.h, "order-refund-reviewer", all)
	_, reader := externalRefundActor(t, f.db, f.h, "order-refund-reader", []string{"invoices.view", "external_refunds.view"})
	_, noReverse := externalRefundActor(t, f.db, f.h, "order-refund-no-reverse", []string{"invoices.view", "external_refunds.view", "external_refunds.create", "external_refunds.review"})
	_, reverseOnly := externalRefundActor(t, f.db, f.h, "order-refund-reverse-only", []string{"invoices.view", "external_refunds.view", "external_refunds.order_reverse"})
	limitedID, limited := externalRefundActor(t, f.db, f.h, "order-refund-limited", all)
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1`, limitedID); err != nil {
		t.Fatal(err)
	}
	in := f.input(t, out, "order-refund-permission-proof")
	before := f.snapshot(t)
	for _, actor := range []string{reader, noReverse, reverseOnly, f.customer} {
		expect(t, request(f.h, "POST", path, in, actor, "order-refund-no-grant"), 403)
	}
	expect(t, request(f.h, "GET", orderExternalRefundContextPath(out.invoice), nil, limited, ""), 404)
	expect(t, request(f.h, "POST", path, in, limited, "order-refund-outside-scope"), 404)
	expect(t, request(f.h, "POST", path, in, "", "order-refund-no-session"), 401)
	// A genuine enabled API key remains forbidden for this browser-only workflow.
	secret := "zjmfgo_" + strings.Repeat("e", 64)
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO api_keys(user_id,name,prefix,secret_hash) VALUES($1,'order-refund-contract','zjmfgo_eeeeeeeeeeeeeeee',$2)`, requesterID, tokenHash(secret)); err != nil {
		t.Fatal(err)
	}
	expect(t, requestBearer(f.h, "POST", path, in, secret), 403)
	expect(t, requestBearer(f.h, "GET", orderExternalRefundContextPath(out.invoice), nil, secret), 403)
	if c := f.context(t, out, noReverse); c["eligible"] != true {
		t.Fatal("eligibility confused with operation permission")
	}
	orderExternalRefundUnchanged(t, before, f.snapshot(t))
	for _, scenario := range []string{"invoice", "order", "service", "missing_service", "duplicate_service", "foreign_service"} {
		t.Run(scenario, func(t *testing.T) {
			bad := orderExternalRefundClone(in)
			versions := bad["expected_service_versions"].([]any)
			switch scenario {
			case "invoice":
				bad["expected_version"] = float64(99)
			case "order":
				bad["expected_order_version"] = float64(99)
			case "service":
				versions[0].(map[string]any)["version"] = float64(99)
			case "missing_service":
				bad["expected_service_versions"] = versions[:len(versions)-1]
			case "duplicate_service":
				bad["expected_service_versions"] = append(versions, versions[0])
			case "foreign_service":
				versions[0].(map[string]any)["id"] = float64(99999999)
			}
			expect(t, request(f.h, "POST", path, bad, requester, "order-refund-version-"+scenario), 409)
			orderExternalRefundUnchanged(t, before, f.snapshot(t))
		})
	}
	created := request(f.h, "POST", path, in, requester, "order-refund-permission-create")
	expect(t, created, 201)
	refund := data(t, created)["refund"].(map[string]any)
	pending := f.snapshot(t)
	expect(t, request(f.h, "POST", path, in, requester, "order-refund-permission-create"), 200)
	reversed := orderExternalRefundClone(in)
	serviceVersions := reversed["expected_service_versions"].([]any)
	for left, right := 0, len(serviceVersions)-1; left < right; left, right = left+1, right-1 {
		serviceVersions[left], serviceVersions[right] = serviceVersions[right], serviceVersions[left]
	}
	normalizedReplay := request(f.h, "POST", path, reversed, requester, "order-refund-permission-create")
	expect(t, normalizedReplay, 200)
	if data(t, normalizedReplay)["replayed"] != true {
		t.Fatal("unordered service versions did not normalize to original request")
	}
	orderExternalRefundUnchanged(t, pending, f.snapshot(t))
	changed := orderExternalRefundClone(in)
	changed["note"] = "same key different evidence"
	expect(t, request(f.h, "POST", path, changed, requester, "order-refund-permission-create"), 409)
	expect(t, request(f.h, "POST", path, in, requester, "order-refund-permission-other"), 409)
	confirm := fmt.Sprintf("%s/%.0f/confirm", path, refund["id"])
	reject := fmt.Sprintf("%s/%.0f/reject", path, refund["id"])
	review := map[string]any{"expected_version": 1, "note": "独立驳回证据待补正"}
	for _, action := range []string{confirm, reject} {
		expect(t, request(f.h, "POST", action, review, noReverse, "order-refund-no-reverse-review"), 403)
		expect(t, request(f.h, "POST", action, review, reverseOnly, "order-refund-no-review"), 403)
		expect(t, request(f.h, "POST", action, review, reader, "order-refund-reader-review"), 403)
		expect(t, requestBearer(f.h, "POST", action, review, secret), 403)
		orderExternalRefundCode(t, request(f.h, "POST", action, review, requester, "order-refund-self-review"), 403, "external_refund_self_review")
	}
	expect(t, request(f.h, "POST", reject, map[string]any{"expected_version": 99, "note": "stale review"}, reviewer, "order-refund-stale-reject"), 409)
	orderExternalRefundUnchanged(t, pending, f.snapshot(t))
	rejected := request(f.h, "POST", reject, review, reviewer, "order-refund-independent-reject")
	expect(t, rejected, 201)
	orderExternalRefundProjection(t, data(t, rejected)["refund"].(map[string]any)["order_reversal"].(map[string]any), "released")
	f.assertPaidPending(t, out)
	released := f.snapshot(t)
	expect(t, request(f.h, "POST", reject, review, reviewer, "order-refund-independent-reject"), 200)
	expect(t, request(f.h, "POST", path, in, requester, "order-refund-permission-create"), 200)
	orderExternalRefundUnchanged(t, released, f.snapshot(t))
	// Rejecting refund only releases the hold. It never approves delivery itself.
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/review", out.order), map[string]any{"decision": "approved", "note": "original order approval after refund rejection", "expected_version": orderVersion(t, f.db, out.order)}, f.root, "order-refund-approve-after-reject"), 201)
	var queued, jobs int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM services WHERE order_id=$1 AND status='queued'),(SELECT count(*) FROM provision_jobs WHERE service_id IN (SELECT id FROM services WHERE order_id=$1) AND action='create' AND status='pending')`, out.order).Scan(&queued, &jobs); err != nil || queued != 3 || jobs != 3 {
		t.Fatalf("released order could not follow ordinary approval: queued=%d jobs=%d err=%v", queued, jobs, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE sessions SET expires_at=now()-interval '1 second' WHERE user_id=$1`, requesterID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, in, requester, "order-refund-permission-create"), 401)
}

func TestOrderExternalRefundHTTPRevocationAfterActualLockWait(t *testing.T) {
	for _, operation := range []string{"context", "create", "confirm", "replay"} {
		for _, revoke := range []string{"scope", "permission"} {
			t.Run(operation+"_"+revoke, func(t *testing.T) {
				f := newOrderExternalRefundHTTPFixture(t)
				out := f.paid(t, "cart", "order-refund-lock-order")
				staffID, staff := externalRefundActor(t, f.db, f.h, "order-refund-lock-staff", []string{"external_refunds.view", "external_refunds.create", "external_refunds.review", "external_refunds.order_reverse"})
				input := f.input(t, out, "order-refund-lock-proof")
				path := orderExternalRefundPath(out.invoice)
				method := "POST"
				if operation == "confirm" {
					created := request(f.h, "POST", path, input, f.root, "order-refund-lock-create")
					expect(t, created, 201)
					path = fmt.Sprintf("%s/%.0f/confirm", path, data(t, created)["refund"].(map[string]any)["id"])
					input = map[string]any{"expected_version": 1, "note": "independent locked review"}
				} else if operation == "replay" {
					expect(t, request(f.h, "POST", path, input, staff, "order-refund-lock-write"), 201)
				} else if operation == "context" {
					method = "GET"
					path = orderExternalRefundContextPath(out.invoice)
					input = nil
				}
				before := f.snapshot(t)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				tx, err := f.db.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				var blocker int
				if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
					t.Fatal(err)
				}
				statement := `UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1`
				expected := 404
				if revoke == "permission" {
					statement = `UPDATE admin_roles SET permissions=array_remove(permissions,'external_refunds.order_reverse') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`
					expected = 403
					if operation == "context" {
						statement = `UPDATE admin_roles SET permissions=array_remove(permissions,'external_refunds.view') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`
					}
				}
				if _, err = tx.Exec(ctx, statement, staffID); err != nil {
					t.Fatal(err)
				}
				result := make(chan *httptest.ResponseRecorder, 1)
				go func() { result <- request(f.h, method, path, input, staff, "order-refund-lock-write") }()
				for {
					var waiting bool
					if err = f.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						t.Log("observed pg_blocking_pids dependency before revocation committed")
						break
					}
					select {
					case w := <-result:
						t.Fatalf("request did not wait: status=%d", w.Code)
					case <-ctx.Done():
						t.Fatal("no actual lock wait")
					case <-time.After(10 * time.Millisecond):
					}
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case w := <-result:
					expect(t, w, expected)
				case <-ctx.Done():
					t.Fatal("request stuck after revocation commit")
				}
				orderExternalRefundUnchanged(t, before, f.snapshot(t))
			})
		}
	}
}

func TestOrderExternalRefundHTTPAtomicFailuresAndConcurrentConfirmation(t *testing.T) {
	for _, stage := range []string{"create_audit", "confirm_stock", "confirm_audit"} {
		t.Run(stage, func(t *testing.T) {
			f := newOrderExternalRefundHTTPFixture(t)
			out := f.paid(t, "admin", "order-refund-fault-order")
			_, reviewer := externalRefundActor(t, f.db, f.h, "order-refund-fault-reviewer", []string{"external_refunds.review", "external_refunds.order_reverse"})
			input := f.input(t, out, "order-refund-fault-proof")
			path := orderExternalRefundPath(out.invoice)
			cookie := f.root
			if stage != "create_audit" {
				created := request(f.h, "POST", path, input, f.root, "order-refund-fault-create")
				expect(t, created, 201)
				path = fmt.Sprintf("%s/%.0f/confirm", path, data(t, created)["refund"].(map[string]any)["id"])
				input = map[string]any{"expected_version": 1, "note": "isolated independently reviewed"}
				cookie = reviewer
			}
			before := f.snapshot(t)
			sql := `CREATE FUNCTION order_refund_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE '%external_refund%' THEN RAISE EXCEPTION 'isolated refund audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER order_refund_fault BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION order_refund_fault()`
			relation := "audit_events"
			if stage == "confirm_stock" {
				sql = `CREATE FUNCTION order_refund_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'isolated stock failure'; END $$; CREATE TRIGGER order_refund_fault BEFORE UPDATE OF stock ON products FOR EACH ROW EXECUTE FUNCTION order_refund_fault()`
				relation = "products"
			}
			if _, err := f.db.Pool.Exec(t.Context(), sql); err != nil {
				t.Fatal(err)
			}
			response := request(f.h, "POST", path, input, cookie, "order-refund-fault-retry")
			if response.Code < 400 {
				t.Fatal("injected fault accepted")
			}
			orderExternalRefundUnchanged(t, before, f.snapshot(t))
			if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER order_refund_fault ON "+relation); err != nil {
				t.Fatal(err)
			}
			expect(t, request(f.h, "POST", path, input, cookie, "order-refund-fault-retry"), 201)
			if stage != "create_audit" {
				f.assertConfirmed(t, out)
			} else {
				f.assertPaidPending(t, out)
			}
		})
	}
	t.Run("concurrent_http_create_confirm", func(t *testing.T) {
		f := newOrderExternalRefundHTTPFixture(t)
		out := f.paid(t, "cart", "order-refund-race-order")
		_, reviewer := externalRefundActor(t, f.db, f.h, "order-refund-race-reviewer", []string{"external_refunds.view", "external_refunds.review", "external_refunds.order_reverse"})
		input := f.input(t, out, "order-refund-race-proof")
		path := orderExternalRefundPath(out.invoice)
		run := func(path, cookie, key string, body map[string]any) {
			t.Helper()
			start := make(chan struct{})
			results := make(chan int, 2)
			for i := 0; i < 2; i++ {
				go func(i int) {
					<-start
					results <- request(f.h, "POST", path, body, cookie, fmt.Sprintf("%s-%d", key, i)).Code
				}(i)
			}
			close(start)
			statuses := map[int]int{}
			for i := 0; i < 2; i++ {
				select {
				case code := <-results:
					statuses[code]++
				case <-time.After(10 * time.Second):
					t.Fatal("HTTP concurrency did not finish")
				}
			}
			if statuses[201] != 1 || statuses[409] != 1 {
				t.Fatalf("concurrent statuses %v", statuses)
			}
		}
		run(path, f.root, "order-refund-race-create", input)
		page := request(f.h, "GET", path, nil, f.root, "")
		expect(t, page, 200)
		items := data(t, page)["items"].([]any)
		if len(items) != 1 {
			t.Fatal("concurrent create made multiple refunds")
		}
		confirm := fmt.Sprintf("%s/%.0f/confirm", path, items[0].(map[string]any)["id"])
		run(confirm, reviewer, "order-refund-race-confirm", map[string]any{"expected_version": 1, "note": "independent concurrent review"})
		f.assertConfirmed(t, out)
	})
}

func TestOrderExternalRefundHTTPConservativeRejections(t *testing.T) {
	for _, scenario := range []string{"partial_amount", "approved_with_create_jobs", "normal_manual_invoice", "topup_without_order", "no_order_versions"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOrderExternalRefundHTTPFixture(t)
			out := f.paid(t, "cart", "order-refund-refuse-order")
			input := f.input(t, out, "order-refund-refuse-proof")
			code := "external_refund_order_not_eligible"
			switch scenario {
			case "partial_amount":
				input["amount_cents"] = out.amount - 1
				code = "external_refund_amount_mismatch"
			case "approved_with_create_jobs":
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/review", out.order), map[string]any{"decision": "approved", "note": "ordinary approval before refund", "expected_version": orderVersion(t, f.db, out.order)}, f.root, "order-refund-refuse-approve"), 201)
				c := f.context(t, out, f.root)
				if c["eligible"] != false {
					t.Fatal("approved queued order exposed eligible")
				}
				input["expected_order_version"] = c["order_version"]
				code = "external_refund_delivery_started"
			case "normal_manual_invoice":
				invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "order-refund-refuse-manual")
				c := f.context(t, orderExternalRefundHTTPOrder{invoice: invoice}, f.root)
				if c["eligible"] != false || c["order_id"] != float64(0) || len(c["services"].([]any)) != 0 || c["order_reversal"] != nil {
					t.Fatal("manual invoice was presented as order")
				}
				return
			case "topup_without_order":
				if _, err := f.db.Pool.Exec(t.Context(), `UPDATE finance_settings SET topup_enabled=true,min_topup_cents=1,max_topup_cents=10000,max_balance_cents=0,require_active_order=false`); err != nil {
					t.Fatal(err)
				}
				options := request(f.h, "GET", "/api/v1/balance/topup-options", nil, f.customer, "")
				expect(t, options, 200)
				created := request(f.h, "POST", "/api/v1/balance/topups", map[string]any{"amount_cents": 100, "expected_version": data(t, options)["version"]}, f.customer, "order-refund-refuse-topup")
				expect(t, created, 201)
				c := f.context(t, orderExternalRefundHTTPOrder{invoice: int64(data(t, created)["invoice_id"].(float64))}, f.root)
				if c["eligible"] != false || c["order_id"] != float64(0) || len(c["services"].([]any)) != 0 || c["order_reversal"] != nil {
					t.Fatal("topup invoice was presented as order")
				}
				return
			case "no_order_versions":
				delete(input, "expected_order_version")
				delete(input, "expected_service_versions")
				code = "version_conflict"
			}
			before := f.snapshot(t)
			response := request(f.h, "POST", orderExternalRefundPath(out.invoice), input, f.root, "order-refund-refuse-create")
			expect(t, response, 409)
			// Eligibility can reject an approved order at either its paid/review gate or
			// delivery-history gate; both must stay conservative and leave no facts.
			if scenario == "approved_with_create_jobs" {
				actual := errorData(t, response)["code"]
				if actual != "external_refund_order_not_eligible" && actual != code {
					t.Fatalf("unexpected eligibility code %v", actual)
				}
			} else if errorData(t, response)["code"] != code {
				t.Fatalf("unexpected rejection %v want %s", errorData(t, response)["code"], code)
			}
			orderExternalRefundUnchanged(t, before, f.snapshot(t))
		})
	}
}
