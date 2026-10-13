package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

func TestAffiliatePolicyPermissionsVersionIdempotencyAndSnapshots(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "affiliate-policy-root", "super_admin")
	_, customer := user(t, db, h, "affiliate-policy-customer", "customer")
	readerID, reader := user(t, db, h, "affiliate-policy-reader", "admin")
	contentRole(t, db, readerID, "affiliates.view")
	managerID, manager := user(t, db, h, "affiliate-policy-manager", "admin")
	contentRole(t, db, managerID, "affiliates.manage")
	path := "/api/v1/admin/affiliate/policy"
	expect(t, request(h, "GET", path, nil, customer, ""), 403)
	expect(t, request(h, "GET", path, nil, reader, ""), 403)
	expect(t, request(h, "GET", path, nil, manager, ""), 200)
	initial := data(t, request(h, "GET", path, nil, root, ""))
	pid := int64(initial["id"].(float64))
	var productID, oldRuleID int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO products(name,slug,price_cents) VALUES('Affiliate override','affiliate-policy-override',1000) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT id FROM affiliate_rule_sets WHERE program_id=$1 AND effective_to IS NULL`, pid).Scan(&oldRuleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO affiliate_product_rules(rule_set_id,product_id,event_type,mode,rate_bps) VALUES($1,$2,'first_purchase','override',1234)`, oldRuleID, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO affiliate_tier_rules(rule_set_id,lower_cents,rate_bps) VALUES($1,0,500)`, oldRuleID); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"name": "真实推介设置", "enabled": true, "attribution_days": 45, "hold_days": 8, "min_withdraw_cents": 5000, "fee_cents": 100, "default_rules": map[string]any{"first_purchase": map[string]any{"rate_bps": 1250, "amount_cents": 0, "max_cents": 900}, "reorder": map[string]any{"rate_bps": 0, "amount_cents": 77, "max_cents": nil}, "renewal": map[string]any{"rate_bps": 2500, "amount_cents": 0, "max_cents": nil}, "upgrade": map[string]any{"rate_bps": 0, "amount_cents": 0, "max_cents": nil}}, "expected_version": 1}
	expect(t, request(h, "PUT", path, body, reader, "affiliate-policy-denied"), 403)
	expect(t, request(h, "PUT", path, body, root, ""), 409)
	saved := request(h, "PUT", path, body, root, "affiliate-policy-save-1")
	expect(t, saved, 200)
	if data(t, saved)["version"] != float64(2) {
		t.Fatal("policy version did not advance")
	}
	replay := request(h, "PUT", path, body, root, "affiliate-policy-save-1")
	expect(t, replay, 200)
	if saved.Body.String() != replay.Body.String() {
		t.Fatalf("replay changed policy: %s", replay.Body.String())
	}
	read := request(h, "GET", path, nil, root, "")
	expect(t, read, 200)
	if read.Body.String() != saved.Body.String() {
		t.Fatalf("policy not persisted: %s", read.Body.String())
	}
	body["name"] = "变更后的请求"
	expect(t, request(h, "PUT", path, body, root, "affiliate-policy-save-1"), 409)
	expect(t, request(h, "PUT", path, body, root, "affiliate-policy-stale"), 409)
	var rules, audits, operations, overrides, tiers int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM affiliate_rule_sets WHERE program_id=$1),(SELECT count(*) FROM audit_events WHERE actor_id=$2 AND action='affiliate.policy.updated'),(SELECT count(*) FROM affiliate_operations WHERE user_id=$2 AND operation='policy.update'),(SELECT count(*) FROM affiliate_product_rules r JOIN affiliate_rule_sets s ON s.id=r.rule_set_id WHERE s.program_id=$1 AND s.effective_to IS NULL AND r.rate_bps=1234),(SELECT count(*) FROM affiliate_tier_rules r JOIN affiliate_rule_sets s ON s.id=r.rule_set_id WHERE s.program_id=$1 AND s.effective_to IS NULL AND r.rate_bps=500)`, pid, rootID).Scan(&rules, &audits, &operations, &overrides, &tiers); err != nil {
		t.Fatal(err)
	}
	if rules != 2 || audits != 1 || operations != 1 || overrides != 1 || tiers != 1 {
		t.Fatalf("unexpected history/audit/receipt/clone counts: %d %d %d %d %d", rules, audits, operations, overrides, tiers)
	}
	var oldDefault []byte
	var ended bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT default_rules,effective_to IS NOT NULL FROM affiliate_rule_sets WHERE id=$1`, oldRuleID).Scan(&oldDefault, &ended); err != nil {
		t.Fatal(err)
	}
	if string(oldDefault) != "{}" || !ended {
		t.Fatal("old rule snapshot modified or left active")
	}
	// All fields reject invalid values without recording an operation or advancing versions.
	for field, value := range map[string]any{"enabled": nil, "hold_days": 366, "fee_cents": 5000, "attribution_days": 0, "min_withdraw_cents": 0, "default_rules": map[string]any{"bogus": map[string]any{"rate_bps": 100}}, "name": ""} {
		raw, _ := json.Marshal(body)
		var invalid map[string]any
		_ = json.Unmarshal(raw, &invalid)
		invalid["expected_version"] = 2
		invalid[field] = value
		expect(t, request(h, "PUT", path, invalid, root, "affiliate-policy-invalid-"+field), 409)
	}
	body["expected_version"] = 2
	body["enabled"] = false
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- request(h, "PUT", path, body, manager, fmt.Sprintf("affiliate-policy-concurrent-%d", i)).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	ok, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			ok++
		} else if code == 409 {
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("concurrent policy saves: success=%d conflict=%d", ok, conflict)
	}
	final := data(t, request(h, "GET", path, nil, manager, ""))
	if final["enabled"] != false || final["version"] != float64(3) {
		t.Fatalf("disabled policy not read back: %#v", final)
	}
	expect(t, request(h, http.MethodPost, "/api/v1/affiliate/apply", nil, customer, "affiliate-policy-disabled-apply"), 409)
}

func TestAffiliatePolicyDisabledStopsNewReferralAndKeepsExistingWithdrawal(t *testing.T) {
	f := settlementFixture(t)
	invoice, _ := f.purchase(t, 701)
	var amount int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT amount_cents FROM affiliate_commission_entries WHERE invoice_id=$1`, invoice).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	var code string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT code FROM affiliate_links WHERE account_id=$1 ORDER BY id LIMIT 1`, f.accountID).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if released, err := f.db.SettleAffiliateCommissions(t.Context(), 100); err != nil || released != 1 {
		t.Fatalf("release=%d err=%v", released, err)
	}
	withdrawal := f.withdraw(t, 50, "affiliate-policy-before-disable-withdrawal")
	pendingUser, _ := user(t, f.db, f.h, "affiliate-policy-unconverted", "customer")
	sessionHash := tokenHash("affiliate-policy-before-disabled-click")
	if click, err := f.db.RecordAffiliateClick(t.Context(), code, sessionHash, "", "/", "test"); err != nil || !click.Valid {
		t.Fatalf("pre-disable click invalid: %v", err)
	}
	path := "/api/v1/admin/affiliate/policy"
	current := data(t, request(f.h, "GET", path, nil, f.admin, ""))
	delete(current, "id")
	delete(current, "version")
	delete(current, "updated_at")
	current["enabled"] = false
	current["expected_version"] = 1
	expect(t, request(f.h, "PUT", path, current, f.admin, "affiliate-policy-disable-earned"), 200)
	click, err := f.db.RecordAffiliateClick(t.Context(), code, tokenHash("affiliate-policy-after-disabled-click"), "", "/", "test")
	if err != nil || click.Valid || click.Recorded {
		t.Fatalf("disabled plan recorded click: %#v %v", click, err)
	}
	tx, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.ConvertAffiliateClickTx(t.Context(), tx, pendingUser, code, sessionHash); err != nil {
		tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var attributed int
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM affiliate_attributions WHERE user_id=$1`, pendingUser).Scan(&attributed); err != nil || attributed != 0 {
		t.Fatalf("disabled registration attributed=%d err=%v", attributed, err)
	}
	newInvoice, _ := f.purchase(t, 702)
	var commissions int
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM affiliate_commission_entries WHERE invoice_id=$1`, newInvoice).Scan(&commissions); err != nil || commissions != 0 {
		t.Fatalf("disabled plan created new commission=%d err=%v", commissions, err)
	}
	expect(t, request(f.h, "POST", "/api/v1/affiliate/withdrawals", map[string]any{"amount_cents": 50, "method": "bank", "target": "6222021234567890123", "holder": "测试收款人"}, f.owner, "affiliate-policy-disabled-new-withdrawal"), 409)
	f.review(t, withdrawal, "approved", 1, "affiliate-policy-disabled-earned-review")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/affiliate/withdrawals/%d/payout", withdrawal), settlementPayoutInput(), f.admin, "affiliate-policy-disabled-earned-payout"), 200)
	f.overview(t, 0, amount-50, 0, 50)
}
