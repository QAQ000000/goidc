package httpapi

import (
	"encoding/json"
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"sync"
	"testing"
	"time"
)

func TestAffiliateRulesPermissionsSnapshotsAndIdempotency(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "rules-root", "super_admin")
	rid, reader := user(t, db, h, "rules-reader", "admin")
	contentRole(t, db, rid, "affiliates.view")
	path := "/api/v1/admin/affiliate/rules"
	expect(t, request(h, "GET", path, nil, reader, ""), 200)
	expect(t, request(h, "GET", path+"/schema", nil, reader, ""), 200)
	pid := product(t, db, 10)
	body := map[string]any{"expected_version": 1, "product_rules": []any{map[string]any{"product_id": pid, "event_type": "first_purchase", "mode": "rate", "rate_bps": 1234, "amount_cents": 0, "max_cents": 100}}, "tier_rules": []any{map[string]any{"lower_cents": 0, "upper_cents": 1000, "rate_bps": 100, "amount_cents": 0}, map[string]any{"lower_cents": 1000, "upper_cents": nil, "rate_bps": 200, "amount_cents": 0}}}
	expect(t, request(h, "PUT", path, body, reader, "rules-no-permission"), 403)
	saved := request(h, "PUT", path, body, root, "rules-save-first")
	expect(t, saved, 200)
	replay := request(h, "PUT", path, body, root, "rules-save-first")
	expect(t, replay, 200)
	if replay.Body.String() != saved.Body.String() {
		t.Fatal("replay changed")
	}
	body["expected_version"] = 2
	expect(t, request(h, "PUT", path, body, root, "rules-save-first"), 409)
	for i, invalid := range []map[string]any{
		{"product_rules": nil}, {"tier_rules": nil}, {"product_rules": []any{map[string]any{"product_id": 999999, "event_type": "first_purchase", "mode": "disabled"}}},
		{"tier_rules": []any{map[string]any{"lower_cents": 0, "upper_cents": 1000, "rate_bps": 100}, map[string]any{"lower_cents": 500, "rate_bps": 200}}},
		{"product_rules": []any{map[string]any{"product_id": pid, "event_type": "bogus", "mode": "disabled"}}},
		{"product_rules": []any{map[string]any{"product_id": pid, "event_type": "renewal", "mode": "disabled", "rate_bps": 100}}},
		{"tier_rules": []any{map[string]any{"lower_cents": 0, "rate_bps": 100, "amount_cents": 5}}},
		{"unknown": true},
	} {
		raw, _ := json.Marshal(body)
		var b map[string]any
		json.Unmarshal(raw, &b)
		for k, v := range invalid {
			b[k] = v
		}
		r := request(h, "PUT", path, b, root, fmt.Sprintf("rules-invalid-%d", i))
		if r.Code != 409 && r.Code != 400 {
			t.Fatalf("invalid accepted %d: %s", i, r.Body.String())
		}
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- request(h, "PUT", path, body, root, fmt.Sprintf("rules-concurrent-%d", i)).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == 200 {
			ok++
		} else if c != 409 {
			t.Fatalf("unexpected %d", c)
		}
	}
	if ok != 1 {
		t.Fatal("concurrent lost version")
	}
	var n int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='affiliate.rules.updated'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("audit %d %v", n, err)
	}
	// Policy and product/tier saves share a version and serialize together.
	policy := data(t, request(h, "GET", "/api/v1/admin/affiliate/policy", nil, root, ""))
	delete(policy, "id")
	delete(policy, "version")
	delete(policy, "updated_at")
	policy["expected_version"] = 3
	body["expected_version"] = 3
	mixed := make(chan int, 2)
	go func() { mixed <- request(h, "PUT", path, body, root, "rules-mixed-save").Code }()
	go func() {
		mixed <- request(h, "PUT", "/api/v1/admin/affiliate/policy", policy, root, "policy-mixed-save").Code
	}()
	a, b := <-mixed, <-mixed
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatalf("shared version saves %d/%d", a, b)
	}
}
func TestAffiliateRulesMonthlyTurnoverAndPrecedence(t *testing.T) {
	f := settlementFixture(t)
	body := map[string]any{"expected_version": 1, "product_rules": []any{}, "tier_rules": []any{map[string]any{"lower_cents": 0, "upper_cents": 1500, "rate_bps": 500, "amount_cents": 0}, map[string]any{"lower_cents": 1500, "upper_cents": nil, "rate_bps": 2000, "amount_cents": 0}}}
	expect(t, request(f.h, "PUT", "/api/v1/admin/affiliate/rules", body, f.admin, "monthly-rules-first"), 200)
	first, _ := f.purchase(t, 1)
	second, _ := f.purchase(t, 2)
	for _, tc := range []struct{ id, amount, turnover int64 }{{first, 66, 1333}, {second, 266, 2666}} {
		var amount, turnover int64
		var source string
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT amount_cents,(rule_snapshot->>'turnover_cents')::bigint,rule_snapshot->>'rule_source' FROM affiliate_commission_entries WHERE invoice_id=$1`, tc.id).Scan(&amount, &turnover, &source); err != nil || amount != tc.amount || turnover != tc.turnover || source != "tier" {
			t.Fatalf("invoice %d: amount=%d turnover=%d source=%s err=%v", tc.id, amount, turnover, source, err)
		}
	}
	// Refunded invoices no longer contribute to the next settlement's turnover.
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE invoices SET status='refunded' WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	third, _ := f.purchase(t, 3)
	var turnover int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (rule_snapshot->>'turnover_cents')::bigint FROM affiliate_commission_entries WHERE invoice_id=$1`, third).Scan(&turnover); err != nil || turnover != 2666 {
		t.Fatalf("refund exclusion: %d %v", turnover, err)
	}
	// Future edits preserve previously earned amounts and historical rule sets.
	body["expected_version"] = 2
	body["tier_rules"] = []any{map[string]any{"lower_cents": 0, "rate_bps": 9000, "amount_cents": 0}}
	expect(t, request(f.h, "PUT", "/api/v1/admin/affiliate/rules", body, f.admin, "monthly-rules-second"), 200)
	var amount int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT amount_cents FROM affiliate_commission_entries WHERE invoice_id=$1`, second).Scan(&amount); err != nil || amount != 266 {
		t.Fatal("historical recomputation")
	}
}

func TestAffiliateRulesCreditCashMonthAndReplay(t *testing.T) {
	f := settlementFixture(t)
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE credit_settings SET enabled=true; UPDATE customer_credit_accounts SET limit_cents=100000`); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"expected_version": 1, "product_rules": []any{}, "tier_rules": []any{map[string]any{"lower_cents": 0, "rate_bps": 1500, "amount_cents": 0}}}
	expect(t, request(f.h, "PUT", "/api/v1/admin/affiliate/rules", body, f.admin, "credit-monthly-rules"), 200)
	pid := product(t, f.db, 10)
	version, item := add(t, f.h, f.customer, pid, 1)
	order := request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, f.customer, "credit-monthly-order")
	expect(t, order, 201)
	invoice := int64(data(t, order)["invoice_id"].(float64))
	seedHistoricalCreditPayment(t, f.db, f.customerID, invoice, f.customer, "credit-monthly-payment")
	var n int
	check := func(want int) {
		t.Helper()
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM affiliate_commission_entries WHERE invoice_id=$1`, invoice).Scan(&n); err != nil || n != want {
			t.Fatalf("commission count %d %v", n, err)
		}
	}
	check(0)
	received := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	// Credit HTTP is retired. Seed historical receipts atomically and exercise
	// only the surviving commission consumer against those persisted facts.
	var actor int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT user_id FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.admin))).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	record := func(amount, version int64, key string) {
		t.Helper()
		if err := f.db.WithTx(t.Context(), func(tx pgx.Tx) error {
			inserted, err := tx.Exec(t.Context(), `WITH account AS (
 UPDATE customer_credit_accounts SET used_cents=used_cents-$3,version=version+1 WHERE user_id=$2
 ), debt AS (
 UPDATE credit_invoice_payments SET remaining_cents=remaining_cents-$3,version=version+1,
 status=CASE WHEN remaining_cents=$3 THEN 'repaid' ELSE 'outstanding' END
 WHERE invoice_id=$1 AND user_id=$2 AND version=$5 RETURNING version,remaining_cents
 ) INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after)
 SELECT $1,$2,$4,$3,$6,$7,$6,$5,version,remaining_cents FROM debt`, invoice, f.customerID, amount, actor, version, key, received)
			if err != nil {
				return err
			}
			if inserted.RowsAffected() != 1 {
				return fmt.Errorf("historical repayment version %d was not seeded", version)
			}
			return f.db.QueueAffiliateCommissions(t.Context(), tx, invoice, 0)
		}); err != nil {
			t.Fatal(err)
		}
	}
	record(333, 1, "monthly-credit-partial")
	check(0)
	record(1000, 2, "monthly-credit-final")
	check(1)
	if err := f.db.WithTx(t.Context(), func(tx pgx.Tx) error {
		return f.db.QueueAffiliateCommissions(t.Context(), tx, invoice, 0)
	}); err != nil {
		t.Fatal(err)
	}
	check(1)
	var paid time.Time
	var amount int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT t.paid_at,c.amount_cents FROM affiliate_turnover_entries t JOIN affiliate_commission_entries c ON c.invoice_id=t.invoice_id WHERE t.invoice_id=$1`, invoice).Scan(&paid, &amount); err != nil || !paid.Equal(received) || amount != 199 {
		t.Fatalf("cash time %v amount %d err %v", paid, amount, err)
	}
}

func TestAffiliateRulesConcurrentCustomersMultiLineAndMonthBoundary(t *testing.T) {
	f := settlementFixture(t)
	other, _ := user(t, f.db, f.h, "rules-concurrent-other", "customer")
	var click int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO affiliate_clicks(link_id,session_hash,registered_user_id,converted_at) SELECT link_id,repeat('f',64),$1,now() FROM affiliate_attributions WHERE user_id=$2 RETURNING id`, other, f.customerID).Scan(&click); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO affiliate_attributions(user_id,account_id,link_id,click_id,first_clicked_at) SELECT $1,account_id,link_id,$2,now() FROM affiliate_attributions WHERE user_id=$3`, other, click, f.customerID); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"expected_version": 1, "product_rules": []any{}, "tier_rules": []any{map[string]any{"lower_cents": 0, "upper_cents": 1500, "rate_bps": 500}, map[string]any{"lower_cents": 1500, "rate_bps": 2000}}}
	expect(t, request(f.h, "PUT", "/api/v1/admin/affiliate/rules", body, f.admin, "rules-concurrent-tier"), 200)
	pid := product(t, f.db, 10)
	makeInvoice := func(uid int64, n int, paid string) int64 {
		t.Helper()
		var order, invoice int64
		if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES($1,$2,'paid',1000,$1,'fixture') RETURNING id`, fmt.Sprintf("rules-direct-%d", n), uid).Scan(&order); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'tier multi','monthly',1,500,0,500),($1,$2,'tier multi','monthly',1,500,0,500)`, order, pid); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at) VALUES($1,$2,$3,'paid',1000,$4::timestamptz) RETURNING id`, fmt.Sprintf("rules-direct-invoice-%d", n), uid, order, paid).Scan(&invoice); err != nil {
			t.Fatal(err)
		}
		return invoice
	}
	ids := []int64{makeInvoice(f.customerID, 1, "2026-10-31T15:59:59Z"), makeInvoice(other, 2, "2026-10-31T15:59:59Z")}
	queue := func(id int64) error {
		tx, err := f.db.Pool.Begin(t.Context())
		if err != nil {
			return err
		}
		defer tx.Rollback(t.Context())
		if err = f.db.QueueAffiliateCommissions(t.Context(), tx, id, 0); err != nil {
			return err
		}
		return tx.Commit(t.Context())
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range ids {
		wg.Add(1)
		go func(id int64) { defer wg.Done(); errs <- queue(id) }(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var total, n int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT sum(amount_cents),count(*) FROM affiliate_commission_entries WHERE invoice_id=ANY($1)`, ids).Scan(&total, &n); err != nil || total != 250 || n != 4 {
		t.Fatalf("concurrent multiline amount %d rows %d err %v", total, n, err)
	}
	for _, id := range ids {
		if err := queue(id); err != nil {
			t.Fatal(err)
		}
	}
	november := makeInvoice(f.customerID, 3, "2026-10-31T16:00:00Z")
	if err := queue(november); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT sum(amount_cents) FROM affiliate_commission_entries WHERE invoice_id=$1`, november).Scan(&total); err != nil || total != 50 {
		t.Fatalf("Shanghai month boundary %d %v", total, err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM affiliate_turnover_entries`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("duplicate turnover %d %v", n, err)
	}
	// Product fixed override wins over monthly tiers and respects its cap.
	body["expected_version"] = 2
	body["product_rules"] = []any{map[string]any{"product_id": pid, "event_type": "reorder", "mode": "fixed", "rate_bps": 0, "amount_cents": 30, "max_cents": 20}}
	expect(t, request(f.h, "PUT", "/api/v1/admin/affiliate/rules", body, f.admin, "rules-fixed-precedence"), 200)
	fixed := makeInvoice(f.customerID, 4, "2026-10-31T16:00:00Z")
	if err := queue(fixed); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT sum(amount_cents) FROM affiliate_commission_entries WHERE invoice_id=$1`, fixed).Scan(&total); err != nil || total != 40 {
		t.Fatalf("fixed precedence/cap %d %v", total, err)
	}
	body["expected_version"] = 3
	body["product_rules"] = []any{map[string]any{"product_id": pid, "event_type": "reorder", "mode": "disabled", "rate_bps": 0, "amount_cents": 0, "max_cents": nil}}
	expect(t, request(f.h, "PUT", "/api/v1/admin/affiliate/rules", body, f.admin, "rules-disabled-precedence"), 200)
	disabled := makeInvoice(f.customerID, 5, "2026-10-31T16:00:00Z")
	if err := queue(disabled); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM affiliate_commission_entries WHERE invoice_id=$1`, disabled).Scan(&n); err != nil || n != 0 {
		t.Fatalf("disabled precedence %d %v", n, err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM affiliate_turnover_entries WHERE invoice_id=$1`, disabled).Scan(&n); err != nil || n != 1 {
		t.Fatalf("zero commission turnover %d %v", n, err)
	}
}
