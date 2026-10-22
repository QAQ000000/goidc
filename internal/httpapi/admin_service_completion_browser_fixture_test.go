package httpapi

import (
	"fmt"
	"testing"
)

// Both final service-management UI flows share one real API and isolated schema.
func TestAdminServiceCompletionBrowserFixture(t *testing.T) {
	browserFixturePaths(t)
	f := newRenewalBatchFixture(t, 700)
	target, _ := user(t, f.db, f.h, "completion-recipient", "customer")
	transferService := addRenewalBatchService(t, f.adminRenewalFixture, 900, 2)
	contentRole(t, f.db, f.actor, "services.view", "customers.view", "services.transfer", "invoices.view", "services.renew", "services.renew.pay_balance")
	reader, _ := user(t, f.db, f.h, "completion-reader", "admin")
	contentRole(t, f.db, reader, "services.view", "invoices.view", "customers.view")
	// Wallet is an explicit test credit, not a mocked payment or request response.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=100000 WHERE id=$1", f.uid); err != nil {
		t.Fatal(err)
	}
	var beforeLedger, beforeRenewals int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM balance_ledger").Scan(&beforeLedger); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoices WHERE EXISTS (SELECT 1 FROM invoice_items WHERE invoice_id=invoices.id AND item_type='renew')").Scan(&beforeRenewals); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT version FROM services WHERE id=$1", transferService).Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("completion browser fixture: services=%v transfer=%d source=%d target=%d", f.services, transferService, f.uid, target)
	adminBrowserFixtureStore(t, "completion-super", f.db, nil)
	var balance, owner, afterVersion int64
	var auto bool
	var transfers, receipts, paid, ledger int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT user_id,version,auto_renew FROM services WHERE id=$1", transferService).Scan(&owner, &afterVersion, &auto); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM service_ownership_transfers WHERE service_id=$1", transferService).Scan(&transfers); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations WHERE scope='admin:service.transfer'").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM invoices WHERE status='paid' AND EXISTS (SELECT 1 FROM invoice_items WHERE invoice_id=invoices.id AND item_type='renew')").Scan(&paid); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM balance_ledger").Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if owner != target || auto || afterVersion != version+1 || transfers != 1 || receipts != 1 || paid-beforeRenewals != 2 || ledger != beforeLedger+2 || balance != 100000-3200 {
		t.Fatal(fmt.Sprintf("completion postconditions: owner=%d version=%d auto=%v transfers=%d receipts=%d paid=%d ledger=%d balance=%d", owner, afterVersion, auto, transfers, receipts, paid, ledger, balance))
	}
	t.Log("completion postconditions: exactly one transfer and two renewal settlements; wallet charged 3200 cents once")
}
