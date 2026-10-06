package httpapi

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func upgradePromoInput(t *testing.T, f upgradeFixture, target, option int64, code string) store.ProductChangeInput {
	t.Helper()
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	return store.ProductChangeInput{TargetProductID: target, TargetRevision: *configRevision(t, f.h, f.root, target), PromoCode: code, ConfigurationChangeInput: store.ConfigurationChangeInput{ExpectedVersion: int64(detail["version"].(float64)), ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), OptionIDs: []int64{option}}}
}

func TestUpgradePromoEligibilityAndSnapshots(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	target, option := productChangeTarget(t, f, false)
	path := fmt.Sprintf("/api/v1/services/%d/product-quotes", f.sid)
	for _, tc := range []struct {
		name   string
		fields map[string]any
	}{
		{"ordinary", map[string]any{"upgrade_only": false}},
		{"new", map[string]any{"only_new_customer": true}},
		{"cycle", map[string]any{"cycles": []string{"annually"}}},
		{"product", map[string]any{"applies_product_ids": []int64{f.pid}}},
		{"required", map[string]any{"requires_product_ids": []int64{f.pid}, "requires_existing": false}},
		{"expired", map[string]any{"expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339)}},
		{"future", map[string]any{"starts_at": time.Now().Add(time.Hour).Format(time.RFC3339)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code := "UP-" + tc.name
			input := map[string]any{"code": code, "discount_type": "percent", "value": 20, "upgrade_only": true}
			for key, value := range tc.fields {
				input[key] = value
			}
			expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", input, f.root, ""), 201)
			expect(t, request(f.h, "POST", path, upgradePromoInput(t, f, target, option, code), f.customer, ""), 409)
		})
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "UP-OK", "discount_type": "percent", "value": 20, "upgrade_only": true, "only_old_customer": true, "cycles": []string{"monthly"}, "applies_product_ids": []int64{target}, "requires_product_ids": []int64{f.pid}, "requires_existing": true}, f.root, ""), 201)
	q := data(t, request(f.h, "POST", path, upgradePromoInput(t, f, target, option, "up-ok"), f.customer, ""))
	base := int64(q["upgrade_base_cents"].(float64))
	if int64(q["upgrade_discount_cents"].(float64)) != base*20/100 {
		t.Fatal("percent rounding", q)
	}
	// A configured promo cannot silently succeed on a zero or negative delta.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE product_prices SET price_cents=2000 WHERE product_id=$1", target); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, upgradePromoInput(t, f, target, option, "UP-OK"), f.customer, ""), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE product_prices SET price_cents=4000 WHERE product_id=$1", target); err != nil {
		t.Fatal(err)
	}
	q = data(t, request(f.h, "POST", path, upgradePromoInput(t, f, target, option, "UP-OK"), f.customer, ""))
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE promo_codes SET active=false WHERE code='UP-OK'"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-changes", f.sid), map[string]any{"quote_id": q["id"]}, f.customer, "upgrade-rule-submit"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE promo_codes SET active=true WHERE code='UP-OK'"); err != nil {
		t.Fatal(err)
	}
	change := f.submit(t, q, "upgrade-rule-submit")
	invoice := int64(change["invoice_id"].(float64))
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE promo_codes SET value=30 WHERE code='UP-OK'"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "upgrade-rule-pay"), 409)
	item := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", invoice), nil, f.customer, ""))["items"].([]any)[0].(map[string]any)
	if item["upgrade_discount_cents"] != q["upgrade_discount_cents"] {
		t.Fatal("invoice snapshot changed", item)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE promo_codes SET value=20 WHERE code='UP-OK'"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_upgrade_payment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='invoice.paid' THEN RAISE EXCEPTION 'reject upgrade payment'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_upgrade_payment BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_upgrade_payment()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "upgrade-rule-pay"), 500)
	var used, redemptions, jobs int
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT used_count,(SELECT count(*) FROM promo_upgrade_redemptions),(SELECT count(*) FROM provision_jobs WHERE action='upgrade'),(SELECT status FROM invoices WHERE id=$1) FROM promo_codes WHERE code='UP-OK'`, invoice).Scan(&used, &redemptions, &jobs, &status); err != nil || used != 0 || redemptions != 0 || jobs != 0 || status != "unpaid" {
		t.Fatal("payment rollback", used, redemptions, jobs, status, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_upgrade_payment"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "upgrade-rule-pay"), 201)
}

func TestUpgradePromoConcurrentLimitAndCartIsolation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		maxUses int
		once    bool
	}{{"global_limit", 1, false}, {"once_per_customer", 0, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := configurationUpgradeFixture(t, false)
			target, option := productChangeTarget(t, f, false)
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET stock=2 WHERE id=$1", target); err != nil {
				t.Fatal(err)
			}
			var secondSID int64
			if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,provision_mode,status,next_due_at,unit_number) SELECT user_id,order_id,order_item_id,product_id,name,'second-promo-service',billing_cycle,provision_mode,status,next_due_at,2 FROM services WHERE id=$1 RETURNING id`, f.sid).Scan(&secondSID); err != nil {
				t.Fatal(err)
			}
			expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "UP-ZERO", "discount_type": "fixed", "value": 100000, "upgrade_only": true, "max_uses": tc.maxUses, "once_per_customer": tc.once}, f.root, ""), 201)
			version, _ := configuredCart(t, f.h, f.customer, f.pid, []int64{f.small}, 1)
			rejected := request(f.h, "POST", "/api/v1/cart/promo", map[string]any{"code": "UP-ZERO", "version": version}, f.customer, "")
			expect(t, rejected, 409)
			if !bytes.Contains(rejected.Body.Bytes(), []byte("promo_upgrade_only")) {
				t.Fatal("cart rejection reason", rejected.Body.String())
			}
			invoices := []int64{}
			for index, sid := range []int64{f.sid, secondSID} {
				copy := f
				copy.sid = sid
				q := data(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/product-quotes", sid), upgradePromoInput(t, copy, target, option, "UP-ZERO"), f.customer, ""))
				if q["amount_cents"] != float64(0) || q["upgrade_base_cents"] != q["upgrade_discount_cents"] {
					t.Fatal("fixed discount cap", q)
				}
				change := copy.submit(t, q, fmt.Sprintf("upgrade-cap-submit-%d", index))
				invoices = append(invoices, int64(change["invoice_id"].(float64)))
			}
			var wg sync.WaitGroup
			codes := make(chan int, 2)
			for index, invoice := range invoices {
				wg.Add(1)
				go func() {
					defer wg.Done()
					codes <- request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, fmt.Sprintf("upgrade-cap-pay-%d", index)).Code
				}()
			}
			wg.Wait()
			close(codes)
			created, conflicts := 0, 0
			for code := range codes {
				if code == 201 {
					created++
				} else if code == 409 {
					conflicts++
				} else {
					t.Fatalf("race status %d", code)
				}
			}
			if created != 1 || conflicts != 1 {
				t.Fatal("limit race", created, conflicts)
			}
			var used, redemptions, paid int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT used_count,(SELECT count(*) FROM promo_upgrade_redemptions),(SELECT count(*) FROM invoices WHERE id=ANY($1) AND status='paid') FROM promo_codes WHERE code='UP-ZERO'`, invoices).Scan(&used, &redemptions, &paid); err != nil || used != 1 || redemptions != 1 || paid != 1 {
				t.Fatal("limit settlement", used, redemptions, paid, err)
			}
		})
	}
}
