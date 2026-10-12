package httpapi

import (
	"fmt"
	"testing"
	"time"
)

func TestCreditLifecycle(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "credit-life-root", "super_admin")
	uid, customer := user(t, db, h, "credit-life-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=1234 WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE credit_settings SET enabled=true; UPDATE customer_credit_accounts SET limit_cents=20000"); err != nil {
		t.Fatal(err)
	}
	id := manualInvoiceCreate(t, h, root, manualInvoiceInput(uid), "credit-life-manual")
	pay := fmt.Sprintf("/api/v1/invoices/%d/pay-credit", id)
	expect(t, request(h, "POST", pay, map[string]any{"expected_version": 1}, customer, "credit-life-pay"), 201)
	expect(t, request(h, "POST", pay, map[string]any{"expected_version": 1}, customer, "credit-life-pay"), 200)
	expect(t, request(h, "POST", pay, map[string]any{"expected_version": 1}, customer, "credit-life-other"), 409)
	repayment := fmt.Sprintf("/api/v1/admin/credit/consumptions/%d/repay", id)
	body := map[string]any{"amount_cents": 1000, "expected_version": 1, "receipt": "bank-credit-life-001", "received_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), "note": "银行到账核实"}
	expect(t, request(h, "POST", repayment, body, root, "credit-life-repay"), 201)
	expect(t, request(h, "POST", repayment, body, root, "credit-life-repay"), 200)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", id), map[string]any{}, root, "credit-life-refund-paid"), 409)
	var used, balance int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT a.used_cents,u.balance_cents FROM customer_credit_accounts a JOIN users u ON u.id=a.user_id WHERE u.id=$1", uid).Scan(&used, &balance); err != nil || used != 2000 || balance != 1234 {
		t.Fatal(used, balance, err)
	}
	id2 := manualInvoiceCreate(t, h, root, manualInvoiceInput(uid), "credit-life-manual-two")
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay-credit", id2), map[string]any{"expected_version": 1}, customer, "credit-life-pay-two"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", id2), map[string]any{}, root, "credit-life-refund-two"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", id), map[string]any{}, root, "credit-life-refund-two"), 409)
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM customer_credit_accounts WHERE user_id=$1", uid).Scan(&version); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/credit/%d/freeze", uid), map[string]any{"frozen": true, "expected_version": version}, root, "credit-life-freeze"), 201)
	id3 := manualInvoiceCreate(t, h, root, manualInvoiceInput(uid), "credit-life-manual-three")
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay-credit", id3), map[string]any{"expected_version": 1}, customer, "credit-life-pay-three"), 409)
	expect(t, request(h, "GET", "/api/v1/admin/credit/consumptions", nil, root, ""), 200)
	expect(t, request(h, "GET", "/api/v1/admin/credit/repayments", nil, root, ""), 200)
	for _, suffix := range []string{"?status=invalid", "?customer_id=bad", "?date_from=2026-99-01", "?date_from=2026-10-13&date_to=2026-10-12"} {
		expect(t, request(h, "GET", "/api/v1/admin/credit/consumptions"+suffix, nil, root, ""), 400)
	}
	page := data(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/credit/consumptions?customer_id=%d&status=refunded", uid), nil, root, ""))
	if page["total"].(float64) != 1 {
		t.Fatal(page)
	}

}
func TestCreditRenewalAndBalanceRace(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE credit_settings SET enabled=true;UPDATE customer_credit_accounts SET limit_cents=100000"); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", f.sid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	renewal := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.sid), map[string]any{}, f.customer, "credit-renew-create")
	expect(t, renewal, 201)
	id := int64(data(t, renewal)["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay-credit", id), map[string]any{"expected_version": 1}, f.customer, "credit-renew-pay"), 201)
	var after time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", f.sid).Scan(&after); err != nil || !after.After(before) {
		t.Fatal(before, after, err)
	}
	id2 := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "credit-race-manual")
	results := make(chan int, 2)
	go func() {
		results <- request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay-credit", id2), map[string]any{"expected_version": 1}, f.customer, "credit-race-credit").Code
	}()
	go func() {
		results <- request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", id2), map[string]any{"expected_version": 1}, f.customer, "credit-race-balance").Code
	}()
	a, b := <-results, <-results
	if !((a == 201 && b == 409) || (b == 201 && a == 409)) {
		t.Fatal("payment race", a, b)
	}
}
func TestCreditManagementPermissionsAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "credit-security-root", "super_admin")
	uid, customer := user(t, db, h, "credit-security-customer", "customer")
	staffID, staff := user(t, db, h, "credit-security-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE credit_settings SET enabled=true;UPDATE customer_credit_accounts SET limit_cents=10000"); err != nil {
		t.Fatal(err)
	}
	id := manualInvoiceCreate(t, h, root, manualInvoiceInput(uid), "credit-security-invoice")
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION credit_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='invoice.paid' THEN RAISE EXCEPTION 'reject'; END IF;RETURN NEW;END $$;CREATE TRIGGER credit_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION credit_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/invoices/%d/pay-credit", id)
	expect(t, request(h, "POST", path, map[string]any{"expected_version": 1}, customer, "credit-security-pay"), 500)
	var used int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT used_cents FROM customer_credit_accounts WHERE user_id=$1", uid).Scan(&used); err != nil || used != 0 {
		t.Fatal(used, err)
	}
	if _, err := db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER credit_reject_audit"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", path, map[string]any{"expected_version": 1}, customer, "credit-security-pay"), 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['credit.view','credit.manage'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	freeze := fmt.Sprintf("/api/v1/admin/credit/%d/freeze", uid)
	body := map[string]any{"frozen": true, "expected_version": 2}
	expect(t, request(h, "PUT", freeze, body, staff, "credit-security-freeze"), 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PUT", freeze, body, staff, "credit-security-freeze"), 404)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PUT", freeze, body, staff, "credit-security-freeze"), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", path, map[string]any{"expected_version": 1}, customer, "credit-security-pay"), 401)
}
func TestCreditPaymentPendingAndUnsupported(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "credit-pending-root", "super_admin")
	uid, customer := user(t, db, h, "credit-pending-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE credit_settings SET enabled=true;UPDATE customer_credit_accounts SET limit_cents=10000"); err != nil {
		t.Fatal(err)
	}
	id := manualInvoiceCreate(t, h, root, manualInvoiceInput(uid), "credit-pending-manual")
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", id), map[string]any{"provider": "mock", "expected_version": 1}, customer, "credit-pending-intent"), 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET provider='epay' WHERE invoice_id=$1", id); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay-credit", id), map[string]any{"expected_version": 1}, customer, "credit-pending-pay"), 409)
	var unsupported int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,amount_cents) VALUES('credit-unsupported',$1,100) RETURNING id", uid).Scan(&unsupported); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay-credit", unsupported), map[string]any{"expected_version": 1}, customer, "credit-unsupported-pay"), 409)
}
