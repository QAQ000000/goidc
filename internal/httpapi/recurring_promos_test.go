package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

type recurringPurchase struct {
	db                                *store.Store
	h                                 http.Handler
	uid, pid, orderID, invoiceID, sid int64
	customer, root, other             string
}

func recurringPurchaseFixture(t *testing.T, percent int64) recurringPurchase {
	return renewalPurchaseFixture(t, "percent", percent, 0)
}

func renewalPurchaseFixture(t *testing.T, kind string, value, limit int64) recurringPurchase {
	t.Helper()
	db, h := testServer(t)
	uid, customer := user(t, db, h, "recurring-customer", "customer")
	_, root := user(t, db, h, "recurring-root", "super_admin")
	_, other := user(t, db, h, "recurring-other", "customer")
	pid := product(t, db, 20)
	expect(t, request(h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "FOREVER", "discount_type": kind, "value": value, "recurring": true, "recurring_limit": limit, "max_uses": 1, "once_per_customer": true}, root, ""), 201)
	version, item := add(t, h, customer, pid, 2)
	applied := request(h, "POST", "/api/v1/cart/promo", map[string]any{"code": "FOREVER", "version": version}, customer, "")
	expect(t, applied, 200)
	preview := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))["promo"].(map[string]any)
	if preview["recurring_limit"] != float64(limit) || preview["recurring"] != true {
		t.Fatal("cart renewal terms missing", preview)
	}
	w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(data(t, applied)["version"].(float64)), ItemIDs: []int64{item}}, customer, "recurring-order")
	expect(t, w, 201)
	oid, iid := int64(data(t, w)["order_id"].(float64)), int64(data(t, w)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), map[string]any{"amount_cents": 100000}, root, "recurring-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{}, customer, "recurring-purchase-pay"), 201)
	for n := 0; n < 2; n++ {
		job, err := db.ClaimProvisionJob(t.Context())
		if err != nil || job == nil {
			t.Fatal("missing create job", err)
		}
		if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
			t.Fatal(err)
		}
	}
	var sid int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT min(id) FROM services WHERE order_id=$1", oid).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	return recurringPurchase{db: db, h: h, uid: uid, pid: pid, orderID: oid, invoiceID: iid, sid: sid, customer: customer, root: root, other: other}
}

func TestRecurringPromoSnapshotManualAutoRenewAndRefund(t *testing.T) {
	f := recurringPurchaseFixture(t, 10)
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM services WHERE order_id=$1 AND renewal_discount_percent=10 AND renewal_promo_code='FOREVER'", f.orderID).Scan(&count); err != nil || count != 2 {
		t.Fatal("quantity benefits missing", count, err)
	}
	// Mutable issuance rules cannot revoke a previously purchased benefit.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE promo_codes SET active=false,value=90,expires_at=now()-interval '1 day' WHERE code='FOREVER'"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/services/%d", f.sid)
	detail := request(f.h, "GET", path, nil, f.customer, "")
	expect(t, detail, 200)
	d := data(t, detail)
	if d["recurring_price_cents"] != float64(1234) || d["renewal_amount_cents"] != float64(1111) || d["renewal_discount_percent"] != float64(10) {
		t.Fatal("display price differs from benefit", d)
	}
	expect(t, request(f.h, "GET", path, nil, f.other, ""), 404)
	expect(t, request(f.h, "POST", path+"/renew", map[string]any{}, f.other, "recurring-other-renew"), 404)
	renew := request(f.h, "POST", path+"/renew", map[string]any{}, f.customer, "recurring-first-renew")
	expect(t, renew, 201)
	iid := int64(data(t, renew)["invoice_id"].(float64))
	if data(t, renew)["amount_cents"] != float64(1111) {
		t.Fatal("renewal did not discount current price")
	}
	expect(t, request(f.h, "POST", path+"/renew", map[string]any{}, f.customer, "recurring-first-renew"), 200)
	expect(t, request(f.h, "POST", path+"/renew", map[string]any{}, f.customer, "recurring-second-pending"), 409)
	detail = request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", iid), nil, f.customer, "")
	expect(t, detail, 200)
	d = data(t, detail)
	line := d["items"].([]any)[0].(map[string]any)
	if d["discount_cents"] != float64(123) || d["promo_code"] != "FOREVER" || line["renewal_base_cents"] != float64(1234) || line["renewal_discount_percent"] != float64(10) {
		t.Fatal("invoice discount history missing", d)
	}
	var before int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{}, f.customer, "recurring-renew-pay"), 201)
	refund := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", iid), map[string]any{}, f.root, "recurring-renew-refund")
	expect(t, refund, 201)
	if data(t, refund)["amount_cents"] != float64(1111) {
		t.Fatal("refunded undiscounted amount")
	}
	var after int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&after); err != nil || after != before {
		t.Fatal("renewal refund changed balance", after, before, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET auto_renew=true,next_due_at=now()+interval '1 hour' WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	processed, err := f.db.ProcessAutoRenewals(t.Context(), 20)
	if err != nil || processed != 1 {
		t.Fatal("automatic renewal failed", processed, err)
	}
	var amount, base, discount, redemptions, uses int64
	var state string
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT i.amount_cents,i.status,ii.renewal_base_cents,ii.renewal_discount_cents,
 (SELECT count(*) FROM promo_code_redemptions),(SELECT used_count FROM promo_codes WHERE code='FOREVER')
 FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id WHERE i.idempotency_key LIKE 'auto-renew-%'`).Scan(&amount, &state, &base, &discount, &redemptions, &uses); err != nil || amount != 1111 || base != 1234 || discount != 123 || state != "paid" || redemptions != 1 || uses != 1 {
		t.Fatal("automatic renewal snapshot or redemption incorrect", amount, state, base, discount, redemptions, uses, err)
	}
}

func TestRecurringPromoEligibilityAndPartialCheckout(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "scope-recurring-customer", "customer")
	_, root := user(t, db, h, "scope-recurring-root", "super_admin")
	pid, other := product(t, db, 10), product(t, db, 10)
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents) VALUES($1,'once',1000,0),($1,'quarterly',3000,0)", pid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "SCOPE20", "discount_type": "percent", "value": 20, "recurring": true, "applies_product_ids": []int64{pid}, "cycles": []string{"monthly", "once"}}, root, ""), 201)
	add(t, h, customer, pid, 2)
	add(t, h, customer, other, 1)
	for _, cycle := range []string{"once", "quarterly"} {
		version, _ := cart(t, h, customer)
		expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": pid, "quantity": 1, "billing_cycle": cycle}, customer, ""), 200)
	}
	w := request(h, "GET", "/api/v1/cart", nil, customer, "")
	d := data(t, w)
	expect(t, request(h, "POST", "/api/v1/cart/promo", map[string]any{"version": d["version"], "code": "SCOPE20"}, customer, ""), 200)
	var selected, excluded []int64
	for _, entry := range d["items"].([]any) {
		item := entry.(map[string]any)
		id := int64(item["id"].(float64))
		if item["billing_cycle"] == "quarterly" {
			excluded = append(excluded, id)
		} else {
			selected = append(selected, id)
		}
	}
	version, _ := cart(t, h, customer)
	w = request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: selected}, customer, "scope-recurring-checkout")
	expect(t, w, 201)
	oid := int64(data(t, w)["order_id"].(float64))
	var granted, ungranted int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FILTER(WHERE renewal_discount_percent=20),count(*) FILTER(WHERE renewal_discount_percent=0) FROM services WHERE order_id=$1", oid).Scan(&granted, &ungranted); err != nil || granted != 2 || ungranted != 2 {
		t.Fatal("benefit leaked outside purchased scope", granted, ungranted, err)
	}
	if len(excluded) != 1 {
		t.Fatal("quarterly item not preserved")
	}
	detail := data(t, request(h, "GET", fmt.Sprintf("/api/v1/orders/%d", oid), nil, customer, ""))
	for _, entry := range detail["items"].([]any) {
		item := entry.(map[string]any)
		percent := item["renewal_discount_percent"].(float64)
		if (item["product_id"] == float64(pid) && item["billing_cycle"] == "monthly") != (percent == 20) {
			t.Fatal("order item benefit snapshot incorrect", item)
		}
	}
	w = request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: selected}, customer, "scope-recurring-checkout")
	expect(t, w, 200)
	if err := db.Pool.QueryRow(t.Context(), "SELECT used_count FROM promo_codes WHERE code='SCOPE20'").Scan(&granted); err != nil || granted != 1 {
		t.Fatal("idempotency consumed extra redemption", granted, err)
	}
}

func TestRecurringPromoRejectUnsupportedAndNoHistoricalGrant(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "reject-recurring-root", "super_admin")
	_, customer := user(t, db, h, "reject-recurring-customer", "customer")
	for _, kind := range []string{"free"} {
		w := request(h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "BAD" + kind, "discount_type": kind, "value": 10, "recurring": true}, root, "")
		if w.Code < 400 {
			t.Fatal("unsupported recurring type accepted", kind)
		}
	}
	pid := product(t, db, 5)
	expect(t, request(h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "ONCE10", "discount_type": "percent", "value": 10}, root, ""), 201)
	version, item := add(t, h, customer, pid, 1)
	w := request(h, "POST", "/api/v1/cart/promo", map[string]any{"code": "ONCE10", "version": version}, customer, "")
	expect(t, w, 200)
	w = request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(data(t, w)["version"].(float64)), ItemIDs: []int64{item}}, customer, "non-recurring-checkout")
	expect(t, w, 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE promo_codes SET recurring=true WHERE code='ONCE10'"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM services WHERE renewal_discount_percent<>0").Scan(&count); err != nil || count != 0 {
		t.Fatal("mutating issuance granted historical benefit", count, err)
	}
}

func TestRecurringPromoRenewalAuditRollbackConcurrencyAndZeroPay(t *testing.T) {
	f := recurringPurchaseFixture(t, 100)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION recurring_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.renewal.created' THEN RAISE EXCEPTION 'reject renewal audit'; END IF; RETURN NEW; END $$; CREATE TRIGGER recurring_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION recurring_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/services/%d/renew", f.sid)
	expect(t, request(f.h, "POST", path, map[string]any{}, f.customer, "recurring-zero-renew"), 500)
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoice_items WHERE item_type='renew'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed audit left renewal", count, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER recurring_reject_audit"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- request(f.h, "POST", path, map[string]any{}, f.customer, "recurring-zero-renew").Code
		}()
	}
	wg.Wait()
	close(codes)
	created, replayed := 0, 0
	for code := range codes {
		if code == 201 {
			created++
		} else if code == 200 {
			replayed++
		} else {
			t.Fatal("concurrent renewal failed", code)
		}
	}
	if created != 1 || replayed != 1 {
		t.Fatal("renewal replay incorrect", created, replayed)
	}
	var iid int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM invoices WHERE idempotency_key='recurring-zero-renew' AND amount_cents=0").Scan(&iid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{}, f.customer, "recurring-zero-pay"), 201)
	var amount int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT delta_cents FROM balance_ledger WHERE reference_type='invoice' AND reference_id=$1", iid).Scan(&amount); err != nil || amount != 0 {
		t.Fatal("zero renewal debited money", amount, err)
	}
	if _, err := f.db.Pool.Exec(context.Background(), "UPDATE invoice_items SET renewal_discount_cents=1 WHERE invoice_id=$1", iid); err == nil {
		t.Fatal("inconsistent monetary snapshot accepted")
	}
}

func TestRecurringPromoSurvivesConfigurationDelivery(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET renewal_discount_percent=20,renewal_promo_code='CONTRACT20' WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	quote := f.quote(t, f.large)
	change := f.submit(t, quote, "recurring-configuration-submit")
	iid := int64(change["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{}, f.customer, "recurring-configuration-pay"), 201)
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	d := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	if d["recurring_price_cents"] != float64(3000) || d["renewal_amount_cents"] != float64(2400) || d["renewal_promo_code"] != "CONTRACT20" {
		t.Fatal("configuration delivery lost benefit", d)
	}
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.sid), map[string]any{}, f.customer, "recurring-renew-after-upgrade")
	expect(t, w, 201)
	if data(t, w)["amount_cents"] != float64(2400) {
		t.Fatal("renewal used original base after configuration change")
	}
}

func TestRecurringPromoSurvivesProductAndCycleDelivery(t *testing.T) {
	for _, mode := range []string{"product", "billing_cycle"} {
		t.Run(mode, func(t *testing.T) {
			f := configurationUpgradeFixture(t, false)
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET renewal_discount_percent=20,renewal_promo_code='CONTRACT20' WHERE id=$1", f.sid); err != nil {
				t.Fatal(err)
			}
			var q map[string]any
			want := int64(3600)
			if mode == "product" {
				pid, oid := productChangeTarget(t, f, false)
				q = productChangeQuote(t, f, pid, oid)
			} else {
				if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents,active) VALUES($1,'quarterly',6000,0,true)`, f.pid); err != nil {
					t.Fatal(err)
				}
				d := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
				w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/billing-cycle-quotes", f.sid), store.BillingCycleChangeInput{TargetBillingCycle: "quarterly", ExpectedVersion: int64(d["version"].(float64)), ExpectedRevision: *configRevision(t, f.h, f.root, f.pid)}, f.customer, "")
				expect(t, w, 201)
				q = data(t, w)
				want = 5200
			}
			change := f.submit(t, q, "recurring-change-submit")
			iid := int64(change["invoice_id"].(float64))
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{}, f.customer, "recurring-change-pay"), 201)
			job, err := f.db.ClaimProvisionJob(t.Context())
			if err != nil || job == nil {
				t.Fatal(err)
			}
			if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.sid), map[string]any{}, f.customer, "recurring-renew-after-change")
			expect(t, w, 201)
			if data(t, w)["amount_cents"] != float64(want) {
				t.Fatal("changed contract lost benefit", data(t, w))
			}
		})
	}
}
