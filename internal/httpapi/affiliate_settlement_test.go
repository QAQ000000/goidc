package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

type affiliateSettlementFixture struct {
	db                             *store.Store
	h                              http.Handler
	owner, admin, customer         string
	ownerID, customerID, accountID int64
}

func settlementFixture(t *testing.T) affiliateSettlementFixture {
	t.Helper()
	db, h, ownerID, owner, customerID, _, code, _ := setupAffiliateFixture(t)
	_, admin := user(t, db, h, "settlement-root", "super_admin")
	customer := login(t, h, "affiliate-referred")
	var accountID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM affiliate_accounts WHERE user_id=$1", ownerID).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	click := request(h, "GET", "/api/v1/affiliate/click/"+code, nil, "", "")
	expect(t, click, 200)
	session := affiliateCookieValue(click.Result(), "zjmfgo_affiliate_session")
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = db.ConvertAffiliateClickTx(t.Context(), tx, customerID, code, tokenHash(session)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE affiliate_programs SET hold_days=0,min_withdraw_cents=50,fee_cents=10; UPDATE affiliate_rule_sets SET default_rules='{"first_purchase":{"rate_bps":1000},"reorder":{"rate_bps":1000}}'`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]any{"amount_cents": 100000}, admin, "settlement-topup"), 201)
	return affiliateSettlementFixture{db, h, owner, admin, customer, ownerID, customerID, accountID}
}
func login(t *testing.T, h http.Handler, username string) string {
	t.Helper()
	response := request(h, "POST", "/api/v1/auth/login", map[string]any{"identifier": username + "@example.test", "password": "integration-password"}, "", "")
	expect(t, response, 200)
	cookies := response.Result().Cookies()
	return cookies[0].Name + "=" + cookies[0].Value
}
func (f affiliateSettlementFixture) purchase(t *testing.T, n int) (int64, int64) {
	t.Helper()
	pid := product(t, f.db, 10)
	version, itemID := add(t, f.h, f.customer, pid, 1)
	order := request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, f.customer, fmt.Sprintf("settlement-order-%d", n))
	expect(t, order, 201)
	invoiceID := int64(data(t, order)["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, f.customer, fmt.Sprintf("settlement-pay-%d", n)), 201)
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	return invoiceID, job.ServiceID
}
func (f affiliateSettlementFixture) withdraw(t *testing.T, amount int64, key string) int64 {
	t.Helper()
	response := request(f.h, "POST", "/api/v1/affiliate/withdrawals", store.AffiliateWithdrawalInput{AmountCents: amount, Method: "bank", Target: "6222021234567890123", Holder: "测试收款人"}, f.owner, key)
	expect(t, response, 201)
	return int64(data(t, response)["id"].(float64))
}
func (f affiliateSettlementFixture) review(t *testing.T, id int64, decision string, version int64, key string) {
	t.Helper()
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/affiliate/withdrawals/%d/review", id), store.AffiliateWithdrawalReview{Decision: decision, Note: "test review", ExpectedVersion: version}, f.admin, key), 200)
}
func settlementPayoutInput() store.AffiliatePayoutInput {
	return store.AffiliatePayoutInput{ExternalRef: "offline-ref-123", Channel: "test bank", ProofHash: strings.Repeat("a", 64), Note: "verified offline transfer", ExpectedVersion: 2, Confirmed: true}
}
func (f affiliateSettlementFixture) overview(t *testing.T, pending, available, reserved, paid int64) {
	t.Helper()
	out, err := f.db.AffiliateOverview(t.Context(), f.ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if out.PendingCents != pending || out.AvailableCents != available || out.ReservedCents != reserved || out.PaidCents != paid {
		t.Fatalf("overview=%+v expected=%d/%d/%d/%d", out, pending, available, reserved, paid)
	}
	var balance int64
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.ownerID).Scan(&balance); err != nil || balance != 0 {
		t.Fatalf("affiliate owner balance=%d err=%v", balance, err)
	}
}
func forceNextSettlement(t *testing.T, db *store.Store) {
	t.Helper()
	if _, err := db.Pool.Exec(t.Context(), "UPDATE affiliate_settlement_runs SET status='failed',lease_until=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
}

func TestAffiliateSettlementReleaseWithdrawReviewPayoutAndReplay(t *testing.T) {
	f := settlementFixture(t)
	invoice, _ := f.purchase(t, 1)
	var amount int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM affiliate_commission_entries WHERE invoice_id=$1", invoice).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if amount < 100 {
		t.Fatalf("fixture commission=%d", amount)
	}
	f.overview(t, amount, 0, 0, 0)
	if n, err := f.db.SettleAffiliateCommissions(t.Context(), 1); err != nil || n != 1 {
		t.Fatalf("release=%d err=%v", n, err)
	}
	f.overview(t, 0, amount, 0, 0)
	id := f.withdraw(t, 50, "settlement-withdraw-one")
	f.overview(t, 0, amount-50, 50, 0)
	for _, kind := range []string{"withdrawals", "attributions"} {
		page := request(f.h, "GET", "/api/v1/admin/affiliate/"+kind+"/page?limit=1&search=affiliate", nil, f.admin, "")
		expect(t, page, 200)
		result := data(t, page)
		if result["total"].(float64) != 1 || len(result["items"].([]any)) != 1 {
			t.Fatalf("%s page=%v", kind, result)
		}
		if kind == "withdrawals" && (result["pending_count"].(float64) != 1 || strings.Contains(page.Body.String(), "6222021234567890123")) {
			t.Fatal("withdrawal page count or masking invalid")
		}
		next := request(f.h, "GET", "/api/v1/admin/affiliate/"+kind+"/page?limit=1&offset=1&search=affiliate", nil, f.admin, "")
		expect(t, next, 200)
		if data(t, next)["total"].(float64) != 1 || len(data(t, next)["items"].([]any)) != 0 {
			t.Fatalf("%s offset page=%s", kind, next.Body.String())
		}
	}
	replay := request(f.h, "POST", "/api/v1/affiliate/withdrawals", store.AffiliateWithdrawalInput{AmountCents: 50, Method: "bank", Target: "6222021234567890123", Holder: "测试收款人"}, f.owner, "settlement-withdraw-one")
	expect(t, replay, 200)
	if int64(data(t, replay)["id"].(float64)) != id {
		t.Fatal("withdrawal replay changed ID")
	}
	var sealed []byte
	var snapshot string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT payout_target_ciphertext,payout_target_snapshot::text FROM affiliate_withdrawals WHERE id=$1", id).Scan(&sealed, &snapshot); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed)+snapshot, "6222021234567890123") || len(sealed) == 0 {
		t.Fatal("plaintext payout target retained")
	}
	detail := request(f.h, "GET", fmt.Sprintf("/api/v1/affiliate/withdrawals/%d", id), nil, f.owner, "")
	expect(t, detail, 200)
	if strings.Contains(detail.Body.String(), "6222021234567890123") {
		t.Fatal("detail leaks target")
	}
	_, outsider := user(t, f.db, f.h, "withdrawal-outsider", "customer")
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/affiliate/withdrawals/%d", id), nil, outsider, ""), 404)
	targetPath := fmt.Sprintf("/api/v1/admin/affiliate/withdrawals/%d/target", id)
	expect(t, request(f.h, "POST", targetPath, map[string]any{"reason": "offline payout"}, f.admin, ""), 409)
	f.review(t, id, "approved", 1, "settlement-review-one")
	expect(t, request(f.h, "POST", targetPath, map[string]any{"reason": " "}, f.admin, ""), 409)
	_, viewer := user(t, f.db, f.h, "withdrawal-target-viewer", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['affiliates.view'] WHERE id=(SELECT admin_role_id FROM users WHERE username='withdrawal-target-viewer')"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", targetPath, map[string]any{"reason": "offline payout"}, viewer, ""), 403)
	target := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/affiliate/withdrawals/%d/target", id), map[string]any{"reason": "offline payout"}, f.admin, "")
	expect(t, target, 200)
	if target.Header().Get("Cache-Control") != "no-store" || data(t, target)["target"] != "6222021234567890123" {
		t.Fatal("target access invalid")
	}
	var targetAudits int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='affiliate.withdrawal.target_read' AND entity_id=$1 AND payload->>'reason'='offline payout'", fmt.Sprint(id)).Scan(&targetAudits); err != nil || targetAudits != 1 {
		t.Fatalf("target audit count=%d err=%v", targetAudits, err)
	}
	path := fmt.Sprintf("/api/v1/admin/affiliate/withdrawals/%d/payout", id)
	input := settlementPayoutInput()
	expect(t, request(f.h, "POST", path, input, f.admin, "settlement-payout-one"), 200)
	expect(t, request(f.h, "POST", targetPath, map[string]any{"reason": "offline payout"}, f.admin, ""), 409)
	expect(t, request(f.h, "POST", path, input, f.admin, "settlement-payout-one"), 200)
	expect(t, request(f.h, "POST", path, input, f.admin, "settlement-payout-other"), 409)
	f.overview(t, 0, amount-50, 0, 50)
	var payouts int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM affiliate_payouts WHERE withdrawal_id=$1", id).Scan(&payouts); err != nil || payouts != 1 {
		t.Fatalf("payouts=%d err=%v", payouts, err)
	}
}

func TestAffiliateConcurrentWithdrawalAndRejection(t *testing.T) {
	f := settlementFixture(t)
	invoice, _ := f.purchase(t, 1)
	if _, err := f.db.SettleAffiliateCommissions(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	var amount int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM affiliate_commission_entries WHERE invoice_id=$1", invoice).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			response := request(f.h, "POST", "/api/v1/affiliate/withdrawals", store.AffiliateWithdrawalInput{AmountCents: amount, Method: "alipay", Target: "affiliate@example.test", Holder: "holder"}, f.owner, fmt.Sprintf("concurrent-withdraw-%d", i))
			codes <- response.Code
		}(i)
	}
	wg.Wait()
	close(codes)
	created, conflict := 0, 0
	for code := range codes {
		if code == 201 {
			created++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatalf("unexpected withdrawal status=%d", code)
		}
	}
	if created != 1 || conflict != 1 {
		t.Fatalf("created=%d conflicts=%d", created, conflict)
	}
	var id int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM affiliate_withdrawals").Scan(&id); err != nil {
		t.Fatal(err)
	}
	f.overview(t, 0, 0, amount, 0)
	f.review(t, id, "rejected", 1, "concurrent-reject")
	f.review(t, id, "rejected", 1, "concurrent-reject")
	f.overview(t, 0, amount, 0, 0)
}

func TestAffiliateRefundReversesPendingAvailableReservedAndPaid(t *testing.T) {
	for _, stage := range []string{"pending", "available", "reserved", "paid"} {
		t.Run(stage, func(t *testing.T) {
			f := settlementFixture(t)
			invoice, _ := f.purchase(t, 1)
			var amount int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM affiliate_commission_entries WHERE invoice_id=$1", invoice).Scan(&amount); err != nil {
				t.Fatal(err)
			}
			var id int64
			if stage != "pending" {
				if _, err := f.db.SettleAffiliateCommissions(t.Context(), 100); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "reserved" || stage == "paid" {
				id = f.withdraw(t, amount, "refund-withdraw")
			}
			if stage == "paid" {
				f.review(t, id, "approved", 1, "refund-review")
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/affiliate/withdrawals/%d/payout", id), settlementPayoutInput(), f.admin, "refund-payout"), 200)
			}
			path := fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice)
			expect(t, request(f.h, "POST", path, map[string]any{}, f.admin, "affiliate-refund-one"), 201)
			expect(t, request(f.h, "POST", path, map[string]any{}, f.admin, "affiliate-refund-one"), 200)
			if stage == "reserved" {
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/affiliate/withdrawals/%d/review", id), store.AffiliateWithdrawalReview{Decision: "approved", Note: "invalid earnings", ExpectedVersion: 1}, f.admin, "refund-invalid-review"), 409)
				f.review(t, id, "rejected", 1, "refund-reject")
				f.overview(t, 0, 0, 0, 0)
			} else if stage == "paid" {
				f.overview(t, 0, 0, 0, amount)
				var debt int64
				if err := f.db.Pool.QueryRow(t.Context(), "SELECT sum(outstanding_cents) FROM affiliate_clawbacks").Scan(&debt); err != nil || debt != amount {
					t.Fatalf("debt=%d err=%v", debt, err)
				}
				job, err := f.db.ClaimProvisionJob(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
					t.Fatal(err)
				}
				next, _ := f.purchase(t, 2)
				forceNextSettlement(t, f.db)
				if _, err = f.db.SettleAffiliateCommissions(t.Context(), 100); err != nil {
					t.Fatal(err)
				}
				var nextAmount int64
				if err = f.db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM affiliate_commission_entries WHERE invoice_id=$1", next).Scan(&nextAmount); err != nil {
					t.Fatal(err)
				}
				f.overview(t, 0, max(int64(0), nextAmount-amount), 0, amount)
			} else {
				f.overview(t, 0, 0, 0, 0)
			}
		})
	}
}

func TestAffiliateReleaseHoldsRecoveryAndRollback(t *testing.T) {
	f := settlementFixture(t)
	invoice, sid := f.purchase(t, 1)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status='suspended' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	if n, err := f.db.SettleAffiliateCommissions(t.Context(), 1); err != nil || n != 0 {
		t.Fatalf("held release n=%d err=%v", n, err)
	}
	// A held first candidate must not starve a later eligible commission.
	next, _ := f.purchase(t, 2)
	forceNextSettlement(t, f.db)
	if n, err := f.db.SettleAffiliateCommissions(t.Context(), 1); err != nil || n != 1 {
		t.Fatalf("held starvation n=%d err=%v", n, err)
	}
	var available int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT available_cents FROM affiliate_commission_entries WHERE invoice_id=$1", next).Scan(&available); err != nil || available == 0 {
		t.Fatalf("later entry available=%d err=%v", available, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	forceNextSettlement(t, f.db)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION fail_affiliate_release() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='released' THEN RAISE EXCEPTION 'fixture release failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_affiliate_release BEFORE INSERT ON affiliate_commission_events FOR EACH ROW EXECUTE FUNCTION fail_affiliate_release()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SettleAffiliateCommissions(t.Context(), 100); err == nil {
		t.Fatal("event failure accepted")
	}
	var pending int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT pending_cents FROM affiliate_commission_entries WHERE invoice_id=$1", invoice).Scan(&pending); err != nil || pending == 0 {
		t.Fatalf("release rollback pending=%d err=%v", pending, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `DROP TRIGGER fail_affiliate_release ON affiliate_commission_events`); err != nil {
		t.Fatal(err)
	}
	if n, err := f.db.SettleAffiliateCommissions(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("failed run recovery n=%d err=%v", n, err)
	}
}
