package httpapi

import (
	"fmt"
	"testing"
)

// The status column is a projection of the bucket columns; every lifecycle path
// must keep it in step within the same transaction.
const derivedCommissionStatusSQL = `CASE
 WHEN reversal_at IS NOT NULL AND reserved_cents+paid_cents>0 THEN 'clawback'
 WHEN reversal_at IS NOT NULL THEN 'reversed'
 WHEN pending_cents>0 THEN 'pending'
 WHEN reserved_cents>0 THEN 'reserved'
 WHEN available_cents>0 THEN 'available'
 WHEN paid_cents>0 THEN 'paid'
 ELSE 'reversed' END`

func assertCommissionStatusDerived(t *testing.T, f affiliateSettlementFixture, invoice int64) string {
	t.Helper()
	var status, derived string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT status,`+derivedCommissionStatusSQL+` FROM affiliate_commission_entries WHERE invoice_id=$1`, invoice).Scan(&status, &derived); err != nil {
		t.Fatal(err)
	}
	if status != derived {
		t.Fatalf("invoice %d status=%s but buckets derive %s", invoice, status, derived)
	}
	return status
}

// queueTurnover runs the payment-settlement hook in its own transaction, the
// same way the payment, credit-repayment and webhook callers invoke it.
func queueTurnover(t *testing.T, f affiliateSettlementFixture, invoice int64) {
	t.Helper()
	tx, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = f.db.QueueAffiliateCommissions(t.Context(), tx, invoice, 0); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// directInvoice inserts a settled, attributable invoice without touching the
// HTTP payment path so created_at/paid_at can model a historical cohort.
func directInvoice(t *testing.T, f affiliateSettlementFixture, uid int64, n int, created, paid string) int64 {
	t.Helper()
	var order, invoice int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES($1,$2,'paid',1333,$1,'fixture') RETURNING id`, fmt.Sprintf("turnover-direct-%d", n), uid).Scan(&order); err != nil {
		t.Fatal(err)
	}
	pid := product(t, f.db, 10)
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'turnover direct','monthly',1,1333,0,1333)`, order, pid); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,created_at,paid_at) VALUES($1,$2,$3,'paid',1333,$4::timestamptz,$5::timestamptz) RETURNING id`, fmt.Sprintf("turnover-direct-invoice-%d", n), uid, order, created, paid).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	return invoice
}

// TestAffiliateTurnoverRecordsInvoicesWithoutEffectiveRuleSet covers the fix for
// settled invoices that predate the first effective rule set: they contribute
// their net amount to the basis exactly once and only earn no commission.
func TestAffiliateTurnoverRecordsInvoicesWithoutEffectiveRuleSet(t *testing.T) {
	f := settlementFixture(t)
	body := map[string]any{"expected_version": 1, "product_rules": []any{}, "tier_rules": []any{
		map[string]any{"lower_cents": 0, "upper_cents": 1500, "rate_bps": 500, "amount_cents": 0},
		map[string]any{"lower_cents": 1500, "upper_cents": nil, "rate_bps": 2000, "amount_cents": 0},
	}}
	expect(t, request(f.h, "PUT", "/api/v1/admin/affiliate/rules", body, f.admin, "turnover-fix-rules"), 200)
	var ruleSetID int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT id FROM affiliate_rule_sets WHERE effective_to IS NULL`).Scan(&ruleSetID); err != nil {
		t.Fatal(err)
	}
	// The current rule set only becomes effective in June: the historical invoice
	// created in January has no rule set, while the July invoice is commissioned.
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE affiliate_rule_sets SET effective_from='2026-06-01T00:00:00+08' WHERE id=$1`, ruleSetID); err != nil {
		t.Fatal(err)
	}
	historical := directInvoice(t, f, f.customerID, 1, "2026-01-05T00:00:00+08", "2026-07-10T10:00:00+08")
	current := directInvoice(t, f, f.customerID, 2, "2026-07-01T00:00:00+08", "2026-07-10T10:00:00+08")
	queueTurnover(t, f, historical)
	var basis, recordedRuleSet int64
	var ruleSetNull bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT basis_cents,coalesce(rule_set_id,0),rule_set_id IS NULL FROM affiliate_turnover_entries WHERE invoice_id=$1`, historical).Scan(&basis, &recordedRuleSet, &ruleSetNull); err != nil {
		t.Fatalf("historical invoice missing from turnover basis: %v", err)
	}
	if basis != 1333 || !ruleSetNull || recordedRuleSet != 0 {
		t.Fatalf("historical basis=%d rule_set=%d null=%v", basis, recordedRuleSet, ruleSetNull)
	}
	var commissions int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM affiliate_commission_entries WHERE invoice_id=$1`, historical).Scan(&commissions); err != nil || commissions != 0 {
		t.Fatalf("historical invoice earned %d commissions: %v", commissions, err)
	}
	queueTurnover(t, f, current)
	// The historical basis lifts the monthly turnover to the second tier, the
	// under-payment this fix prevents.
	var amount, turnover int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT amount_cents,(rule_snapshot->>'turnover_cents')::bigint FROM affiliate_commission_entries WHERE invoice_id=$1`, current).Scan(&amount, &turnover); err != nil {
		t.Fatal(err)
	}
	if amount != 266 || turnover != 2666 {
		t.Fatalf("current commission amount=%d turnover=%d; want 266/2666", amount, turnover)
	}
	// Replaying both settlement hooks changes nothing: one basis row per invoice.
	queueTurnover(t, f, historical)
	queueTurnover(t, f, current)
	var entries, entriesCurrent int
	var total int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM affiliate_turnover_entries),(SELECT count(*) FROM affiliate_commission_entries WHERE invoice_id=$1),(SELECT COALESCE(sum(basis_cents),0) FROM affiliate_turnover_entries)`, current).Scan(&entries, &entriesCurrent, &total); err != nil {
		t.Fatal(err)
	}
	if entries != 2 || entriesCurrent != 1 || total != 2666 {
		t.Fatalf("replay duplicated turnover entries=%d current=%d total=%d", entries, entriesCurrent, total)
	}
}

// TestAffiliateTurnoverNeverDoubleCountsCommissionedInvoices replays a normally
// commissioned invoice: the basis row and the commission row both stay unique.
func TestAffiliateTurnoverNeverDoubleCountsCommissionedInvoices(t *testing.T) {
	f := settlementFixture(t)
	invoice, _ := f.purchase(t, 1)
	var entries, commissions int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM affiliate_turnover_entries WHERE invoice_id=$1),(SELECT count(*) FROM affiliate_commission_entries WHERE invoice_id=$1)`, invoice).Scan(&entries, &commissions); err != nil || entries != 1 || commissions == 0 {
		t.Fatalf("first settlement entries=%d commissions=%d err=%v", entries, commissions, err)
	}
	queueTurnover(t, f, invoice)
	queueTurnover(t, f, invoice)
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM affiliate_turnover_entries WHERE invoice_id=$1),(SELECT count(*) FROM affiliate_commission_entries WHERE invoice_id=$1)`, invoice).Scan(&entries, &commissions); err != nil || entries != 1 || commissions == 0 {
		t.Fatalf("replay entries=%d commissions=%d err=%v", entries, commissions, err)
	}
}

// TestAffiliateCommissionStatusTracksBucketsOnAllPaths locks the invariant the
// customer console relies on: release, withdrawal and reversal keep status equal
// to the value derived from the bucket columns.
func TestAffiliateCommissionStatusTracksBucketsOnAllPaths(t *testing.T) {
	for _, stage := range []string{"pending", "available", "reserved", "paid"} {
		t.Run(stage, func(t *testing.T) {
			f := settlementFixture(t)
			invoice, _ := f.purchase(t, 1)
			if got := assertCommissionStatusDerived(t, f, invoice); got != "pending" {
				t.Fatalf("after settlement status=%s want pending", got)
			}
			var amount int64
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT amount_cents FROM affiliate_commission_entries WHERE invoice_id=$1`, invoice).Scan(&amount); err != nil {
				t.Fatal(err)
			}
			var withdrawalID int64
			if stage != "pending" {
				if _, err := f.db.SettleAffiliateCommissions(t.Context(), 100); err != nil {
					t.Fatal(err)
				}
				if got := assertCommissionStatusDerived(t, f, invoice); got != "available" {
					t.Fatalf("after release status=%s want available", got)
				}
			}
			if stage == "reserved" || stage == "paid" {
				withdrawalID = f.withdraw(t, amount, "status-path-withdraw")
				if got := assertCommissionStatusDerived(t, f, invoice); got != "reserved" {
					t.Fatalf("after reservation status=%s want reserved", got)
				}
			}
			if stage == "paid" {
				f.review(t, withdrawalID, "approved", 1, "status-path-review")
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/affiliate/withdrawals/%d/payout", withdrawalID), settlementPayoutInput(), f.admin, "status-path-payout"), 200)
				if got := assertCommissionStatusDerived(t, f, invoice); got != "paid" {
					t.Fatalf("after payout status=%s want paid", got)
				}
			}
			// The customer console must see the same value the buckets derive.
			listed := listData(t, request(f.h, "GET", "/api/v1/affiliate/commissions", nil, f.owner, ""))
			if len(listed) != 1 || listed[0]["status"] != assertCommissionStatusDerived(t, f, invoice) {
				t.Fatalf("console commissions=%v", listed)
			}
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.admin, "status-path-refund"), 201)
			want := "reversed"
			if stage == "reserved" || stage == "paid" {
				want = "clawback"
			}
			if got := assertCommissionStatusDerived(t, f, invoice); got != want {
				t.Fatalf("after refund status=%s want %s", got, want)
			}
			if stage == "reserved" {
				// Rejecting the withdrawal releases the reservation into reversed.
				f.review(t, withdrawalID, "rejected", 1, "status-path-reject")
				if got := assertCommissionStatusDerived(t, f, invoice); got != "reversed" {
					t.Fatalf("after rejection status=%s want reversed", got)
				}
			}
			listed = listData(t, request(f.h, "GET", "/api/v1/affiliate/commissions", nil, f.owner, ""))
			if len(listed) != 1 || listed[0]["status"] != "reversed" && listed[0]["status"] != "clawback" {
				t.Fatalf("console status after refund=%v", listed)
			}
		})
	}
}
