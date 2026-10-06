package httpapi

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

type upgradeFixture struct {
	db                                        *store.Store
	h                                         http.Handler
	root, customer, other                     string
	uid, pid, gid, small, large, sid, orderID int64
}

func TestCheckboxAddOnConfigurationQuote(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	group := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", f.pid), store.ProductOptionGroupInput{
		Name: "security", Slug: "security", OptionType: 3, Active: enabled(), ExpectedRevision: configRevision(t, f.h, f.root, f.pid),
		Options: []store.ProductOptionInput{{Label: "安全附加项", Value: "security-on", PriceDeltaCents: 400, Active: enabled()}},
	}, f.root, "")
	expect(t, group, 201)
	addon := configOption(t, f.h, f.root, f.pid, int64(data(t, group)["id"].(float64)), "security-on")
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	path := fmt.Sprintf("/api/v1/services/%d/configuration-quotes", f.sid)
	quote := func(ids []int64) map[string]any {
		w := request(f.h, "POST", path, store.ConfigurationChangeInput{OptionIDs: ids, ExpectedVersion: int64(detail["version"].(float64)), ExpectedRevision: *configRevision(t, f.h, f.root, f.pid)}, f.customer, "")
		expect(t, w, 201)
		return data(t, w)
	}
	checked := quote([]int64{f.small, addon})
	if checked["new_recurring_cents"] != float64(2900) || len(checked["config_snapshot"].([]any)) != 2 {
		t.Fatal("checked add-on missing from quote", checked)
	}
	unchanged := request(f.h, "POST", path, store.ConfigurationChangeInput{OptionIDs: []int64{f.small}, ExpectedVersion: int64(detail["version"].(float64)), ExpectedRevision: *configRevision(t, f.h, f.root, f.pid)}, f.customer, "")
	expect(t, unchanged, 409)
	if !strings.Contains(unchanged.Body.String(), "configuration_unchanged") {
		t.Fatal("unchecked add-on changed service configuration", unchanged.Body.String())
	}
}

func TestConfigurationUpgradeAuditRollbackAndPaymentCancellationRace(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	quote := f.quote(t, f.large)
	path := fmt.Sprintf("/api/v1/services/%d/configuration-changes", f.sid)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION upgrade_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('service.configuration.submitted','invoice.paid','service.simulation.completed') THEN RAISE EXCEPTION 'upgrade audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER upgrade_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION upgrade_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, map[string]any{"quote_id": quote["id"]}, f.customer, "audit-upgrade-submit"), 500)
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoices").Scan(&count); err != nil || count != 1 {
		t.Fatal("failed submission left invoice", count, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER upgrade_reject_audit"); err != nil {
		t.Fatal(err)
	}
	change := f.submit(t, quote, "audit-upgrade-submit")
	invoice := int64(change["invoice_id"].(float64))
	payPath := fmt.Sprintf("/api/v1/invoices/%d/pay", invoice)
	var balance int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events ENABLE TRIGGER upgrade_reject_audit"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", payPath, map[string]any{}, f.customer, "audit-upgrade-pay"), 500)
	var status string
	var after int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT i.status,u.balance_cents FROM invoices i JOIN users u ON u.id=i.user_id WHERE i.id=$1", invoice).Scan(&status, &after); err != nil || status != "unpaid" || after != balance {
		t.Fatal("payment rollback failed", status, after, err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE action='upgrade'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed payment queued job", count, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER upgrade_reject_audit"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", payPath, map[string]any{}, f.customer, "audit-upgrade-pay"), 201)
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events ENABLE TRIGGER upgrade_reject_audit"); err != nil {
		t.Fatal(err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("delivery audit failure ignored")
	}
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT coalesce(s.recurring_price_cents,oi.price_cents) FROM services s JOIN order_items oi ON oi.id=s.order_item_id WHERE s.id=$1", f.sid).Scan(&after); err != nil || after != 2500 {
		t.Fatal("failed delivery changed contract", after, err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER upgrade_reject_audit"); err != nil {
		t.Fatal(err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	quote = f.quote(t, f.small)
	change = f.submit(t, quote, "race-upgrade-submit")
	invoice = int64(change["invoice_id"].(float64))
	results := make(chan int, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		results <- request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "race-upgrade-pay").Code
	}()
	go func() {
		defer wg.Done()
		results <- request(f.h, "DELETE", fmt.Sprintf("%s/%d", path, int64(change["id"].(float64))), nil, f.customer, "").Code
	}()
	wg.Wait()
	close(results)
	winners, conflicts := 0, 0
	for code := range results {
		switch code {
		case 200, 201:
			winners++
		case 409:
			conflicts++
		default:
			t.Fatalf("unexpected race status %d", code)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatal("payment/cancel race", winners, conflicts)
	}
	var changeStatus string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT i.status,c.status FROM invoices i JOIN service_configuration_changes c ON c.invoice_id=i.id WHERE i.id=$1", invoice).Scan(&status, &changeStatus); err != nil {
		t.Fatal(err)
	}
	if (status != "paid" || changeStatus != "queued") && (status != "cancelled" || changeStatus != "cancelled") {
		t.Fatal("inconsistent race result", status, changeStatus)
	}
}

func configurationUpgradeFixture(t *testing.T, manual bool) upgradeFixture {
	t.Helper()
	db, h := testServer(t)
	return setupConfigurationUpgradeFixture(t, db, h, manual)
}

func setupConfigurationUpgradeFixture(t *testing.T, db *store.Store, h http.Handler, manual bool) upgradeFixture {
	t.Helper()
	_, root := user(t, db, h, "upgrade-root", "super_admin")
	uid, customer := user(t, db, h, "upgrade-customer", "customer")
	_, other := user(t, db, h, "upgrade-other", "customer")
	product := productInput("upgrade-product", nil)
	if !manual {
		product.ProvisionMode = "simulation"
	}
	pid := managedProductID(t, h, root, product)
	gid := configGroup(t, h, root, pid, "memory", true, []store.ProductOptionInput{{Label: "4GB", Value: "small", PriceDeltaCents: 500, Active: enabled()}, {Label: "8GB", Value: "large", PriceDeltaCents: 1000, SetupDeltaCents: 9000, Active: enabled()}})
	small, large := configOption(t, h, root, pid, gid, "small"), configOption(t, h, root, pid, gid, "large")
	v, item := configuredCart(t, h, customer, pid, []int64{small}, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "upgrade-initial-order")
	expect(t, order, 201)
	orderID := int64(data(t, order)["order_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), map[string]int64{"amount_cents": 100000}, root, "upgrade-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(data(t, order)["invoice_id"].(float64))), map[string]any{}, customer, "upgrade-initial-pay"), 201)
	var sid int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", orderID).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	job, err := db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	if manual {
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", sid), store.ManualCompletion{JobID: job.ID, UpstreamID: "resource-upgrade", Note: "resource verified"}, root, ""), 200)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=date_trunc('second',now())+interval '15 days' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/configuration-upgrade-policy", pid), store.ConfigurationUpgradePolicy{Enabled: true, AllowDowngrade: true, ExpectedRevision: *configRevision(t, h, root, pid)}, root, ""), 200)
	return upgradeFixture{db, h, root, customer, other, uid, pid, gid, small, large, sid, orderID}
}

func (f upgradeFixture) quote(t *testing.T, option int64) map[string]any {
	t.Helper()
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-quotes", f.sid), store.ConfigurationChangeInput{OptionIDs: []int64{option}, ExpectedVersion: int64(detail["version"].(float64)), ExpectedRevision: *configRevision(t, f.h, f.root, f.pid)}, f.customer, "")
	expect(t, w, 201)
	return data(t, w)
}

func (f upgradeFixture) submit(t *testing.T, quote map[string]any, key string) map[string]any {
	t.Helper()
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-changes", f.sid), map[string]any{"quote_id": quote["id"]}, f.customer, key)
	expect(t, w, 201)
	return data(t, w)
}

func TestConfigurationUpgradePaymentDeliveryAndDowngradeCredit(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual=%v", manual), func(t *testing.T) {
			f := configurationUpgradeFixture(t, manual)
			var original []byte
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT config_snapshot FROM order_items WHERE order_id=$1", f.orderID).Scan(&original); err != nil {
				t.Fatal(err)
			}
			quote := f.quote(t, f.large)
			if quote["old_recurring_cents"] != float64(2500) || quote["new_recurring_cents"] != float64(3000) || quote["amount_cents"].(float64) <= 0 || quote["amount_cents"].(float64) > 500 {
				t.Fatal("quote includes setup or wrong recurring", quote)
			}
			change := f.submit(t, quote, "upgrade-submit")
			path := fmt.Sprintf("/api/v1/services/%d", f.sid)
			expect(t, request(f.h, "POST", path+"/configuration-changes", map[string]any{"quote_id": quote["id"]}, f.customer, "upgrade-submit"), 200)
			expect(t, request(f.h, "POST", path+"/renew", map[string]any{}, f.customer, "upgrade-renew-pending"), 409)
			expect(t, request(f.h, "POST", path+"/actions", map[string]any{"action": "terminate"}, f.customer, "upgrade-terminate-pending"), 409)
			expect(t, request(f.h, "POST", path+"/cancellation", map[string]any{"type": "immediate", "reason": "testing"}, f.customer, "upgrade-cancel-pending"), 409)
			invoice := int64(change["invoice_id"].(float64))
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.other, "upgrade-other-pay"), 404)
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "upgrade-pay"), 201)
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "upgrade-pay"), 200)
			detail := data(t, request(f.h, "GET", path, nil, f.customer, ""))
			if detail["recurring_price_cents"] != float64(2500) {
				t.Fatal("payment applied configuration before delivery")
			}
			job, err := f.db.ClaimProvisionJob(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			if manual {
				detail = data(t, request(f.h, "GET", path, nil, f.customer, ""))
				if detail["recurring_price_cents"] != float64(2500) {
					t.Fatal("manual waiting applied price")
				}
				completion := store.ManualCompletion{JobID: job.ID, Note: "upgrade resource confirmed"}
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", f.sid), completion, f.root, ""), 200)
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", f.sid), completion, f.root, ""), 200)
			}
			detail = data(t, request(f.h, "GET", path, nil, f.customer, ""))
			if detail["recurring_price_cents"] != float64(3000) || detail["config_snapshot"].([]any)[0].(map[string]any)["id"] != float64(f.large) {
				t.Fatal("new service contract not applied", detail)
			}
			var after []byte
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT config_snapshot FROM order_items WHERE order_id=$1", f.orderID).Scan(&after); err != nil || !bytes.Equal(original, after) {
				t.Fatal("order history changed", err)
			}
			quote = f.quote(t, f.small)
			if quote["amount_cents"] != float64(0) || quote["credit_cents"].(float64) <= 0 || quote["new_recurring_cents"] != float64(2500) {
				t.Fatal("downgrade quote", quote)
			}
			change = f.submit(t, quote, "downgrade-submit")
			var balanceBefore int64
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&balanceBefore); err != nil {
				t.Fatal(err)
			}
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(change["invoice_id"].(float64))), map[string]any{}, f.customer, "downgrade-pay"), 201)
			job, err = f.db.ClaimProvisionJob(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			if manual {
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", f.sid), store.ManualCompletion{JobID: job.ID, Note: "downgrade resource confirmed"}, f.root, ""), 200)
			}
			var balance, credits int64
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&balance); err != nil || balance != balanceBefore+int64(quote["credit_cents"].(float64)) {
				t.Fatal("downgrade credit wrong", balance, err)
			}
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM balance_ledger WHERE user_id=$1 AND reference_type='configuration_change'", f.uid).Scan(&credits); err != nil || credits != 1 {
				t.Fatal("credit ledger duplicate", credits, err)
			}
			renew := request(f.h, "POST", path+"/renew", map[string]any{}, f.customer, "renew-after-downgrade")
			expect(t, renew, 201)
			if data(t, renew)["amount_cents"] != float64(2500) {
				t.Fatal("renewal lost current contract")
			}
		})
	}
}

func TestConfigurationUpgradeOwnershipStaleCancelAndRefund(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	path := fmt.Sprintf("/api/v1/services/%d", f.sid)
	expect(t, request(f.h, "GET", path+"/upgrade-options", nil, f.other, ""), 404)
	quote := f.quote(t, f.large)
	expect(t, request(f.h, "POST", path+"/configuration-changes", map[string]any{"quote_id": quote["id"]}, f.other, "stolen-upgrade"), 404)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET revision=revision+1 WHERE id=$1", f.pid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/configuration-changes", map[string]any{"quote_id": quote["id"]}, f.customer, "stale-upgrade"), 409)
	quote = f.quote(t, f.large)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE service_configuration_changes SET expires_at=now()-interval '1 second' WHERE id=$1", int64(quote["id"].(float64))); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path+"/configuration-changes", map[string]any{"quote_id": quote["id"]}, f.customer, "expired-upgrade"), 409)
	quote = f.quote(t, f.large)
	change := f.submit(t, quote, "cancel-upgrade")
	cancelPath := fmt.Sprintf("%s/configuration-changes/%d", path, int64(change["id"].(float64)))
	expect(t, request(f.h, "DELETE", cancelPath, nil, f.other, ""), 404)
	expect(t, request(f.h, "DELETE", cancelPath, nil, f.customer, ""), 200)
	expect(t, request(f.h, "DELETE", cancelPath, nil, f.customer, ""), 200)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(change["invoice_id"].(float64))), map[string]any{}, f.customer, "cancelled-upgrade-pay"), 409)
	quote = f.quote(t, f.large)
	change = f.submit(t, quote, "refund-upgrade")
	invoice := int64(change["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "refundable-upgrade-pay"), 201)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "refund-upgrade-before-work"), 201)
	if _, err := f.db.ClaimProvisionJob(t.Context()); err == nil {
		t.Fatal("refunded upgrade job remained queued")
	}
	_ = f.quote(t, f.large)
}

func TestConfigurationUpgradePreservesUnchangedPricesAndRetries(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	// Add a second configuration to an existing contract, then increase its
	// catalog price. Changing memory must retain the contracted second price.
	extra := configGroup(t, f.h, f.root, f.pid, "backup", false, []store.ProductOptionInput{{Label: "Backup", Value: "backup", PriceDeltaCents: 400, Active: enabled()}})
	extraOption := configOption(t, f.h, f.root, f.pid, extra, "backup")
	var snapshot []store.OptionSnapshot
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT config_snapshot FROM order_items WHERE order_id=$1", f.orderID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot = append(snapshot, store.OptionSnapshot{ID: extraOption, GroupID: extra, GroupName: "backup", GroupSlug: "backup", Label: "Backup", Value: "backup", PriceDeltaCents: 400, Quantity: 1})
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET config_snapshot=$2,option_ids=$3,recurring_price_cents=2900 WHERE id=$1", f.sid, snapshot, []int64{f.small, extraOption}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE product_options SET price_delta_cents=9000 WHERE id=$1", extraOption); err != nil {
		t.Fatal(err)
	}
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	input := store.ConfigurationChangeInput{OptionIDs: []int64{f.large, extraOption}, ExpectedVersion: int64(detail["version"].(float64)), ExpectedRevision: *configRevision(t, f.h, f.root, f.pid)}
	quoteResponse := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/configuration-quotes", f.sid), input, f.customer, "")
	expect(t, quoteResponse, 201)
	quote := data(t, quoteResponse)
	if quote["new_recurring_cents"] != float64(3400) {
		t.Fatal("unchanged group repriced", quote)
	}
	change := f.submit(t, quote, "retry-upgrade-submit")
	invoice := int64(change["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "retry-upgrade-pay"), 201)
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET status='failed',locked_at=NULL WHERE id=$1", job.ID); err != nil {
		t.Fatal(err)
	}
	retryPath := fmt.Sprintf("/api/v1/admin/services/%d/configuration-changes/%d/retry", f.sid, int64(change["id"].(float64)))
	expect(t, request(f.h, "POST", retryPath, map[string]any{}, f.customer, ""), 403)
	readerID, reader := user(t, f.db, f.h, "upgrade-retry-reader", "admin")
	if _, err = f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE name='upgrade-retry-reader-role'"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", retryPath, map[string]any{}, reader, ""), 403)
	if err = f.db.RetryConfigurationChange(t.Context(), readerID, f.sid, int64(change["id"].(float64))); err == nil {
		t.Fatal("store retry accepted readonly actor")
	}
	expect(t, request(f.h, "POST", retryPath, map[string]any{}, f.root, ""), 200)
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("old job lease accepted")
	}
	next, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != job.ID || next.Attempts <= job.Attempts {
		t.Fatal("retry reset job fencing", next)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *next); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "completed-upgrade-refund"), 409)
	if err = f.db.ProcessProvisionJob(t.Context(), *next); err == nil {
		t.Fatal("completed job executed twice")
	}
}

func TestConfigurationUpgradePolicyAndSelectionRestrictions(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	_, reader := user(t, f.db, f.h, "upgrade-policy-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view'] WHERE name='upgrade-policy-reader-role'"); err != nil {
		t.Fatal(err)
	}
	policyPath := fmt.Sprintf("/api/v1/admin/products/%d/configuration-upgrade-policy", f.pid)
	revision := *configRevision(t, f.h, f.root, f.pid)
	policy := store.ConfigurationUpgradePolicy{Enabled: true, AllowDowngrade: false, ExpectedRevision: revision}
	expect(t, request(f.h, "PUT", policyPath, policy, reader, ""), 403)
	expect(t, request(f.h, "PUT", policyPath, policy, f.root, ""), 200)
	expect(t, request(f.h, "PUT", policyPath, policy, f.root, ""), 409)
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", f.sid), nil, f.customer, ""))
	input := store.ConfigurationChangeInput{OptionIDs: []int64{f.large}, ExpectedVersion: int64(detail["version"].(float64))}
	quotePath := fmt.Sprintf("/api/v1/services/%d/configuration-quotes", f.sid)
	for _, mutation := range []string{
		"UPDATE products SET config_upgrade_enabled=false WHERE id=$1",
		"UPDATE product_option_groups SET upgrade_enabled=false WHERE product_id=$1",
		"UPDATE product_options SET upgrade_enabled=false WHERE value='large' AND group_id IN (SELECT id FROM product_option_groups WHERE product_id=$1)",
		"UPDATE product_options SET upgrade_enabled=false WHERE value='small' AND group_id IN (SELECT id FROM product_option_groups WHERE product_id=$1)",
		"UPDATE product_options SET hidden=true WHERE value='large' AND group_id IN (SELECT id FROM product_option_groups WHERE product_id=$1)",
	} {
		if _, err := f.db.Pool.Exec(t.Context(), mutation, f.pid); err != nil {
			t.Fatal(err)
		}
		input.ExpectedRevision = *configRevision(t, f.h, f.root, f.pid)
		expect(t, request(f.h, "POST", quotePath, input, f.customer, ""), 409)
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE products SET config_upgrade_enabled=true WHERE id=$1", f.pid); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE product_option_groups SET upgrade_enabled=true WHERE product_id=$1", f.pid); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE product_options SET upgrade_enabled=true,hidden=false WHERE group_id IN (SELECT id FROM product_option_groups WHERE product_id=$1)", f.pid); err != nil {
			t.Fatal(err)
		}
	}
	// A price decrease remains a downgrade even if it selects a different label.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE product_options SET price_delta_cents=100 WHERE id=$1", f.large); err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision = *configRevision(t, f.h, f.root, f.pid)
	expect(t, request(f.h, "POST", quotePath, input, f.customer, ""), 409)
}

func TestConfigurationUpgradeSubmitQuotedCancellationRace(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	path := fmt.Sprintf("/api/v1/services/%d/configuration-changes", f.sid)
	for attempt := 0; attempt < 8; attempt++ {
		quote := f.quote(t, f.large)
		id := int64(quote["id"].(float64))
		start := make(chan struct{})
		results := make(chan int, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			results <- request(f.h, "POST", path, map[string]any{"quote_id": id}, f.customer, fmt.Sprintf("quoted-race-%d", attempt)).Code
		}()
		go func() {
			defer wg.Done()
			<-start
			results <- request(f.h, "DELETE", fmt.Sprintf("%s/%d", path, id), nil, f.customer, "").Code
		}()
		close(start)
		wg.Wait()
		close(results)
		for code := range results {
			if code != 200 && code != 201 && code != 409 {
				t.Fatalf("unexpected quoted cancellation status %d", code)
			}
		}
		var cancelled, orphan bool
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT c.status='cancelled',i.id IS NOT NULL AND i.status<>'cancelled' FROM service_configuration_changes c LEFT JOIN invoices i ON i.id=c.invoice_id WHERE c.id=$1`, id).Scan(&cancelled, &orphan); err != nil || !cancelled || orphan {
			t.Fatal("quoted cancellation left payable invoice", cancelled, orphan, err)
		}
	}
}
