package httpapi

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func productChangeTarget(t *testing.T, f upgradeFixture, manual bool) (int64, int64) {
	t.Helper()
	input := productInput("upgrade-target", nil)
	input.Name = "高性能服务器"
	input.PriceCents = 4000
	input.SetupFeeCents = 50000
	stock := 1
	input.Stock = &stock
	if !manual {
		input.ProvisionMode = "simulation"
	}
	pid := managedProductID(t, f.h, f.root, input)
	gid := configGroup(t, f.h, f.root, pid, "disk", true, []store.ProductOptionInput{{Label: "NVMe", Value: "nvme", PriceDeltaCents: 500, SetupDeltaCents: 9000, Active: enabled()}})
	oid := configOption(t, f.h, f.root, pid, gid, "nvme")
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/upgrade-targets", f.pid), store.ProductUpgradeTargetsInput{ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), Targets: []store.ProductUpgradeTarget{{ProductID: pid}}}, f.root, ""), 200)
	return pid, oid
}

func productChangeQuote(t *testing.T, f upgradeFixture, target, option int64) map[string]any {
	t.Helper()
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	in := store.ProductChangeInput{TargetProductID: target, TargetRevision: *configRevision(t, f.h, f.root, target), ConfigurationChangeInput: store.ConfigurationChangeInput{ExpectedVersion: int64(detail["version"].(float64)), ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), OptionIDs: []int64{option}}}
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/product-quotes", f.sid), in, f.customer, "")
	expect(t, w, 201)
	return data(t, w)
}

func TestProductChangeUpgradeOnlyPromoSettlesOnce(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	target, option := productChangeTarget(t, f, false)
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "UPGRADE500", "discount_type": "fixed", "value": 500, "upgrade_only": true, "max_uses": 1, "once_per_customer": true}, f.root, "upgrade-promo-create"), 201)
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	in := store.ProductChangeInput{TargetProductID: target, TargetRevision: *configRevision(t, f.h, f.root, target), PromoCode: "UPGRADE500", ConfigurationChangeInput: store.ConfigurationChangeInput{ExpectedVersion: int64(detail["version"].(float64)), ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), OptionIDs: []int64{option}}}
	quote := data(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/product-quotes", f.sid), in, f.customer, ""))
	if quote["upgrade_promo_code"] != "UPGRADE500" || quote["upgrade_discount_cents"] != float64(500) || quote["upgrade_base_cents"].(float64) <= 500 || quote["amount_cents"].(float64) != quote["upgrade_base_cents"].(float64)-500 {
		t.Fatal("upgrade promo quote", quote)
	}
	change := f.submit(t, quote, "upgrade-promo-submit")
	var used int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT used_count FROM promo_codes WHERE code='UPGRADE500'").Scan(&used); err != nil || used != 0 {
		t.Fatal("pending upgrade consumed promo", used, err)
	}
	invoice := int64(change["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "upgrade-promo-pay"), 201)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "upgrade-promo-pay"), 200)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT used_count FROM promo_codes WHERE code='UPGRADE500'").Scan(&used); err != nil || used != 1 {
		t.Fatal("upgrade promo settlement count", used, err)
	}
	var redemptions int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM promo_upgrade_redemptions").Scan(&redemptions); err != nil || redemptions != 1 {
		t.Fatal("upgrade promo redemption", redemptions, err)
	}
	item := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", invoice), nil, f.customer, ""))["items"].([]any)[0].(map[string]any)
	if item["upgrade_promo_code"] != "UPGRADE500" || item["upgrade_discount_cents"] != float64(500) || item["upgrade_base_cents"] == nil {
		t.Fatal("upgrade invoice snapshot", item)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "BADUPGRADE", "discount_type": "percent", "value": 10, "upgrade_only": true, "recurring": true}, f.root, ""), 409)
}

func TestProductChangeDeliveryContractInventoryAndRenewal(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprint(manual), func(t *testing.T) {
			f := configurationUpgradeFixture(t, manual)
			pid, oid := productChangeTarget(t, f, manual)
			group := serviceGroup(t, f.h, f.root, "升级目标分组")
			assignNav(t, f.h, f.root, pid, &group)
			if _, err := f.db.Pool.Exec(t.Context(), "INSERT INTO service_navigation_preferences(user_id,group_id,visible) VALUES($1,$2,false)", f.uid, group); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET stock=0 WHERE id=$1", f.pid); err != nil {
				t.Fatal(err)
			}
			var before []byte
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT to_jsonb(oi) FROM order_items oi WHERE order_id=$1", f.orderID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			quote := productChangeQuote(t, f, pid, oid)
			if quote["new_recurring_cents"] != float64(4500) || quote["amount_cents"].(float64) > 2000 || quote["change_type"] != "product" {
				t.Fatal("wrong product upgrade price", quote)
			}
			change := f.submit(t, quote, "product-upgrade-submit")
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-changes", f.sid), map[string]any{"quote_id": quote["id"]}, f.customer, "product-upgrade-submit"), 200)
			var stock int
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT stock FROM products WHERE id=$1", pid).Scan(&stock); err != nil || stock != 0 {
				t.Fatal("target not reserved exactly once", stock, err)
			}
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(change["invoice_id"].(float64))), map[string]any{}, f.customer, "product-upgrade-pay"), 201)
			detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
			if detail["product_id"] != float64(f.pid) || detail["recurring_price_cents"] != float64(2500) {
				t.Fatal("payment switched contract", detail)
			}
			job, err := f.db.ClaimProvisionJob(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			if manual {
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", f.sid), store.ManualCompletion{JobID: job.ID, Note: "product resource verified"}, f.root, ""), 200)
			}
			detail = data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
			if detail["product_id"] != float64(pid) || detail["name"] != "高性能服务器" || detail["recurring_price_cents"] != float64(4500) || detail["option_ids"].([]any)[0] != float64(oid) {
				t.Fatal("product contract not delivered", detail)
			}
			var after []byte
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT to_jsonb(oi) FROM order_items oi WHERE order_id=$1", f.orderID).Scan(&after); err != nil || !bytes.Equal(before, after) {
				t.Fatal("original order mutated", err)
			}
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT stock FROM products WHERE id=$1", f.pid).Scan(&stock); err != nil || stock != 1 {
				t.Fatal("old allocation not released", stock, err)
			}
			if serviceNav(t, f.h, f.customer, group)["visible"] != true {
				t.Fatal("upgrade did not restore target navigation")
			}
			renewal := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.sid), map[string]any{}, f.customer, "product-upgrade-renew")
			expect(t, renewal, 201)
			if data(t, renewal)["amount_cents"] != float64(4500) {
				t.Fatal("renewal used old product")
			}
		})
	}
}

func TestProductChangeCancelRefundAndReservationRaces(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	pid, oid := productChangeTarget(t, f, false)
	quote := productChangeQuote(t, f, pid, oid)
	change := f.submit(t, quote, "product-cancel-submit")
	cancelPath := fmt.Sprintf("/api/v1/services/%d/configuration-changes/%d", f.sid, int64(change["id"].(float64)))
	expect(t, request(f.h, "DELETE", cancelPath, nil, f.customer, ""), 200)
	expect(t, request(f.h, "DELETE", cancelPath, nil, f.customer, ""), 200)
	var stock int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT stock FROM products WHERE id=$1", pid).Scan(&stock); err != nil || stock != 1 {
		t.Fatal("cancel stock release", stock, err)
	}
	quote = productChangeQuote(t, f, pid, oid)
	change = f.submit(t, quote, "product-refund-submit")
	invoice := int64(change["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "product-refund-pay"), 201)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "product-refund-before-work"), 201)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT stock FROM products WHERE id=$1", pid).Scan(&stock); err != nil || stock != 1 {
		t.Fatal("refund stock release", stock, err)
	}
	// Two independent services compete for one target unit.
	originalSID := f.sid
	var secondSID int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,provision_mode,status,next_due_at,unit_number)
	 SELECT user_id,order_id,order_item_id,product_id,name,'second-upgrade-service',billing_cycle,provision_mode,status,next_due_at,2 FROM services WHERE id=$1 RETURNING id`, f.sid).Scan(&secondSID); err != nil {
		t.Fatal(err)
	}
	q1 := productChangeQuote(t, f, pid, oid)
	f.sid = secondSID
	q2 := productChangeQuote(t, f, pid, oid)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	wg.Add(2)
	for index, entry := range []struct {
		sid int64
		q   map[string]any
	}{{originalSID, q1}, {secondSID, q2}} {
		go func() {
			defer wg.Done()
			codes <- request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-changes", entry.sid), map[string]any{"quote_id": entry.q["id"]}, f.customer, fmt.Sprintf("product-stock-race-%d", index)).Code
		}()
	}
	wg.Wait()
	close(codes)
	created, conflicts := 0, 0
	for code := range codes {
		switch code {
		case 201:
			created++
		case 409:
			conflicts++
		default:
			t.Fatalf("stock race status %d", code)
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatal("stock oversold", created, conflicts)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT stock FROM products WHERE id=$1", pid).Scan(&stock); err != nil || stock != 0 {
		t.Fatal("stock race inventory", stock, err)
	}
}

func TestProductChangePermissionsStaleAndAuditRollback(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	pid, oid := productChangeTarget(t, f, false)
	_, reader := user(t, f.db, f.h, "product-upgrade-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view','services.view'] WHERE name='product-upgrade-reader-role'"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/products/%d/upgrade-targets", f.pid)
	policy := store.ProductUpgradeTargetsInput{ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), Targets: []store.ProductUpgradeTarget{{ProductID: pid}}}
	expect(t, request(f.h, "GET", path, nil, reader, ""), 200)
	expect(t, request(f.h, "PUT", path, policy, reader, ""), 403)
	expect(t, request(f.h, "PUT", path, policy, f.customer, ""), 401)
	invalid := policy
	invalid.Targets = []store.ProductUpgradeTarget{{ProductID: f.pid}}
	expect(t, request(f.h, "PUT", path, invalid, f.root, ""), 409)
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d/product-upgrade-options", f.sid), nil, f.other, ""), 404)
	quote := productChangeQuote(t, f, pid, oid)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET revision=revision+1 WHERE id=$1", pid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-changes", f.sid), map[string]any{"quote_id": quote["id"]}, f.customer, "product-stale-submit"), 409)
	quote = productChangeQuote(t, f, pid, oid)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION product_change_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('service.configuration.submitted','service.simulation.completed') THEN RAISE EXCEPTION 'product change audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER product_change_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION product_change_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-changes", f.sid), map[string]any{"quote_id": quote["id"]}, f.customer, "product-audit-submit"), 500)
	var stock, count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT stock FROM products WHERE id=$1", pid).Scan(&stock); err != nil || stock != 1 {
		t.Fatal("audit failure reserved stock", stock, err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoices").Scan(&count); err != nil || count != 1 {
		t.Fatal("audit failure created invoice", count, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER product_change_reject_audit"); err != nil {
		t.Fatal(err)
	}
	change := f.submit(t, quote, "product-audit-submit")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(change["invoice_id"].(float64))), map[string]any{}, f.customer, "product-audit-pay"), 201)
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events ENABLE TRIGGER product_change_reject_audit"); err != nil {
		t.Fatal(err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("delivery audit failure ignored")
	}
	var delivered int64
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT product_id FROM services WHERE id=$1", f.sid).Scan(&delivered); err != nil || delivered != f.pid {
		t.Fatal("failed delivery switched product", delivered, err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER product_change_reject_audit"); err != nil {
		t.Fatal(err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
}

func TestProductChangeDowngradeCreditAndTerminationStock(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	pid, oid := productChangeTarget(t, f, false)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE product_prices SET price_cents=1000 WHERE product_id=$1", pid); err != nil {
		t.Fatal(err)
	}
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	in := store.ProductChangeInput{TargetProductID: pid, TargetRevision: *configRevision(t, f.h, f.root, pid), ConfigurationChangeInput: store.ConfigurationChangeInput{ExpectedVersion: int64(detail["version"].(float64)), ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), OptionIDs: []int64{oid}}}
	quotePath := fmt.Sprintf("/api/v1/services/%d/product-quotes", f.sid)
	expect(t, request(f.h, "POST", quotePath, in, f.customer, ""), 409)
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/upgrade-targets", f.pid), store.ProductUpgradeTargetsInput{ExpectedRevision: in.ExpectedRevision, Targets: []store.ProductUpgradeTarget{{ProductID: pid, AllowDowngrade: true}}}, f.root, ""), 200)
	quote := productChangeQuote(t, f, pid, oid)
	if quote["new_recurring_cents"] != float64(1500) || quote["amount_cents"] != float64(0) || quote["credit_cents"].(float64) <= 0 {
		t.Fatal("downgrade contract", quote)
	}
	change := f.submit(t, quote, "product-downgrade-submit")
	var balance int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(change["invoice_id"].(float64))), map[string]any{}, f.customer, "product-downgrade-pay"), 201)
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&after); err != nil || after-balance != int64(quote["credit_cents"].(float64)) {
		t.Fatal("product downgrade credit", after, balance, err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("duplicate delivery accepted")
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/actions", f.sid), map[string]any{"action": "terminate"}, f.customer, "product-upgrade-terminate"), 202)
	job, err = f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	var stock int
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT stock FROM products WHERE id=$1", pid).Scan(&stock); err != nil || stock != 1 {
		t.Fatal("termination released wrong inventory", stock, err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("duplicate termination accepted")
	}
	var originalInvoice int64
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT id FROM invoices WHERE order_id=$1", f.orderID).Scan(&originalInvoice); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", originalInvoice), map[string]any{}, f.root, "product-original-refund-after-terminate"), 201)
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT stock FROM products WHERE id=$1", pid).Scan(&stock); err != nil || stock != 1 {
		t.Fatal("original refund released target twice", stock, err)
	}
}

func TestProductChangeIncompatiblePolicyRemoval(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	pid, _ := productChangeTarget(t, f, false)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET provision_mode='manual',revision=revision+1 WHERE id=$1", pid); err != nil {
		t.Fatal(err)
	}
	policyPath := fmt.Sprintf("/api/v1/admin/products/%d/upgrade-targets", f.pid)
	policy := data(t, request(f.h, "GET", policyPath, nil, f.root, ""))
	candidates := policy["candidates"].([]any)
	if len(candidates) != 1 || candidates[0].(map[string]any)["compatible"] != false {
		t.Fatal("incompatible configured target missing", policy)
	}
	options := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d/product-upgrade-options", f.sid), nil, f.customer, ""))
	if len(options["products"].([]any)) != 0 {
		t.Fatal("incompatible customer target exposed", options)
	}
	expect(t, request(f.h, "PUT", policyPath, store.ProductUpgradeTargetsInput{ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), Targets: []store.ProductUpgradeTarget{{ProductID: pid}}}, f.root, ""), 409)
	expect(t, request(f.h, "PUT", policyPath, store.ProductUpgradeTargetsInput{ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), Targets: []store.ProductUpgradeTarget{}}, f.root, ""), 200)
}
