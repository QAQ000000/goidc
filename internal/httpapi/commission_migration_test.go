package httpapi

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

// Rebuild only the two new migrations inside testServer's disposable schema.
// Fixtures deliberately retain the pre-existing financial/commission records:
// upgrading a schema must establish turnover, never re-earn historical money.
func TestCommissionMigrationsUpgradeHistorical133(t *testing.T) {
	f := settlementFixture(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := f.db.Pool.Exec(t.Context(), sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	paid, _ := f.purchase(t, 501)
	exec(`UPDATE affiliate_rule_sets SET default_rules='{"first_purchase":{"rate_bps":0},"reorder":{"rate_bps":0}}'`)
	zero, _ := f.purchase(t, 502)
	var zeroCount int
	if e := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM affiliate_commission_entries WHERE invoice_id=$1`, zero).Scan(&zeroCount); e != nil || zeroCount != 0 {
		t.Fatalf("zero-rate fixture generated commissions: %d %v", zeroCount, e)
	}
	exec(`UPDATE affiliate_rule_sets SET default_rules='{"first_purchase":{"rate_bps":1000},"reorder":{"rate_bps":1000}}'`)
	repaid, _ := f.purchase(t, 503)
	outstanding, _ := f.purchase(t, 504)
	refunded, _ := f.purchase(t, 505)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", refunded), map[string]any{}, f.admin, "migration-old-refund"), 201)
	unproven, _ := f.purchase(t, 506)
	// No attribution row exists for this otherwise ordinary, genuinely paid order.
	noAtCustomer, noAtCookie := user(t, f.db, f.h, "migration-no-attribution", "customer")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", noAtCustomer), map[string]any{"amount_cents": 10000}, f.admin, "migration-no-at-topup"), 201)
	pid := product(t, f.db, 2)
	version, item := add(t, f.h, noAtCookie, pid, 1)
	order := request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, noAtCookie, "migration-no-at-order")
	expect(t, order, 201)
	noAt := int64(data(t, order)["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", noAt), map[string]any{}, noAtCookie, "migration-no-at-pay"), 201)
	// Establish a historical August billing cohort. Proven attribution and rules
	// predate it; the unproven invoice alone predates its customer's attribution.
	exec(`UPDATE affiliate_rule_sets SET effective_from='2026-01-01T00:00:00+08'`)
	exec(`UPDATE affiliate_attributions SET registered_at='2026-02-01T00:00:00+08' WHERE user_id=$1`, f.customerID)
	exec(`UPDATE invoices SET created_at='2026-08-01T00:00:00+08',paid_at=CASE WHEN status='paid' THEN '2026-08-25T10:00:00+08'::timestamptz ELSE NULL END WHERE id=ANY($1::bigint[])`, []int64{paid, zero, repaid, outstanding, refunded, unproven, noAt})
	exec(`UPDATE invoices SET created_at='2026-01-15T00:00:00+08' WHERE id=$1`, unproven)
	var actor int64
	if e := f.db.Pool.QueryRow(t.Context(), `SELECT id FROM administrators WHERE username='settlement-root'`).Scan(&actor); e != nil {
		t.Fatal(e)
	}
	exec(`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,status,version,expected_version,idempotency_key) SELECT id,user_id,amount_cents,CASE WHEN id=$1 THEN 0 ELSE amount_cents-333 END,CASE WHEN id=$1 THEN 'repaid' ELSE 'outstanding' END,CASE WHEN id=$1 THEN 3 ELSE 2 END,1,'migration-credit-'||id FROM invoices WHERE id=ANY($2::bigint[])`, repaid, []int64{repaid, outstanding})
	exec(`INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after) SELECT id,user_id,$3::bigint,333,'migration-partial-'||id,'2026-08-31T23:30:00+08'::timestamptz,'migration-partial-'||id,1,2,amount_cents-333 FROM invoices WHERE id=ANY($2::bigint[]) UNION ALL SELECT id,user_id,$3::bigint,amount_cents-333,'migration-final-'||id,'2026-09-02T09:00:00+08'::timestamptz,'migration-final-'||id,2,3,0 FROM invoices WHERE id=$1`, repaid, []int64{repaid, outstanding}, actor)
	snapshot := func() []byte {
		t.Helper()
		var raw []byte
		if e := f.db.Pool.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY id),'[]') FROM affiliate_commission_entries c`).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		return raw
	}
	before := snapshot()
	if bytes.Equal(before, []byte("[]")) {
		t.Fatal("fixture lacks old commission snapshots")
	}
	// Simulate an installed schema-133 database, without touching any other schema.
	exec(`DROP TRIGGER sales_invoice_reversal ON invoices; DROP TRIGGER sales_order_reversal ON orders; DROP FUNCTION reverse_sales_invoice(); DROP FUNCTION reverse_sales_order(); DROP TABLE sales_settlement_items; DROP TABLE sales_settlements; DROP TABLE affiliate_turnover_entries; DELETE FROM schema_migrations WHERE version IN ('134_affiliate_turnover.sql','135_sales_settlements.sql')`)
	for attempt := 0; attempt < 2; attempt++ {
		if e := f.db.Migrate(t.Context()); e != nil {
			t.Fatalf("upgrade/repeat %d: %v", attempt, e)
		}
		if e := f.db.CheckSchema(t.Context()); e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(before, snapshot()) {
			t.Fatal("134/135 recomputed or otherwise changed historical commissions")
		}
		var count int
		var total int64
		if e := f.db.Pool.QueryRow(t.Context(), `SELECT count(*),COALESCE(sum(basis_cents),0) FROM affiliate_turnover_entries`).Scan(&count, &total); e != nil {
			t.Fatal(e)
		}
		if count != 3 || total != 3999 {
			t.Fatalf("historical turnover count=%d amount=%d; expected paid+zero+repaid once", count, total)
		}
		for _, invoice := range []int64{paid, zero, repaid} {
			var basis int64
			var received time.Time
			if e := f.db.Pool.QueryRow(t.Context(), `SELECT basis_cents,paid_at FROM affiliate_turnover_entries WHERE invoice_id=$1`, invoice).Scan(&basis, &received); e != nil {
				t.Fatal(e)
			}
			expected := "2026-08-25T02:00:00Z"
			if invoice == repaid {
				expected = "2026-09-02T01:00:00Z"
			}
			if basis != 1333 || received.UTC().Format(time.RFC3339) != expected {
				t.Fatalf("invoice %d basis=%d received=%s", invoice, basis, received)
			}
		}
		for _, invoice := range []int64{outstanding, refunded, unproven, noAt} {
			var exists bool
			if e := f.db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM affiliate_turnover_entries WHERE invoice_id=$1)`, invoice).Scan(&exists); e != nil || exists {
				t.Fatalf("unqualified invoice %d backfilled: %v %v", invoice, exists, e)
			}
		}
		var august, september int64
		if e := f.db.Pool.QueryRow(t.Context(), `SELECT COALESCE(sum(basis_cents) FILTER(WHERE paid_at>='2026-08-01T00:00:00+08' AND paid_at<'2026-09-01T00:00:00+08'),0),COALESCE(sum(basis_cents) FILTER(WHERE paid_at>='2026-09-01T00:00:00+08' AND paid_at<'2026-10-01T00:00:00+08'),0) FROM affiliate_turnover_entries`).Scan(&august, &september); e != nil || august != 2666 || september != 1333 {
			t.Fatalf("month baseline August=%d September=%d err=%v", august, september, e)
		}
		var salesCount int
		if e := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM sales_settlements`).Scan(&salesCount); e != nil || salesCount != 0 {
			t.Fatalf("135 invented historical sales settlements: %d %v", salesCount, e)
		}
	}
}
