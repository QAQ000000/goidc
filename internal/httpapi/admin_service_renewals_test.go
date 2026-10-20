package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
)

type adminRenewalFixture struct {
	upgradeFixture
	actor int64
	staff string
}

func newAdminRenewalFixture(t *testing.T, manual bool) adminRenewalFixture {
	t.Helper()
	f := configurationUpgradeFixture(t, manual)
	actor, staff := user(t, f.db, f.h, "admin-renewal-staff", "admin")
	contentRole(t, f.db, actor, "services.view", "services.renew", "invoices.view", "services.renew.pay_balance")
	return adminRenewalFixture{f, actor, staff}
}

func adminRenewalPath(service int64) string {
	return fmt.Sprintf("/api/v1/admin/services/%d", service)
}

func (f adminRenewalFixture) version(t *testing.T) int64 {
	t.Helper()
	var version int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT version FROM services WHERE id=$1", f.sid).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func (f adminRenewalFixture) renewalQuote(t *testing.T) map[string]any {
	t.Helper()
	w := request(f.h, "POST", adminRenewalPath(f.sid)+"/renewal-quotes", map[string]any{"expected_version": f.version(t)}, f.staff, "")
	expect(t, w, 200)
	q := data(t, w)
	if (q["can_create"] == true && (q["quote_hash"] == "" || q["quote_hash"] == nil)) || q["service_id"] != float64(f.sid) || q["customer_id"] != float64(f.uid) {
		t.Fatal("incomplete administrator renewal quote", w.Body.String())
	}
	return q
}

func renewalCreateBody(q map[string]any) map[string]any {
	return map[string]any{"expected_version": q["service_version"], "quote_hash": q["quote_hash"], "reason": "客户工单授权代续费"}
}

func (f adminRenewalFixture) createRenewal(t *testing.T, key string) map[string]any {
	t.Helper()
	q := f.renewalQuote(t)
	w := request(f.h, "POST", adminRenewalPath(f.sid)+"/renewals", renewalCreateBody(q), f.staff, key)
	expect(t, w, 201)
	return data(t, w)
}

func renewalPayPath(service int64, invoice map[string]any) string {
	return fmt.Sprintf("%s/renewals/%d/pay", adminRenewalPath(service), int64(invoice["invoice_id"].(float64)))
}

func renewalPayBody(invoice map[string]any) map[string]any {
	return map[string]any{"expected_version": invoice["version"], "amount_cents": invoice["amount_cents"], "reason": "客户已确认使用账户余额"}
}

type adminRenewalSnapshot struct {
	balance, serviceVersion, invoices, invoiceItems, ledger, receipts, notifications, effects, jobs, audits int64
	contractPrice, discountsUsed                                                                            int64
	due                                                                                                     time.Time
}

func (f adminRenewalFixture) snapshot(t *testing.T) adminRenewalSnapshot {
	t.Helper()
	var out adminRenewalSnapshot
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT u.balance_cents,s.version,s.next_due_at,
 (SELECT count(*) FROM invoices WHERE user_id=u.id),
 (SELECT count(*) FROM balance_ledger WHERE user_id=u.id),
 (SELECT count(*) FROM mutation_operations),
 (SELECT count(*) FROM notification_events WHERE user_id=u.id),
 (SELECT count(*) FROM service_renewal_effects WHERE service_id=s.id),
 (SELECT count(*) FROM provision_jobs WHERE service_id=s.id),
 (SELECT count(*) FROM audit_events WHERE action IN ('service.renewal.created','invoice.paid')),
 (SELECT count(*) FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE i.user_id=u.id),
 coalesce(s.recurring_price_cents,-1),s.renewal_discounts_used
 FROM users u JOIN services s ON s.user_id=u.id WHERE u.id=$1 AND s.id=$2`, f.uid, f.sid).
		Scan(&out.balance, &out.serviceVersion, &out.due, &out.invoices, &out.ledger, &out.receipts, &out.notifications, &out.effects, &out.jobs, &out.audits, &out.invoiceItems, &out.contractPrice, &out.discountsUsed); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAdminServiceRenewalZeroPriceRequiresExplicitPayment(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual=%t", manual), func(t *testing.T) {
			f := newAdminRenewalFixture(t, manual)
			if _, err := f.db.Pool.Exec(t.Context(), `UPDATE services SET renewal_discount_percent=100,
 renewal_promo_code='ADMIN-ZERO-ONCE',renewal_discount_limit=1,renewal_discounts_used=0,version=version+1 WHERE id=$1`, f.sid); err != nil {
				t.Fatal(err)
			}
			before := f.snapshot(t)
			q := f.renewalQuote(t)
			if q["base_cents"] != float64(2500) || q["discount_cents"] != float64(2500) || q["amount_cents"] != float64(0) {
				t.Fatal("zero-price benefit did not retain its positive contract base", q)
			}
			f.unchanged(t, before)
			w := request(f.h, "POST", adminRenewalPath(f.sid)+"/renewals", renewalCreateBody(q), f.staff, "admin-renewal-zero-create")
			expect(t, w, 201)
			invoice := data(t, w)
			if invoice["status"] != "unpaid" || invoice["amount_cents"] != float64(0) {
				t.Fatal("zero-price creation must remain an explicit unpaid invoice", w.Body.String())
			}
			created := f.snapshot(t)
			if created.balance != before.balance || created.ledger != before.ledger || created.effects != before.effects || created.jobs != before.jobs || created.notifications != before.notifications || created.serviceVersion != before.serviceVersion || !created.due.Equal(before.due) || created.contractPrice != 2500 || created.discountsUsed != 0 || created.invoices != before.invoices+1 || created.invoiceItems != before.invoiceItems+1 {
				t.Fatal("zero-price creation settled or altered the service contract", before, created)
			}
			// Missing amount is not consent to a zero-price settlement.
			invalid := renewalPayBody(invoice)
			delete(invalid, "amount_cents")
			expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), invalid, f.staff, "admin-renewal-zero-pay"), 400)
			f.unchanged(t, created)
			w = request(f.h, "POST", renewalPayPath(f.sid, invoice), renewalPayBody(invoice), f.staff, "admin-renewal-zero-pay")
			expect(t, w, 201)
			if data(t, w)["status"] != "paid" || data(t, w)["renewal_status"] != "completed" {
				t.Fatal("explicit zero-price settlement did not complete", w.Body.String())
			}
			after := f.snapshot(t)
			if after.balance != before.balance || after.effects != before.effects+1 || !after.due.Equal(before.due.AddDate(0, 1, 0)) || after.contractPrice != 2500 || after.discountsUsed != 1 {
				t.Fatal("zero-price settlement cleared the contract or failed to consume exactly one benefit", before, after)
			}
			next := f.renewalQuote(t)
			if next["base_cents"] != float64(2500) || next["discount_cents"] != float64(0) || next["amount_cents"] != float64(2500) {
				t.Fatal("one free period changed future contracted renewals", next)
			}
			expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), renewalPayBody(invoice), f.staff, "admin-renewal-zero-pay"), 200)
			f.unchanged(t, after)
		})
	}
}

func (f adminRenewalFixture) unchanged(t *testing.T, before adminRenewalSnapshot) {
	t.Helper()
	if after := f.snapshot(t); after != before {
		t.Fatalf("rejected/read request changed financial or service facts: before=%+v after=%+v", before, after)
	}
}

func TestAdminServiceRenewalLifecycleAndReadOnlyQuote(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual=%t", manual), func(t *testing.T) {
			f := newAdminRenewalFixture(t, manual)
			before := f.snapshot(t)
			q := f.renewalQuote(t)
			if q["amount_cents"] != float64(2500) || q["can_create"] != true || q["service_version"] != float64(before.serviceVersion) {
				t.Fatal("wrong contracted renewal quote", q)
			}
			f.unchanged(t, before)
			body := renewalCreateBody(q)
			path := adminRenewalPath(f.sid) + "/renewals"
			w := request(f.h, "POST", path, body, f.staff, "admin-renewal-create")
			expect(t, w, 201)
			invoice := data(t, w)
			if invoice["customer_id"] != float64(f.uid) || invoice["service_id"] != float64(f.sid) || invoice["status"] != "unpaid" || invoice["amount_cents"] != float64(2500) {
				t.Fatal("creation must return an unpaid customer invoice", w.Body.String())
			}
			created := f.snapshot(t)
			if created.balance != before.balance || created.serviceVersion != before.serviceVersion || !created.due.Equal(before.due) || created.effects != before.effects || created.invoices != before.invoices+1 || created.ledger != before.ledger {
				t.Fatal("creating an unpaid renewal changed balance or entitlement", before, created)
			}
			pending := f.renewalQuote(t)
			if pending["can_create"] != false || pending["pending_invoice_id"] != invoice["invoice_id"] {
				t.Fatal("pending invoice is not surfaced by the quote", pending)
			}
			f.unchanged(t, created)
			payBody, payPath := renewalPayBody(invoice), renewalPayPath(f.sid, invoice)
			w = request(f.h, "POST", payPath, payBody, f.staff, "admin-renewal-pay")
			expect(t, w, 201)
			paid := data(t, w)
			if paid["status"] != "paid" || paid["renewal_status"] != "completed" || paid["customer_id"] != float64(f.uid) || paid["service_id"] != float64(f.sid) {
				t.Fatal("local renewal payment did not finish entitlement", w.Body.String())
			}
			after := f.snapshot(t)
			if after.balance != before.balance-2500 || after.ledger != before.ledger+1 || after.effects != before.effects+1 || !after.due.Equal(before.due.AddDate(0, 1, 0)) || after.jobs != before.jobs {
				t.Fatal("renewal did not settle exactly one period and debit", before, after)
			}
			var actor, count int64
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT actor_id FROM audit_events WHERE action='invoice.paid' AND entity_id=$1 ORDER BY id DESC LIMIT 1`, fmt.Sprint(int64(invoice["invoice_id"].(float64)))).Scan(&actor); err != nil || actor != f.actor {
				t.Fatal("administrator payment lost the real administrator actor", actor, err)
			}
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM notification_events WHERE invoice_id=$1 AND kind='invoice.paid'", int64(invoice["invoice_id"].(float64))).Scan(&count); err != nil || count != 1 {
				t.Fatal("payment notification is not exactly once", count, err)
			}
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM notification_events WHERE kind='service.renewed' AND payload->>'service_id'=$1", fmt.Sprint(f.sid)).Scan(&count); err != nil || count != 1 {
				t.Fatal("renewal notification is not exactly once", count, err)
			}
			w = request(f.h, "POST", path, body, f.staff, "admin-renewal-create")
			expect(t, w, 200)
			if data(t, w)["replayed"] != true || data(t, w)["invoice_id"] != invoice["invoice_id"] {
				t.Fatal("creation retry lost its original invoice", w.Body.String())
			}
			w = request(f.h, "POST", payPath, payBody, f.staff, "admin-renewal-pay")
			expect(t, w, 200)
			if data(t, w)["replayed"] != true || data(t, w)["balance_cents"] != paid["balance_cents"] {
				t.Fatal("payment retry did not preserve the original receipt", w.Body.String())
			}
			f.unchanged(t, after)
		})
	}
}

func TestAdminServiceRenewalIdempotencyCASAndActorIsolation(t *testing.T) {
	f := newAdminRenewalFixture(t, false)
	q := f.renewalQuote(t)
	body, path := renewalCreateBody(q), adminRenewalPath(f.sid)+"/renewals"
	w := request(f.h, "POST", path, body, f.staff, "admin-renewal-isolation")
	expect(t, w, 201)
	invoice := data(t, w)
	changed := renewalCreateBody(q)
	changed["reason"] = "另一个原因不能覆盖原意图"
	expect(t, request(f.h, "POST", path, changed, f.staff, "admin-renewal-isolation"), 409)
	changed = renewalCreateBody(q)
	changed["expected_version"] = f.version(t) + 1
	expect(t, request(f.h, "POST", path, changed, f.staff, "admin-renewal-stale"), 409)
	// Another actor cannot replay the first actor's receipt for a pending invoice.
	secondID, second := user(t, f.db, f.h, "admin-renewal-second", "admin")
	contentRole(t, f.db, secondID, "services.view", "services.renew", "invoices.view", "services.renew.pay_balance")
	w = request(f.h, "POST", path, body, second, "admin-renewal-isolation")
	expect(t, w, 409)
	before := f.snapshot(t)
	pay := renewalPayBody(invoice)
	pay["amount_cents"] = int64(invoice["amount_cents"].(float64)) + 1
	expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), pay, f.staff, "admin-renewal-bad-price"), 409)
	pay = renewalPayBody(invoice)
	pay["expected_version"] = int64(invoice["version"].(float64)) + 1
	expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), pay, f.staff, "admin-renewal-bad-version"), 409)
	f.unchanged(t, before)
	pay = renewalPayBody(invoice)
	w = request(f.h, "POST", renewalPayPath(f.sid, invoice), pay, f.staff, "admin-renewal-isolation")
	expect(t, w, 201) // Identical key is allowed in a distinct operation scope.
	after := f.snapshot(t)
	pay["reason"] = "不是同一个付款意图"
	expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), pay, f.staff, "admin-renewal-isolation"), 409)
	pay = renewalPayBody(invoice)
	expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), pay, second, "admin-renewal-isolation"), 409)
	f.unchanged(t, after)
	// A stale quotation must not silently pick up a new contract price.
	q = f.renewalQuote(t)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET recurring_price_cents=3000,version=version+1 WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	stale := renewalCreateBody(q)
	stale["expected_version"] = f.version(t)
	before = f.snapshot(t)
	expect(t, request(f.h, "POST", path, stale, f.staff, "admin-renewal-old-quote"), 409)
	f.unchanged(t, before)
}

func TestAdminServiceRenewalStrictInputPermissionsAndScope(t *testing.T) {
	f := newAdminRenewalFixture(t, false)
	quotePath, path := adminRenewalPath(f.sid)+"/renewal-quotes", adminRenewalPath(f.sid)+"/renewals"
	for _, cookie := range []string{"", f.customer, f.other} {
		expect(t, request(f.h, "POST", quotePath, map[string]any{"expected_version": f.version(t)}, cookie, ""), 401)
	}
	q := f.renewalQuote(t)
	before := f.snapshot(t)
	for _, invalid := range []map[string]any{{}, {"expected_version": nil}, {"expected_version": "1"}, {"expected_version": true}, {"expected_version": 1, "price_cents": 0}} {
		expect(t, request(f.h, "POST", quotePath, invalid, f.staff, ""), 400)
	}
	for _, field := range []string{"expected_version", "quote_hash", "reason"} {
		invalid := renewalCreateBody(q)
		delete(invalid, field)
		expect(t, request(f.h, "POST", path, invalid, f.staff, "admin-renewal-invalid"), 400)
	}
	invalid := renewalCreateBody(q)
	invalid["amount_cents"] = 1
	expect(t, request(f.h, "POST", path, invalid, f.staff, "admin-renewal-invalid"), 400)
	expect(t, request(f.h, "POST", path, renewalCreateBody(q), f.staff, ""), 400)
	duplicate := httptest.NewRequest("POST", path, strings.NewReader(fmt.Sprintf(`{"expected_version":%v,"quote_hash":%q,"reason":"客户工单授权代续费"}`, q["service_version"], q["quote_hash"])))
	duplicate.Header.Set("Content-Type", "application/json")
	duplicate.Header.Set("Cookie", f.staff)
	duplicate.Header.Set("Origin", "http://localhost:3088")
	duplicate.Header.Set("X-Requested-With", "zjmfgo")
	duplicate.Header.Add("Idempotency-Key", "admin-renewal-duplicate-a")
	duplicate.Header.Add("Idempotency-Key", "admin-renewal-duplicate-b")
	response := httptest.NewRecorder()
	f.h.ServeHTTP(response, duplicate)
	expect(t, response, 400)
	contentRole(t, f.db, f.actor, "services.view", "services.manage", "invoices.view")
	expect(t, request(f.h, "POST", quotePath, map[string]any{"expected_version": f.version(t)}, f.staff, ""), 403)
	expect(t, request(f.h, "POST", path, renewalCreateBody(q), f.staff, "admin-renewal-denied"), 403)
	contentRole(t, f.db, f.actor, "services.renew")
	expect(t, request(f.h, "POST", quotePath, map[string]any{"expected_version": f.version(t)}, f.staff, ""), 403)
	contentRole(t, f.db, f.actor, "services.view", "services.renew", "invoices.view")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", quotePath, map[string]any{"expected_version": f.version(t)}, f.staff, ""), 404)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", f.uid, f.actor); err != nil {
		t.Fatal(err)
	}
	// Scope fixture changes the customer but none of the service/financial facts.
	f.unchanged(t, before)
	invoice := f.createRenewal(t, "admin-renewal-scoped")
	payPath, pay := renewalPayPath(f.sid, invoice), renewalPayBody(invoice)
	expect(t, request(f.h, "POST", payPath, pay, f.staff, "admin-renewal-denied-pay"), 403)
	contentRole(t, f.db, f.actor, "services.view", "services.renew.pay_balance")
	expect(t, request(f.h, "POST", payPath, pay, f.staff, "admin-renewal-denied-pay"), 403)
	contentRole(t, f.db, f.actor, "services.view", "services.renew.pay_balance", "invoices.view")
	for _, field := range []string{"expected_version", "amount_cents", "reason"} {
		invalid := renewalPayBody(invoice)
		delete(invalid, field)
		expect(t, request(f.h, "POST", payPath, invalid, f.staff, "admin-renewal-invalid-pay"), 400)
	}
	invalid = renewalPayBody(invoice)
	invalid["amount_cents"] = nil
	expect(t, request(f.h, "POST", payPath, invalid, f.staff, "admin-renewal-invalid-pay"), 400)
	// Existing payment/manage rights do not imply explicit permission to spend a customer's balance.
	contentRole(t, f.db, f.actor, "services.view", "services.manage", "invoices.view", "invoices.manage")
	expect(t, request(f.h, "POST", payPath, pay, f.staff, "admin-renewal-denied-pay"), 403)
	contentRole(t, f.db, f.actor, "services.view", "services.renew.pay_balance", "invoices.view")
	// Invoices from the original purchase and unrelated/manual billing are not renewals.
	var original int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM invoices WHERE order_id=$1", f.orderID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{original, manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "admin-renewal-manual-bill")} {
		var amount, version int64
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT amount_cents,version FROM invoices WHERE id=$1", id).Scan(&amount, &version); err != nil {
			t.Fatal(err)
		}
		expect(t, request(f.h, "POST", fmt.Sprintf("%s/renewals/%d/pay", adminRenewalPath(f.sid), id), map[string]any{"expected_version": version, "amount_cents": amount, "reason": "不可从代续费支付其他账单"}, f.staff, "admin-renewal-wrong-bill"), 409)
	}
	expect(t, request(f.h, "POST", renewalPayPath(f.sid+999999, invoice), pay, f.staff, "admin-renewal-wrong-service"), 404)
}

func TestAdminServiceRenewalAuditAndReceiptRollback(t *testing.T) {
	f := newAdminRenewalFixture(t, false)
	q, path := f.renewalQuote(t), adminRenewalPath(f.sid)+"/renewals"
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_admin_renewal_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.action IN ('service.renewal.created','invoice.paid','service.renewal.paid') THEN RAISE EXCEPTION 'administrator renewal audit rejected'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_admin_renewal_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_admin_renewal_audit();
 CREATE FUNCTION reject_admin_renewal_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.scope IN ('admin:service.renewal.create','admin:service.renewal.pay') THEN RAISE EXCEPTION 'administrator renewal receipt rejected'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_admin_renewal_receipt BEFORE INSERT ON mutation_operations FOR EACH ROW EXECUTE FUNCTION reject_admin_renewal_receipt();
 ALTER TABLE mutation_operations DISABLE TRIGGER reject_admin_renewal_receipt`); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	expect(t, request(f.h, "POST", path, renewalCreateBody(q), f.staff, "admin-renewal-rollback-create"), 500)
	f.unchanged(t, before)
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_admin_renewal_audit; ALTER TABLE mutation_operations ENABLE TRIGGER reject_admin_renewal_receipt"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, renewalCreateBody(q), f.staff, "admin-renewal-rollback-create"), 500)
	f.unchanged(t, before)
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE mutation_operations DISABLE TRIGGER reject_admin_renewal_receipt"); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", path, renewalCreateBody(q), f.staff, "admin-renewal-rollback-create")
	expect(t, w, 201)
	invoice := data(t, w)
	before = f.snapshot(t)
	for _, table := range []string{"audit_events", "mutation_operations"} {
		trigger := "reject_admin_renewal_audit"
		if table == "mutation_operations" {
			trigger = "reject_admin_renewal_receipt"
		}
		if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE "+table+" ENABLE TRIGGER "+trigger); err != nil {
			t.Fatal(err)
		}
		expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), renewalPayBody(invoice), f.staff, "admin-renewal-rollback-pay"), 500)
		f.unchanged(t, before)
		if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE "+table+" DISABLE TRIGGER "+trigger); err != nil {
			t.Fatal(err)
		}
	}
	expect(t, request(f.h, "POST", renewalPayPath(f.sid, invoice), renewalPayBody(invoice), f.staff, "admin-renewal-rollback-pay"), 201)
}

func TestAdminServiceRenewalConcurrentCreateAndCustomerPayment(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_key=%t", sameKey), func(t *testing.T) {
			f := newAdminRenewalFixture(t, false)
			body := renewalCreateBody(f.renewalQuote(t))
			start, done := make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
			var wg sync.WaitGroup
			for i := range 2 {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					key := "admin-renewal-concurrent-create"
					if !sameKey {
						key += fmt.Sprint(i)
					}
					done <- request(f.h, "POST", adminRenewalPath(f.sid)+"/renewals", body, f.staff, key)
				}(i)
			}
			close(start)
			wg.Wait()
			close(done)
			counts := map[int]int{}
			var invoice map[string]any
			for w := range done {
				counts[w.Code]++
				if w.Code == 201 {
					invoice = data(t, w)
				} else if (sameKey && w.Code != 200) || (!sameKey && w.Code != 409) {
					t.Fatal("unexpected concurrent creation result", w.Code, w.Body.String())
				}
			}
			if counts[201] != 1 || (sameKey && counts[200] != 1) || (!sameKey && counts[409] != 1) {
				t.Fatal("concurrent renewal invoice duplicated", counts)
			}
			before := f.snapshot(t)
			start, done = make(chan struct{}), make(chan *httptest.ResponseRecorder, 2)
			go func() {
				<-start
				done <- request(f.h, "POST", renewalPayPath(f.sid, invoice), renewalPayBody(invoice), f.staff, "shared-customer-admin-key")
			}()
			go func() {
				<-start
				done <- request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(invoice["invoice_id"].(float64))), map[string]any{}, f.customer, "shared-customer-admin-key")
			}()
			close(start)
			counts = map[int]int{}
			for range 2 {
				w := awaitBalanceResponse(t, done)
				if w.Code != 201 && w.Code != 409 {
					t.Fatal("unexpected administrator/customer payment race", w.Code, w.Body.String())
				}
				counts[w.Code]++
			}
			if counts[201] != 1 || counts[409] != 1 {
				t.Fatal("customer/admin payment did not elect one winner", counts)
			}
			after := f.snapshot(t)
			if after.balance != before.balance-2500 || after.ledger != before.ledger+1 || after.effects != before.effects+1 || !after.due.Equal(before.due.AddDate(0, 1, 0)) {
				t.Fatal("concurrent payment duplicated money or entitlement", before, after)
			}
		})
	}
}

func TestAdminServiceRenewalConcurrentPaymentReplaysOneResult(t *testing.T) {
	f := newAdminRenewalFixture(t, false)
	invoice := f.createRenewal(t, "admin-renewal-concurrent-payment-invoice")
	before := f.snapshot(t)
	start := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			<-start
			done <- request(f.h, "POST", renewalPayPath(f.sid, invoice), renewalPayBody(invoice), f.staff, "admin-renewal-concurrent-payment")
		}()
	}
	close(start)
	counts := map[int]int{}
	var balance any
	for range 2 {
		w := awaitBalanceResponse(t, done)
		if w.Code != 200 && w.Code != 201 {
			t.Fatal("same-key payment did not replay", w.Code, w.Body.String())
		}
		counts[w.Code]++
		result := data(t, w)
		if result["invoice_id"] != invoice["invoice_id"] || result["renewal_status"] != "completed" {
			t.Fatal("concurrent payment returned a different business result", w.Body.String())
		}
		if balance != nil && balance != result["balance_cents"] {
			t.Fatal("concurrent replay rewrote its committed balance", balance, result)
		}
		balance = result["balance_cents"]
	}
	if counts[201] != 1 || counts[200] != 1 {
		t.Fatal("same-key payment did not have one first writer", counts)
	}
	after := f.snapshot(t)
	if after.balance != before.balance-2500 || after.effects != before.effects+1 || after.ledger != before.ledger+1 || !after.due.Equal(before.due.AddDate(0, 1, 0)) {
		t.Fatal("same-key payment duplicated effects", before, after)
	}
}

func TestAdminServiceRenewalPaymentBusinessFences(t *testing.T) {
	for _, scenario := range []string{"insufficient_balance", "disabled_customer", "online_pending", "lifecycle_pending", "approved_cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			db, h := testServerConfig(t, func(cfg *config.Config) {
				epay := epayTestConfig()
				cfg.EpayAPIURL, cfg.EpayPID, cfg.EpayKey, cfg.EpayTypes = epay.EpayAPIURL, epay.EpayPID, epay.EpayKey, epay.EpayTypes
			})
			base := setupConfigurationUpgradeFixture(t, db, h, false)
			actor, staff := user(t, db, h, "admin-renewal-fence-staff", "admin")
			f := adminRenewalFixture{base, actor, staff}
			invoice := f.createRenewal(t, "admin-renewal-fence-create")
			var err error
			switch scenario {
			case "insufficient_balance":
				_, err = db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=0 WHERE id=$1", f.uid)
			case "disabled_customer":
				_, err = db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", f.uid)
			case "online_pending":
				w := request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", int64(invoice["invoice_id"].(float64))), map[string]any{"provider": "epay_alipay"}, f.customer, "admin-renewal-fence-online")
				expect(t, w, 201)
			case "lifecycle_pending":
				expect(t, request(h, "POST", adminRenewalPath(f.sid)+"/actions", map[string]any{"action": "suspend"}, f.root, "admin-renewal-fence-suspend"), 202)
			case "approved_cancellation":
				var rootID int64
				if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM administrators WHERE role='super_admin'").Scan(&rootID); err != nil {
					t.Fatal(err)
				}
				// This legacy historical state remains protected even though new customer requests are retired.
				var cancellation store.CancellationRequest
				cancellation, err = db.RequestServiceCancellation(t.Context(), f.uid, f.sid, "end_of_billing", "历史已批准取消", "admin-renewal-fence-cancel")
				if err == nil {
					_, err = db.ResolveServiceCancellation(t.Context(), rootID, cancellation.ID, "approved", "历史取消权益核对完成")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := f.snapshot(t)
			w := request(h, "POST", renewalPayPath(f.sid, invoice), renewalPayBody(invoice), f.staff, "admin-renewal-fence-pay")
			if w.Code != 409 && !(scenario == "disabled_customer" && w.Code == 404) {
				t.Fatal("business fence did not reject payment", scenario, w.Code, w.Body.String())
			}
			f.unchanged(t, before)
			var status string
			if err = db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", int64(invoice["invoice_id"].(float64))).Scan(&status); err != nil || status != "unpaid" {
				t.Fatal("rejected payment did not retain original unpaid invoice", status, err)
			}
		})
	}
}

// Both session and API-key requests must be rechecked after a service wait.
// Natural expiration is different from revocation: row locks cannot stop time.
func TestAdminServiceRenewalWaitingCredentialAndNaturalExpiry(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		for _, expiry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expiry=%t", credential, expiry), func(t *testing.T) {
				f := newAdminRenewalFixture(t, false)
				body := renewalCreateBody(f.renewalQuote(t))
				secret := "zjmfgo_" + strings.Repeat("d", 64)
				hash := tokenHash(cookieValue(f.staff))
				table, column := "sessions", "token_hash"
				if credential == "api_key" {
					hash, table, column = tokenHash(secret), "api_keys", "secret_hash"
					if _, err := f.db.Pool.Exec(t.Context(), "INSERT INTO api_keys(user_id,name,prefix,secret_hash) VALUES($1,'admin-renewal-wait','zjmfgo_1234567890123456',$2)", f.actor, hash); err != nil {
						t.Fatal(err)
					}
				}
				if expiry {
					if _, err := f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '2 seconds' WHERE "+column+"=$1", hash); err != nil {
						t.Fatal(err)
					}
				}
				block, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer block.Rollback(context.Background())
				if _, err = block.Exec(t.Context(), "SELECT id FROM services WHERE id=$1 FOR UPDATE", f.sid); err != nil {
					t.Fatal(err)
				}
				before := f.snapshot(t)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					if credential == "api_key" {
						done <- requestFrom(f.h, "POST", adminRenewalPath(f.sid)+"/renewals", body, "192.0.2.1:1234", "", map[string]string{"Authorization": "Bearer " + secret, "Idempotency-Key": "admin-renewal-wait-create"})
					} else {
						done <- request(f.h, "POST", adminRenewalPath(f.sid)+"/renewals", body, f.staff, "admin-renewal-wait-create")
					}
				}()
				waitUploadBlockedBy(t, f.db, block.Conn().PgConn().PID())
				if expiry {
					deadline := time.Now().Add(5 * time.Second)
					for {
						var expired bool
						if err = f.db.Pool.QueryRow(t.Context(), "SELECT clock_timestamp()>=expires_at FROM "+table+" WHERE "+column+"=$1", hash).Scan(&expired); err != nil {
							t.Fatal(err)
						}
						if expired {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("credential did not expire while business row was blocked")
						}
						time.Sleep(20 * time.Millisecond)
					}
				} else if credential == "session" {
					_, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", hash)
				} else {
					_, err = f.db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1", hash)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = block.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), 401)
				f.unchanged(t, before)
			})
		}
	}
}

func TestAdminServiceRenewalReceiptWaitRechecksRevokedCredential(t *testing.T) {
	for _, operation := range []string{"create", "pay"} {
		for _, credential := range []string{"session", "api_key"} {
			t.Run(operation+"/"+credential, func(t *testing.T) {
				f := newAdminRenewalFixture(t, false)
				body := renewalCreateBody(f.renewalQuote(t))
				path := adminRenewalPath(f.sid) + "/renewals"
				if operation == "pay" {
					invoice := f.createRenewal(t, "admin-renewal-receipt-invoice")
					path, body = renewalPayPath(f.sid, invoice), renewalPayBody(invoice)
				}
				secret := "zjmfgo_" + strings.Repeat("e", 64)
				if credential == "api_key" {
					if _, err := f.db.Pool.Exec(t.Context(), "INSERT INTO api_keys(user_id,name,prefix,secret_hash) VALUES($1,'admin-renewal-receipt','zjmfgo_1234567890123456',$2)", f.actor, tokenHash(secret)); err != nil {
						t.Fatal(err)
					}
				}
				key := "admin-renewal-receipt-wait"
				block, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer block.Rollback(context.Background())
				if _, err = block.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("mutation:%d:admin:service.renewal.%s:%s", f.actor, operation, key)); err != nil {
					t.Fatal(err)
				}
				before := f.snapshot(t)
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					if credential == "api_key" {
						done <- requestFrom(f.h, "POST", path, body, "192.0.2.1:1234", "", map[string]string{"Authorization": "Bearer " + secret, "Idempotency-Key": key})
					} else {
						done <- request(f.h, "POST", path, body, f.staff, key)
					}
				}()
				waitUploadBlockedBy(t, f.db, block.Conn().PgConn().PID())
				if credential == "api_key" {
					_, err = f.db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1", tokenHash(secret))
				} else {
					_, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.staff)))
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = block.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), 401)
				f.unchanged(t, before)
			})
		}
	}
}

func TestAdminServiceRenewalPermissionWaitDoesNotRestoreRevokedAuthority(t *testing.T) {
	for _, operation := range []string{"quote", "create", "pay"} {
		t.Run(operation, func(t *testing.T) {
			f := newAdminRenewalFixture(t, false)
			body := renewalCreateBody(f.renewalQuote(t))
			path := adminRenewalPath(f.sid) + "/renewals"
			if operation == "quote" {
				path, body = adminRenewalPath(f.sid)+"/renewal-quotes", map[string]any{"expected_version": f.version(t)}
			} else if operation == "pay" {
				invoice := f.createRenewal(t, "admin-renewal-permission-invoice")
				path, body = renewalPayPath(f.sid, invoice), renewalPayBody(invoice)
			}
			block, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer block.Rollback(context.Background())
			// HTTP authentication sees the committed grants. The Store must wait
			// for this real grant writer and observe its committed revocation.
			if _, err = block.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view','invoices.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor); err != nil {
				t.Fatal(err)
			}
			before := f.snapshot(t)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- request(f.h, "POST", path, body, f.staff, "admin-renewal-permission-wait")
			}()
			waitUploadBlockedBy(t, f.db, block.Conn().PgConn().PID())
			if err = block.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 403)
			f.unchanged(t, before)
		})
	}
}

// Gate late inserts instead of the initial replay lock: money, entitlement and
// audit rows may already have been written, but they must all remain atomic
// with the final clock-based credential check.
func TestAdminServiceRenewalLateWaitNaturalExpiryRollsBack(t *testing.T) {
	for _, operation := range []string{"create", "pay"} {
		for _, credential := range []string{"session", "api_key"} {
			for _, wait := range []string{"audit", "receipt_insert"} {
				t.Run(operation+"/"+credential+"/"+wait, func(t *testing.T) {
					f := newAdminRenewalFixture(t, false)
					path, body := adminRenewalPath(f.sid)+"/renewals", renewalCreateBody(f.renewalQuote(t))
					var invoice map[string]any
					if operation == "pay" {
						invoice = f.createRenewal(t, "admin-renewal-late-expiry-invoice")
						path, body = renewalPayPath(f.sid, invoice), renewalPayBody(invoice)
					}
					secret, table, column, hash := adminRenewalTestCredential(t, f, credential)
					gate, err := f.db.Pool.Begin(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer gate.Rollback(context.Background())
					if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended(current_schema()||':admin-renewal-late-gate',0))"); err != nil {
						t.Fatal(err)
					}
					trigger := `CREATE FUNCTION wait_admin_renewal_late_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.scope='admin:service.renewal.` + operation + `' THEN
 PERFORM pg_advisory_xact_lock(hashtextextended(TG_TABLE_SCHEMA||':admin-renewal-late-gate',0)); END IF; RETURN NEW; END $$;
 CREATE TRIGGER wait_admin_renewal_late_insert BEFORE INSERT ON mutation_operations FOR EACH ROW EXECUTE FUNCTION wait_admin_renewal_late_insert()`
					if wait == "audit" {
						action := "service.renewal.created"
						if operation == "pay" {
							action = "invoice.paid"
						}
						trigger = `CREATE FUNCTION wait_admin_renewal_late_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.action='` + action + `' THEN
 PERFORM pg_advisory_xact_lock(hashtextextended(TG_TABLE_SCHEMA||':admin-renewal-late-gate',0)); END IF; RETURN NEW; END $$;
 CREATE TRIGGER wait_admin_renewal_late_insert BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_admin_renewal_late_insert()`
					}
					if _, err = f.db.Pool.Exec(t.Context(), trigger); err != nil {
						t.Fatal(err)
					}
					// The late insert already holds the credential row; expiration
					// must advance naturally rather than trying to update its lock.
					if _, err = f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '2 seconds' WHERE "+column+"=$1", hash); err != nil {
						t.Fatal(err)
					}
					before := f.snapshot(t)
					done := make(chan *httptest.ResponseRecorder, 1)
					go func() { done <- adminRenewalCredentialRequest(f, path, body, "admin-renewal-late-expiry", secret) }()
					waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
					waitAdminRenewalCredentialExpiry(t, f.db, table, column, hash)
					if err = gate.Commit(t.Context()); err != nil {
						t.Fatal(err)
					}
					expect(t, awaitBalanceResponse(t, done), 401)
					f.unchanged(t, before)
					if invoice != nil {
						var status string
						var version int64
						if err = f.db.Pool.QueryRow(t.Context(), "SELECT status,version FROM invoices WHERE id=$1", int64(invoice["invoice_id"].(float64))).Scan(&status, &version); err != nil || status != "unpaid" || version != int64(invoice["version"].(float64)) {
							t.Fatal("expired late payment changed the original unpaid invoice", status, version, err)
						}
					}
				})
			}
		}
	}
}

func adminRenewalTestCredential(t *testing.T, f adminRenewalFixture, kind string) (secret, table, column, hash string) {
	t.Helper()
	table, column, hash = "sessions", "token_hash", tokenHash(cookieValue(f.staff))
	if kind == "api_key" {
		secret = "zjmfgo_" + strings.Repeat("f", 64)
		table, column, hash = "api_keys", "secret_hash", tokenHash(secret)
		if _, err := f.db.Pool.Exec(t.Context(), "INSERT INTO api_keys(user_id,name,prefix,secret_hash) VALUES($1,'admin-renewal-concurrency','zjmfgo_1234567890123456',$2)", f.actor, hash); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func adminRenewalCredentialRequest(f adminRenewalFixture, path string, body map[string]any, key, secret string) *httptest.ResponseRecorder {
	if secret != "" {
		return requestFrom(f.h, "POST", path, body, "192.0.2.1:1234", "", map[string]string{"Authorization": "Bearer " + secret, "Idempotency-Key": key})
	}
	return request(f.h, "POST", path, body, f.staff, key)
}

func waitAdminRenewalCredentialExpiry(t *testing.T, db *store.Store, table, column, hash string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	for {
		var expired bool
		if err := db.Pool.QueryRow(ctx, "SELECT clock_timestamp()>=expires_at FROM "+table+" WHERE "+column+"=$1", hash).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("administrator credential did not naturally expire at its late insert wait")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitAdminRenewalRotationChain(t *testing.T, db *store.Store, blocker uint32, renewalFirst bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	for {
		var ready bool
		query := `WITH first_writer AS (
 SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(pid))
)
SELECT EXISTS(SELECT 1 FROM pg_stat_activity a JOIN first_writer w ON w.pid=ANY(pg_blocking_pids(a.pid))
 WHERE a.datname=current_database() AND a.query='SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status=''active'' FOR SHARE')`
		if renewalFirst {
			query = `WITH first_writer AS (
 SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(pid))
)
SELECT EXISTS(SELECT 1 FROM pg_stat_activity a JOIN first_writer w ON w.pid=ANY(pg_blocking_pids(a.pid))
 WHERE a.datname=current_database() AND a.query='SELECT password_hash FROM administrators WHERE id=$1 AND status=''active'' FOR UPDATE')`
		}
		if err := db.Pool.QueryRow(ctx, query, blocker).Scan(&ready); err != nil {
			t.Fatal(err)
		}
		if ready {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("administrator renewal/password rotation did not form its own expected lock chain", renewalFirst)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestAdminServiceRenewalPasswordRotationLockOrder(t *testing.T) {
	for _, operation := range []string{"create", "pay"} {
		for _, credential := range []string{"session", "api_key"} {
			for _, renewalFirst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/renewal_first=%t", operation, credential, renewalFirst), func(t *testing.T) {
					f := newAdminRenewalFixture(t, false)
					path, body := adminRenewalPath(f.sid)+"/renewals", renewalCreateBody(f.renewalQuote(t))
					if operation == "pay" {
						invoice := f.createRenewal(t, "admin-renewal-password-invoice")
						path, body = renewalPayPath(f.sid, invoice), renewalPayBody(invoice)
					}
					secret, table, column, hash := adminRenewalTestCredential(t, f, credential)
					login := portalRequest(f.h, "POST", "/api/v1/auth/login", map[string]string{"username": "admin-renewal-staff", "password": "integration-password"}, "", "admin")
					expect(t, login, 200)
					retained := login.Result().Cookies()[0]
					rotationCookie := retained.Name + "=" + retained.Value
					before := f.snapshot(t)
					gate, err := f.db.Pool.Begin(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer gate.Rollback(context.Background())
					if renewalFirst {
						if operation == "create" {
							_, err = gate.Exec(t.Context(), "SELECT id FROM services WHERE id=$1 FOR UPDATE", f.sid)
						} else {
							// Finance deliberately uses NOWAIT for service locks.
							// Its customer lock is the blocking business boundary.
							_, err = gate.Exec(t.Context(), "SELECT id FROM users WHERE id=$1 FOR UPDATE", f.uid)
						}
					} else {
						_, err = gate.Exec(t.Context(), "SELECT user_id FROM sessions WHERE token_hash=$1 FOR UPDATE", tokenHash(retained.Value))
					}
					if err != nil {
						t.Fatal(err)
					}
					renewal, rotation := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
					startRenewal := func() {
						go func() {
							renewal <- adminRenewalCredentialRequest(f, path, body, "admin-renewal-password-command", secret)
						}()
					}
					startRotation := func() {
						go func() {
							rotation <- portalRequest(f.h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "rotated-admin-renewal-password"}, rotationCookie, "admin")
						}()
					}
					if renewalFirst {
						startRenewal()
						waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
						startRotation()
					} else {
						startRotation()
						waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
						startRenewal()
					}
					waitAdminRenewalRotationChain(t, f.db, gate.Conn().PgConn().PID(), renewalFirst)
					if !renewalFirst {
						// A renewal waiting for the account must not pin the
						// other session/API key needed by password invalidation.
						probe, probeErr := f.db.Pool.Begin(t.Context())
						if probeErr != nil {
							t.Fatal(probeErr)
						}
						defer probe.Rollback(context.Background())
						var owner int64
						if probeErr = probe.QueryRow(t.Context(), "SELECT user_id FROM "+table+" WHERE "+column+"=$1 FOR UPDATE NOWAIT", hash).Scan(&owner); probeErr != nil || owner != f.actor {
							t.Fatal("renewal waiting on account prematurely locked its credential", owner, probeErr)
						}
						if probeErr = probe.Rollback(t.Context()); probeErr != nil {
							t.Fatal(probeErr)
						}
					}
					if err = gate.Commit(t.Context()); err != nil {
						t.Fatal(err)
					}
					renewalResponse := awaitBalanceResponse(t, renewal)
					expect(t, awaitBalanceResponse(t, rotation), 200)
					if renewalFirst {
						expect(t, renewalResponse, 201)
						after := f.snapshot(t)
						if operation == "create" {
							if after.invoices != before.invoices+1 || after.balance != before.balance || after.ledger != before.ledger || after.effects != before.effects || !after.due.Equal(before.due) {
								t.Fatal("pre-rotation creation failed its single commit", before, after)
							}
						} else if after.balance != before.balance-2500 || after.ledger != before.ledger+1 || after.effects != before.effects+1 || !after.due.Equal(before.due.AddDate(0, 1, 0)) {
							t.Fatal("pre-rotation payment failed its single settlement", before, after)
						}
					} else {
						expect(t, renewalResponse, 401)
						f.unchanged(t, before)
					}
					committed := f.snapshot(t)
					expect(t, adminRenewalCredentialRequest(f, path, body, "admin-renewal-password-command", secret), 401)
					f.unchanged(t, committed)
					var sessions, passwordAudits int64
					var retainedSession bool
					if err = f.db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM sessions WHERE user_id=$1),
 EXISTS(SELECT 1 FROM sessions WHERE user_id=$1 AND token_hash=$2),
 (SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='customer.password_changed')`, f.actor, tokenHash(retained.Value)).Scan(&sessions, &retainedSession, &passwordAudits); err != nil || sessions != 1 || !retainedSession || passwordAudits != 1 {
						t.Fatal("password rotation did not retain only its own session and audit", sessions, retainedSession, passwordAudits, err)
					}
				})
			}
		}
	}
}

func TestAdminServiceRenewalSupplierPaidRemainsPendingUntilConfirmed(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "admin-renewal-supplier")
	sid, _ := f.purchase(t, f.pid, "admin-renewal-supplier-purchase")
	if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
		t.Fatal(err)
	}
	var version int64
	var before time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT version,next_due_at FROM services WHERE id=$1", sid).Scan(&version, &before); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", adminRenewalPath(sid)+"/renewal-quotes", map[string]any{"expected_version": version}, f.root, "")
	expect(t, w, 200)
	q := data(t, w)
	if f.renews.Load() != 0 || f.renewalPays.Load() != 0 {
		t.Fatal("read-only administrator quote wrote upstream")
	}
	w = request(f.h, "POST", adminRenewalPath(sid)+"/renewals", renewalCreateBody(q), f.root, "admin-renewal-supplier-create")
	expect(t, w, 201)
	invoice := data(t, w)
	w = request(f.h, "POST", renewalPayPath(sid, invoice), renewalPayBody(invoice), f.root, "admin-renewal-supplier-pay")
	expect(t, w, 201)
	if data(t, w)["status"] != "paid" || data(t, w)["renewal_status"] != "pending" {
		t.Fatal("supplier local collection must not claim upstream completion", w.Body.String())
	}
	var due time.Time
	var jobs, notified int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT next_due_at,
 (SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='renew'),
 (SELECT count(*) FROM notification_events WHERE kind='service.renewed' AND payload->>'service_id'=$2)
 FROM services WHERE id=$1`, sid, fmt.Sprint(sid)).Scan(&due, &jobs, &notified); err != nil || !due.Equal(before) || jobs != 1 || notified != 0 {
		t.Fatal("supplier local payment fabricated an extended period", due, before, jobs, notified, err)
	}
	job := f.claim(t)
	if job.Action != "renew" {
		t.Fatal("supplier renewal queued wrong task", job)
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	if f.renews.Load() != 1 || f.renewalPays.Load() != 1 {
		t.Fatal("supplier renewal did not use existing exactly-once dispatch", f.renews.Load(), f.renewalPays.Load())
	}
	w = request(f.h, "POST", renewalPayPath(sid, invoice), renewalPayBody(invoice), f.root, "admin-renewal-supplier-pay")
	expect(t, w, 200)
	if data(t, w)["replayed"] != true || data(t, w)["renewal_status"] != "pending" {
		t.Fatal("historical payment receipt was silently rewritten after worker success", w.Body.String())
	}
}
