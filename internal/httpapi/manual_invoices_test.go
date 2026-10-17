package httpapi

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func manualInvoiceInput(uid int64) store.ManualInvoiceInput {
	return store.ManualInvoiceInput{UserID: uid, DueAt: time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC), Items: []store.ManualInvoiceItem{{Description: "人工技术服务", AmountCents: 3500}, {Description: "协商优惠", AmountCents: -500}}}
}

func manualInvoiceCreate(t *testing.T, h http.Handler, actor string, in store.ManualInvoiceInput, key string) int64 {
	t.Helper()
	w := request(h, "POST", "/api/v1/admin/invoices", in, actor, key)
	expect(t, w, 201)
	return int64(data(t, w)["invoice_id"].(float64))
}

func TestManualInvoiceLifecycleAndZeroAmount(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	var beforeBalance int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&beforeBalance); err != nil {
		t.Fatal(err)
	}
	in := manualInvoiceInput(f.uid)
	id := manualInvoiceCreate(t, f.h, f.root, in, "manual-create-invoice")
	path := fmt.Sprintf("/api/v1/invoices/%d", id)
	admin := fmt.Sprintf("/api/v1/admin/invoices/%d", id)
	expect(t, request(f.h, "POST", "/api/v1/admin/invoices", in, f.root, "manual-create-invoice"), 200)
	detail := data(t, request(f.h, "GET", path, nil, f.customer, ""))
	if detail["invoice_kind"] != "manual" || detail["version"] != float64(1) || detail["order_id"] != nil || detail["amount_cents"] != float64(3000) || len(detail["items"].([]any)) != 2 {
		t.Fatal("manual bill contract", detail)
	}
	expect(t, request(f.h, "GET", path, nil, f.other, ""), 404)
	in.ExpectedVersion = 1
	in.Items[0].AmountCents = 4000
	expect(t, request(f.h, "PUT", admin, in, f.root, "manual-edit-invoice"), 200)
	replay := data(t, request(f.h, "PUT", admin, in, f.root, "manual-edit-invoice"))
	if replay["replayed"] != true || replay["version"] != float64(2) || replay["amount_cents"] != float64(3500) {
		t.Fatal("edit retry contract", replay)
	}
	expect(t, request(f.h, "PUT", admin, in, f.root, "manual-stale-edit"), 409)
	in.ExpectedVersion = 2
	in.Items[0].AmountCents = 4500
	expect(t, request(f.h, "PUT", admin, in, f.root, "manual-edit-invoice"), 409)
	expect(t, request(f.h, "POST", path+"/pay", map[string]any{}, f.customer, "manual-no-version-pay"), 409)
	expect(t, request(f.h, "POST", path+"/pay", map[string]any{"expected_version": 1}, f.customer, "manual-stale-pay"), 409)
	expect(t, request(f.h, "POST", path+"/pay", map[string]any{"expected_version": 2}, f.customer, "manual-pay-invoice"), 201)
	detail = data(t, request(f.h, "GET", path, nil, f.customer, ""))
	if detail["status"] != "paid" || detail["version"] != float64(3) || len(detail["ledger"].([]any)) != 1 {
		t.Fatal("manual payment", detail)
	}
	expect(t, request(f.h, "PUT", admin, in, f.root, "manual-edit-paid"), 409)
	expect(t, request(f.h, "POST", admin+"/refund", map[string]any{}, f.root, "manual-refund-invoice"), 201)
	expect(t, request(f.h, "POST", admin+"/refund", map[string]any{}, f.root, "manual-refund-invoice"), 200)
	detail = data(t, request(f.h, "GET", path, nil, f.customer, ""))
	if detail["status"] != "refunded" || detail["version"] != float64(4) || len(detail["ledger"].([]any)) != 2 {
		t.Fatal("manual refund", detail)
	}
	var afterBalance int64
	var jobs int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents,(SELECT count(*) FROM provision_jobs) FROM users WHERE id=$1", f.uid).Scan(&afterBalance, &jobs); err != nil || afterBalance != beforeBalance || jobs != 1 {
		t.Fatal("manual bill changed resource jobs or balance", afterBalance, jobs, err)
	}
	in = manualInvoiceInput(f.uid)
	in.Items = []store.ManualInvoiceItem{{Description: "免费处理", AmountCents: 0}}
	id = manualInvoiceCreate(t, f.h, f.root, in, "manual-zero-create")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", id), map[string]any{"expected_version": 1}, f.customer, "manual-zero-pay"), 201)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", id), map[string]any{}, f.root, "manual-zero-refund"), 201)
}

func TestManualInvoiceCancellationPendingAndGatewaySnapshot(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	in := manualInvoiceInput(f.uid)
	id := manualInvoiceCreate(t, f.h, f.root, in, "manual-cancel-create")
	path := fmt.Sprintf("/api/v1/invoices/%d", id)
	admin := fmt.Sprintf("/api/v1/admin/invoices/%d", id)
	cancel := store.ManualInvoiceCancel{ExpectedVersion: 1, Reason: "客户取消服务项目"}
	expect(t, request(f.h, "POST", admin+"/cancel", cancel, f.root, "manual-cancel-key"), 200)
	expect(t, request(f.h, "POST", admin+"/cancel", cancel, f.root, "manual-cancel-key"), 200)
	expect(t, request(f.h, "POST", path+"/pay", map[string]any{}, f.customer, "manual-cancelled-pay"), 409)
	id = manualInvoiceCreate(t, f.h, f.root, in, "manual-gateway-create")
	path, admin = fmt.Sprintf("/api/v1/invoices/%d", id), fmt.Sprintf("/api/v1/admin/invoices/%d", id)
	expect(t, request(f.h, "POST", path+"/payments", map[string]any{"provider": "mock", "expected_version": 1}, f.customer, "manual-gateway-intent"), 201)
	in.ExpectedVersion = 1
	in.Items[0].AmountCents = 4000
	expect(t, request(f.h, "PUT", admin, in, f.root, "manual-pending-edit"), 409)
	expect(t, request(f.h, "POST", admin+"/cancel", cancel, f.root, "manual-pending-cancel"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET expires_at=now()-interval '1 minute' WHERE invoice_id=$1", id); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "PUT", admin, in, f.root, "manual-pending-edit"), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET status='failed' WHERE invoice_id=$1", id); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "PUT", admin, in, f.root, "manual-pending-edit"), 200)
	if _, err := f.db.CreatePaymentIntent(t.Context(), f.uid, id, "mock", "outdated-manual-intent", "", "manual-outdated-gateway", 1); err == nil {
		t.Fatal("stale gateway price accepted")
	}
	if _, err := f.db.CreatePaymentIntent(t.Context(), f.uid, id, "mock", "outdated-manual-quote", "", "manual-outdated-quote", 2, 1); err == nil {
		t.Fatal("gateway quoted before the invoice edit was accepted")
	}
	expect(t, request(f.h, "POST", path+"/payments", map[string]any{"provider": "mock", "expected_version": 1}, f.customer, "manual-stale-gateway"), 409)
	w := request(f.h, "POST", path+"/payments", map[string]any{"provider": "mock", "expected_version": 2}, f.customer, "manual-new-gateway")
	expect(t, w, 201)
	payment := data(t, w)["provider_payment_id"].(string)
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), "mock", "manual-gateway-event", payment, id, 3500, "succeeded", []byte(`{"status":"succeeded"}`)); err != nil {
		t.Fatal(err)
	}
	detail := data(t, request(f.h, "GET", path, nil, f.customer, ""))
	if detail["status"] != "paid" || detail["version"] != float64(3) || len(detail["ledger"].([]any)) != 0 {
		t.Fatal("manual gateway settlement", detail)
	}
	retry := request(f.h, "POST", path+"/payments", map[string]any{"provider": "mock", "expected_version": 2}, f.customer, "manual-new-gateway")
	expect(t, retry, 200)
	if data(t, retry)["checkout_url"] != data(t, w)["checkout_url"] || data(t, retry)["provider_payment_id"] != payment {
		t.Fatal("payment retry returned a different gateway checkout")
	}
	expect(t, request(f.h, "POST", admin+"/refund", map[string]any{}, f.root, "manual-external-refund"), 409)
}

func TestManualInvoicePermissionsValidationAndTransferReplay(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	staffID, staff := user(t, f.db, f.h, "manual-finance", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view'] WHERE name='manual-finance-role'"); err != nil {
		t.Fatal(err)
	}
	in := manualInvoiceInput(f.uid)
	expect(t, request(f.h, "GET", "/api/v1/admin/invoice-customers", nil, staff, ""), 403)
	expect(t, request(f.h, "POST", "/api/v1/admin/invoices", in, staff, "manual-denied-create"), 403)
	expect(t, request(f.h, "POST", "/api/v1/admin/invoices", in, f.customer, "manual-customer-create"), 403)
	expect(t, request(f.h, "POST", "/api/v1/admin/invoices", in, f.root, ""), 409)
	for _, invalid := range []store.ManualInvoiceInput{{UserID: f.uid, DueAt: in.DueAt}, {UserID: f.uid, DueAt: in.DueAt, Items: []store.ManualInvoiceItem{{Description: "负应收", AmountCents: -1}}}, {UserID: f.uid, DueAt: in.DueAt, Items: []store.ManualInvoiceItem{{Description: "溢出", AmountCents: 10000000001}}}} {
		expect(t, request(f.h, "POST", "/api/v1/admin/invoices", invalid, f.root, "manual-invalid-create"), 409)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view','invoices.manage'] WHERE name='manual-finance-role'"); err != nil {
		t.Fatal(err)
	}
	page := data(t, request(f.h, "GET", "/api/v1/admin/invoice-customers?q=upgrade-customer", nil, staff, ""))
	if page["total"] != float64(1) {
		t.Fatal("invoice customer picker", page)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/invoices", in, staff, "manual-scope-create"), 404)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", f.uid, staffID); err != nil {
		t.Fatal(err)
	}
	id := manualInvoiceCreate(t, f.h, staff, in, "manual-scope-create")
	expect(t, request(f.h, "POST", "/api/v1/admin/invoices", in, staff, "manual-scope-create"), 200)
	other := in
	other.UserID++
	other.ExpectedVersion = 1
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/invoices/%d", id), other, staff, "manual-owner-change"), 409)
	// The API clears ownership using the same exclusive lock as invoice operations.
	salesAssignment(t, f.h, f.root, f.uid, nil, 1)
	expect(t, request(f.h, "POST", "/api/v1/admin/invoices", in, staff, "manual-scope-create"), 404)
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/invoices/%d", id), nil, staff, ""), 404)
	page = data(t, request(f.h, "GET", "/api/v1/admin/invoice-customers", nil, staff, ""))
	if page["total"] != float64(0) {
		t.Fatal("customer picker leaked transferred scope", page)
	}
	var originalInvoice int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM invoices WHERE order_id=$1", f.orderID).Scan(&originalInvoice); err != nil {
		t.Fatal(err)
	}
	in.ExpectedVersion = 1
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/invoices/%d", originalInvoice), in, f.root, "manual-automatic-edit"), 409)
}

func TestManualInvoiceMigrationAndCorruptItemsGuard(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	ctx := t.Context()
	if _, err := f.db.Pool.Exec(ctx, `DROP TABLE IF EXISTS manual_invoice_operations; DROP TRIGGER invoice_version ON invoices; DROP FUNCTION advance_invoice_version();
 ALTER TABLE invoices DROP CONSTRAINT manual_invoice_origin,DROP COLUMN invoice_kind,DROP COLUMN version,DROP COLUMN updated_at;
 ALTER TABLE payment_transactions DROP COLUMN checkout_url; DELETE FROM schema_migrations WHERE version='056_manual_invoices.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, "DROP TABLE manual_invoice_operations"); err != nil {
		t.Fatal(err)
	}
	var kind string
	var version, amount int64
	if err := f.db.Pool.QueryRow(ctx, "SELECT invoice_kind,version,amount_cents FROM invoices WHERE order_id=$1", f.orderID).Scan(&kind, &version, &amount); err != nil || kind != "automatic" || version != 1 || amount != 2600 {
		t.Fatal("migration changed original invoice", kind, version, amount, err)
	}
	id := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "manual-corrupt-create")
	if _, err := f.db.Pool.Exec(ctx, "UPDATE invoice_items SET service_id=$2 WHERE invoice_id=$1", id, f.sid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", id), map[string]any{"expected_version": 1}, f.customer, "manual-corrupt-pay"), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", id), map[string]any{"provider": "mock", "expected_version": 1}, f.customer, "manual-corrupt-gateway"), 409)
}

func TestManualInvoiceAuditRollbackAndCancelPaymentRace(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	in := manualInvoiceInput(f.uid)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_manual_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'invoice.manual.%' THEN RAISE EXCEPTION 'manual audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_manual_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_manual_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/invoices", in, f.root, "manual-audit-create"), 500)
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoices WHERE invoice_kind='manual'").Scan(&count); err != nil || count != 0 {
		t.Fatal("audit failure left manual invoice", count, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_manual_audit"); err != nil {
		t.Fatal(err)
	}
	id := manualInvoiceCreate(t, f.h, f.root, in, "manual-audit-create")
	in.ExpectedVersion = 1
	in.Items[0].AmountCents = 4000
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events ENABLE TRIGGER reject_manual_audit"); err != nil {
		t.Fatal(err)
	}
	admin := fmt.Sprintf("/api/v1/admin/invoices/%d", id)
	expect(t, request(f.h, "PUT", admin, in, f.root, "manual-audit-edit"), 500)
	cancel := store.ManualInvoiceCancel{ExpectedVersion: 1, Reason: "撤销人工账单"}
	expect(t, request(f.h, "POST", admin+"/cancel", cancel, f.root, "manual-audit-cancel"), 500)
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", id), nil, f.customer, ""))
	if detail["version"] != float64(1) || detail["amount_cents"] != float64(3000) || detail["status"] != "unpaid" || detail["items"].([]any)[0].(map[string]any)["amount_cents"] != float64(3500) {
		t.Fatal("audit failure changed invoice", detail)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_manual_audit"); err != nil {
		t.Fatal(err)
	}
	results := make(chan int, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		results <- request(f.h, "POST", admin+"/cancel", cancel, f.root, "manual-race-cancel").Code
	}()
	go func() {
		defer wg.Done()
		results <- request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", id), map[string]any{"expected_version": 1}, f.customer, "manual-race-pay").Code
	}()
	wg.Wait()
	close(results)
	winners, conflicts := 0, 0
	for code := range results {
		if code == 200 || code == 201 {
			winners++
		} else if code == 409 {
			conflicts++
		} else {
			t.Fatalf("race status=%d", code)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatal("cancel/payment race", winners, conflicts)
	}
}

func TestManualInvoiceBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "manual-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		manualInvoiceCreate(t, h, f.root, manualInvoiceInput(f.uid), "manual-browser-invoice")
		_, _ = user(t, db, h, "manual-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view'] WHERE name='manual-reader-role'"); err != nil {
			t.Fatal(err)
		}
	})
}
