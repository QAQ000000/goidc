package httpapi

import (
	"encoding/json"
	"fmt"
	"github.com/codex/zjmfgo/db/migrations"
	"github.com/codex/zjmfgo/internal/store"
	"math"
	"strings"
	"sync"
	"testing"
)

func renewLimited(t *testing.T, f recurringPurchase, key string, want int64) int64 {
	t.Helper()
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.sid), map[string]any{}, f.customer, key)
	expect(t, w, 201)
	if data(t, w)["amount_cents"] != float64(want) {
		t.Fatal("wrong renewal amount", data(t, w))
	}
	return int64(data(t, w)["invoice_id"].(float64))
}

func TestLimitedRenewalExternalSettlementConsumesOnce(t *testing.T) {
	f := renewalPurchaseFixture(t, "fixed", 500, 1)
	iid := renewLimited(t, f, "limited-gateway-renew", 734)
	intent := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", iid), map[string]any{"provider": "mock"}, f.customer, "limited-gateway-intent")
	expect(t, intent, 201)
	pid := data(t, intent)["provider_payment_id"]
	body, _ := json.Marshal(map[string]any{"event_id": "limited-gateway-event", "payment_id": pid, "invoice_id": iid, "amount_cents": 734, "status": "succeeded"})
	expect(t, signedWebhook(f.h, body, paymentSignature(body)), 201)
	expect(t, signedWebhook(f.h, body, paymentSignature(body)), 200)
	limitedUsed(t, f, 1)
	renewLimited(t, f, "limited-post-gateway", 1234)
}

func TestLimitedRenewalMigrationPreservesSettledAndPendingHistory(t *testing.T) {
	f := recurringPurchaseFixture(t, 10)
	first := renewLimited(t, f, "limited-migration-paid", 1111)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", first), map[string]any{}, f.customer, "limited-migration-pay"), 201)
	pending := renewLimited(t, f, "limited-migration-pending", 1111)
	// Restore the pre064 columns and original062 constraints in this test schema.
	tx, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `DROP FUNCTION service_renewal_discount(bigint,integer,bigint,integer,bigint);
 ALTER TABLE promo_codes DROP COLUMN recurring_limit;
 ALTER TABLE order_items DROP COLUMN renewal_fixed_discount_cents,DROP COLUMN renewal_discount_limit;
 ALTER TABLE services DROP COLUMN renewal_fixed_discount_cents,DROP COLUMN renewal_discount_limit,DROP COLUMN renewal_discounts_used;
 ALTER TABLE invoice_items DROP COLUMN renewal_fixed_discount_cents,DROP COLUMN renewal_discount_limit,DROP COLUMN renewal_discount_ordinal;
 DELETE FROM schema_migrations WHERE version='064_limited_renewal_benefits.sql'`); err != nil {
		t.Fatal(err)
	}
	original, err := migrations.Files.ReadFile("062_recurring_promotions.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range strings.Split(string(original), ";") {
		if pos := strings.Index(segment, "ADD CONSTRAINT"); pos >= 0 {
			var table string
			for _, name := range []string{"order_items", "services", "invoice_items"} {
				if strings.Contains(segment, "ALTER TABLE "+name) {
					table = name
				}
			}
			if table == "" {
				t.Fatal("unknown constraint")
			}
			if _, err = tx.Exec(t.Context(), "ALTER TABLE "+table+" "+segment[pos:]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	limitedUsed(t, f, 1)
	var ordinal int64
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT renewal_discount_ordinal FROM invoice_items WHERE invoice_id=$1", pending).Scan(&ordinal); err != nil || ordinal != 2 {
		t.Fatal("pending migration slot", ordinal, err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", pending), map[string]any{}, f.customer, "limited-migrated-pay"), 201)
	limitedUsed(t, f, 2)
}

func TestLimitedRenewalSQLPreviewMatchesQuote(t *testing.T) {
	db, _ := testServer(t)
	for _, b := range []store.RenewalBenefit{{Percent: 99, Code: "P"}, {FixedCents: math.MaxInt64, Code: "F"}, {FixedCents: 500, Limit: 1, Used: 1, Code: "F"}} {
		for _, base := range []int64{0, 1, 1234, math.MaxInt64} {
			want, _, _, err := b.Quote(base)
			if err != nil {
				t.Fatal(err)
			}
			var got int64
			if err = db.Pool.QueryRow(t.Context(), "SELECT service_renewal_discount($1,$2,$3,$4,$5)", base, b.Percent, b.FixedCents, b.Limit, b.Used).Scan(&got); err != nil || got != want {
				t.Fatal("SQL/Go differs", base, b, got, want, err)
			}
		}
	}
}
func limitedUsed(t *testing.T, f recurringPurchase, want int64) {
	t.Helper()
	var used int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT renewal_discounts_used FROM services WHERE id=$1", f.sid).Scan(&used); err != nil || used != want {
		t.Fatal("benefit counter", used, want, err)
	}
}
func TestLimitedRenewalFixedManualAutoExhaustionAndSibling(t *testing.T) {
	f := renewalPurchaseFixture(t, "fixed", 500, 2)
	limitedUsed(t, f, 0)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE promo_codes SET active=false,value=900,recurring_limit=99 WHERE code='FOREVER'"); err != nil {
		t.Fatal(err)
	}
	iid := renewLimited(t, f, "limited-first-renew", 734)
	limitedUsed(t, f, 0)
	invoice := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", iid), nil, f.customer, ""))
	item := invoice["items"].([]any)[0].(map[string]any)
	if item["renewal_fixed_discount_cents"] != float64(500) || item["renewal_discount_ordinal"] != float64(1) || item["renewal_discount_limit"] != float64(2) || invoice["promo_code"] != "FOREVER" || invoice["discount_cents"] != float64(500) {
		t.Fatal("fixed snapshot", invoice)
	}
	// Insufficient balance cannot consume the reserved slot.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=0 WHERE id=$1", f.uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{}, f.customer, "limited-empty-pay"), 409)
	limitedUsed(t, f, 0)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=10000 WHERE id=$1", f.uid); err != nil {
		t.Fatal(err)
	}
	payPath := fmt.Sprintf("/api/v1/invoices/%d/pay", iid)
	expect(t, request(f.h, "POST", payPath, map[string]any{}, f.customer, "limited-first-pay"), 201)
	expect(t, request(f.h, "POST", payPath, map[string]any{}, f.customer, "limited-first-pay"), 200)
	limitedUsed(t, f, 1)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET auto_renew=true,next_due_at=now()+interval '1 hour' WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 1 {
		t.Fatal("auto renewal", n, err)
	}
	limitedUsed(t, f, 2)
	var autoAmount, autoOrdinal int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT i.amount_cents,ii.renewal_discount_ordinal FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id WHERE i.idempotency_key LIKE 'auto-renew-%'").Scan(&autoAmount, &autoOrdinal); err != nil || autoAmount != 734 || autoOrdinal != 2 {
		t.Fatal("auto amount/ordinal", autoAmount, autoOrdinal, err)
	}
	d := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	if d["renewal_amount_cents"] != float64(1234) || d["renewal_discounts_used"] != float64(2) {
		t.Fatal("exhausted preview", d)
	}
	iid = renewLimited(t, f, "limited-exhausted-renew", 1234)
	var ordinal, discount int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT renewal_discount_ordinal,renewal_discount_cents FROM invoice_items WHERE invoice_id=$1", iid).Scan(&ordinal, &discount); err != nil || ordinal != 0 || discount != 0 {
		t.Fatal("exhausted snapshot", ordinal, discount, err)
	}
	var siblingUsed, siblingFixed, siblingLimit int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT renewal_discounts_used,renewal_fixed_discount_cents,renewal_discount_limit FROM services WHERE order_id=$1 AND id<>$2", f.orderID, f.sid).Scan(&siblingUsed, &siblingFixed, &siblingLimit); err != nil || siblingUsed != 0 || siblingFixed != 500 || siblingLimit != 2 {
		t.Fatal("sibling terms/counter", siblingUsed, siblingFixed, siblingLimit, err)
	}
}

func TestLimitedRenewalZeroRefundAndConcurrentPayment(t *testing.T) {
	for _, kind := range []string{"percent", "fixed"} {
		t.Run(kind, func(t *testing.T) {
			value := int64(100)
			if kind == "fixed" {
				value = 99999
			}
			f := renewalPurchaseFixture(t, kind, value, 1)
			iid := renewLimited(t, f, "limited-zero-renew", 0)
			var wg sync.WaitGroup
			codes := make(chan int, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					codes <- request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{}, f.customer, "limited-zero-pay").Code
				}(i)
			}
			wg.Wait()
			close(codes)
			seen := map[int]int{}
			for code := range codes {
				seen[code]++
			}
			if seen[201] != 1 || seen[200] != 1 {
				t.Fatal("payment replay", seen)
			}
			limitedUsed(t, f, 1)
			refund := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", iid), map[string]any{}, f.root, "limited-zero-refund")
			expect(t, refund, 201)
			if data(t, refund)["amount_cents"] != float64(0) {
				t.Fatal("gross refund", data(t, refund))
			}
			limitedUsed(t, f, 1)
			renewLimited(t, f, "limited-after-refund", 1234)
		})
	}
}

func TestLimitedRenewalAuditRollbackAndValidation(t *testing.T) {
	f := renewalPurchaseFixture(t, "fixed", 500, 1)
	iid := renewLimited(t, f, "limited-audit-renew", 734)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_limited_pay() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='invoice.paid' THEN RAISE EXCEPTION 'reject limited pay'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_limited_pay BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_limited_pay()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{}, f.customer, "limited-audit-pay"), 500)
	limitedUsed(t, f, 0)
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_limited_pay"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{}, f.customer, "limited-audit-pay"), 201)
	limitedUsed(t, f, 1)
	for i, in := range []map[string]any{
		{"recurring": false, "recurring_limit": 1}, {"recurring": true, "recurring_limit": -1}, {"recurring": true, "recurring_limit": 10001},
	} {
		in["code"] = fmt.Sprintf("INVALID%d", i)
		in["discount_type"] = "fixed"
		in["value"] = 500
		w := request(f.h, "POST", "/api/v1/admin/promo-codes", in, f.root, "")
		if w.Code < 400 {
			t.Fatal("invalid limit accepted", in)
		}
	}
}
