package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/jackc/pgx/v5"
)

func onlineTopupFixture(t *testing.T) (*Store, context.Context, int64, int64) {
	t.Helper()
	db := isolatedTestStore(t)
	var serverVersion int
	if err := db.Pool.QueryRow(t.Context(), `SELECT current_setting('server_version_num')::int`).Scan(&serverVersion); err != nil || serverVersion < 180000 || serverVersion >= 190000 {
		t.Fatalf("PostgreSQL 18 required: version=%d err=%v", serverVersion, err)
	}
	t.Logf("isolated PostgreSQL server_version_num=%d", serverVersion)
	db.ProvisionEncryptionKey = []byte("01234567890123456789012345678901")
	actor, uid := topUpFixture(t, db)
	ctx := WithTopupSession(t.Context(), "online-test-session")
	if _, err := db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('online-test-session',$1,now()+interval '1 hour');`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE finance_settings SET topup_enabled=true,min_topup_cents=1,max_topup_cents=100000,max_balance_cents=1000,require_active_order=false`); err != nil {
		t.Fatal(err)
	}
	ctx = WithPaymentConfiguration(ctx, PaymentConfiguration{PaymentSettings: PaymentSettings{Enabled: true, Version: 1, APIURL: "https://payments.example.test", PID: "10001", Types: []string{"alipay"}}, Key: "fixture-secret", PublicOrigin: "https://billing.example.test"})
	return db, ctx, actor, uid
}
func createTopup(t *testing.T, db *Store, ctx context.Context, uid, amount int64, key string) OnlineTopupResult {
	t.Helper()
	o, err := db.OnlineTopupOptions(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	out, err := db.CreateOnlineTopup(ctx, uid, OnlineTopupInput{AmountCents: amount, ExpectedVersion: o.Version}, key)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func topupIntent(db *Store, ctx context.Context, uid int64, out OnlineTopupResult, key string) (PaymentIntentResult, error) {
	return db.CreatePaymentIntent(ctx, uid, out.InvoiceID, "epay_alipay", "online-"+key, "https://payments.example.test/checkout", key, out.Version)
}
func topupSuccess(db *Store, ctx context.Context, p PaymentIntentResult, event string) (WebhookResult, error) {
	return db.ProcessPaymentWebhook(ctx, p.Provider, event, p.ProviderPaymentID, p.InvoiceID, p.AmountCents, "succeeded", []byte(fmt.Sprintf(`{"event":%q}`, event)), "receipt-"+p.ProviderPaymentID)
}
func TestOnlineTopupAdmissionSnapshotAndExactlyOnce(t *testing.T) {
	db, ctx, _, uid := onlineTopupFixture(t)
	out := createTopup(t, db, ctx, uid, 700, "create-topup-a")
	replay, err := db.CreateOnlineTopup(ctx, uid, OnlineTopupInput{AmountCents: 700, ExpectedVersion: out.SettingsVersion}, "create-topup-a")
	if err != nil || !replay.Replayed || replay.InvoiceID != out.InvoiceID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	_, err = db.CreateOnlineTopup(ctx, uid, OnlineTopupInput{AmountCents: 701, ExpectedVersion: out.SettingsVersion}, "create-topup-a")
	if conflictCode(t, err) != "idempotency_conflict" {
		t.Fatal(err)
	}
	o, err := db.OnlineTopupOptions(ctx, uid)
	if err != nil || o.ReservedCents != 0 {
		t.Fatalf("creation reserved capacity: %+v %v", o, err)
	}
	p, err := topupIntent(db, ctx, uid, out, "intent-topup-a")
	if err != nil {
		t.Fatal(err)
	}
	o, err = db.OnlineTopupOptions(ctx, uid)
	if err != nil || o.ReservedCents != 700 {
		t.Fatalf("admission missing: %+v %v", o, err)
	}
	_, err = db.CancelOnlineTopup(ctx, uid, out.InvoiceID, OnlineTopupCancelInput{ExpectedVersion: out.Version}, "cancel-accepted")
	if conflictCode(t, err) != "topup_payment_protected" {
		t.Fatal(err)
	}
	// Expiry and failed notifications do not erase an admitted receipt or release capacity.
	if _, err = db.Pool.Exec(ctx, `UPDATE payment_transactions SET status='expired' WHERE id=$1`, p.PaymentID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ProcessPaymentWebhook(ctx, p.Provider, "first-failed", p.ProviderPaymentID, p.InvoiceID, p.AmountCents, "failed", []byte(`{"failed":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE finance_settings SET topup_enabled=false,max_balance_cents=1,require_active_order=true`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := topupSuccess(db, ctx, p, fmt.Sprintf("success-%d", i))
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var balance, ledger, facts, services, commissions int64
	if err = db.Pool.QueryRow(ctx, `SELECT balance_cents,(SELECT count(*) FROM balance_ledger WHERE reference_type='gateway_topup'),(SELECT count(*) FROM balance_topup_settlements),(SELECT count(*) FROM services),(SELECT count(*) FROM affiliate_commission_entries) FROM users WHERE id=$1`, uid).Scan(&balance, &ledger, &facts, &services, &commissions); err != nil {
		t.Fatal(err)
	}
	if balance != 700 || ledger != 1 || facts != 1 || services != 0 || commissions != 0 {
		t.Fatalf("balance=%d ledger=%d facts=%d services=%d commissions=%d", balance, ledger, facts, services, commissions)
	}
	var state string
	var reserved int64
	if err = db.Pool.QueryRow(ctx, `SELECT status,(SELECT coalesce(sum(amount_cents),0)::bigint FROM balance_topups WHERE user_id=$1 AND status='accepted') FROM balance_topups WHERE invoice_id=$2`, uid, p.InvoiceID).Scan(&state, &reserved); err != nil || state != "credited" || reserved != 0 {
		t.Fatalf("state=%s reserved=%d err=%v", state, reserved, err)
	}
}
func TestOnlineTopupConcurrentCapacityAndAdminLock(t *testing.T) {
	db, ctx, actor, uid := onlineTopupFixture(t)
	a := createTopup(t, db, ctx, uid, 700, "capacity-create-a")
	b := createTopup(t, db, ctx, uid, 700, "capacity-create-b")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, out := range []OnlineTopupResult{a, b} {
		go func(i int, out OnlineTopupResult) {
			<-start
			_, e := topupIntent(db, ctx, uid, out, fmt.Sprintf("capacity-intent-%d", i))
			results <- e
		}(i, out)
	}
	close(start)
	passed, blocked := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			passed++
		} else if conflictCode(t, err) == "topup_balance_limit" {
			blocked++
		}
	}
	if passed != 1 || blocked != 1 {
		t.Fatalf("accepted=%d blocked=%d", passed, blocked)
	}
	_, _, err := db.TopUp(ctx, actor, uid, 400, "admin-with-reserved", "")
	if conflictCode(t, err) != "topup_balance_limit" {
		t.Fatal(err)
	}
	// Concurrent admitted settlement and admin credit use the same customer lock.
	var p PaymentIntentResult
	if err = db.Pool.QueryRow(ctx, `SELECT id,invoice_id,provider,provider_payment_id,amount_cents FROM payment_transactions LIMIT 1`).Scan(&p.PaymentID, &p.InvoiceID, &p.Provider, &p.ProviderPaymentID, &p.AmountCents); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { _, e := topupSuccess(db, ctx, p, "capacity-success"); errs <- e }()
	go func() { _, _, e := db.TopUp(ctx, actor, uid, 300, "admin-capacity-safe", ""); errs <- e }()
	for i := 0; i < 2; i++ {
		if err = <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var balance int64
	if err = db.Pool.QueryRow(ctx, `SELECT balance_cents FROM users WHERE id=$1`, uid).Scan(&balance); err != nil || balance != 1000 {
		t.Fatalf("balance=%d err=%v", balance, err)
	}
}
func TestOnlineTopupPermissionsVersionCancellationAndPaymentBoundaries(t *testing.T) {
	db, ctx, actor, uid := onlineTopupFixture(t)
	_, err := db.OnlineTopupOptions(t.Context(), uid)
	if conflictCode(t, err) != "permission_denied" {
		t.Fatal(err)
	}
	_, err = db.OnlineTopupOptions(ctx, actor)
	if conflictCode(t, err) != "permission_denied" {
		t.Fatal(err)
	}
	out := createTopup(t, db, ctx, uid, 500, "cancel-create-a")
	_, err = db.PayInvoice(ctx, uid, out.InvoiceID, "balance-topup-deny")
	if conflictCode(t, err) != "topup_external_payment_required" {
		t.Fatal(err)
	}
	_, err = db.PayInvoiceCredit(ctx, uid, out.InvoiceID, "credit-topup-deny", out.Version, "online-test-session")
	if conflictCode(t, err) != "topup_external_payment_required" {
		t.Fatal(err)
	}
	_, err = db.CreatePaymentIntent(ctx, uid, out.InvoiceID, "mock", "mock-no", "", "mock-topup-deny", out.Version)
	if conflictCode(t, err) != "topup_external_payment_required" {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE finance_settings SET min_topup_cents=2,version=version+1`); err != nil {
		t.Fatal(err)
	}
	_, err = topupIntent(db, ctx, uid, out, "stale-intent-a")
	if conflictCode(t, err) != "version_conflict" {
		t.Fatal(err)
	}
	c, err := db.CancelOnlineTopup(ctx, uid, out.InvoiceID, OnlineTopupCancelInput{ExpectedVersion: out.Version, Note: "设置已变化"}, "cancel-created-a")
	if err != nil || c.Status != "cancelled" {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = db.CancelOnlineTopup(ctx, uid, out.InvoiceID, OnlineTopupCancelInput{ExpectedVersion: out.Version, Note: "设置已变化"}, "cancel-created-a")
	if err != nil || !c.Replayed {
		t.Fatalf("%+v %v", c, err)
	}
	_, err = db.CancelOnlineTopup(ctx, uid, out.InvoiceID, OnlineTopupCancelInput{ExpectedVersion: out.Version, Note: "other"}, "cancel-created-a")
	if conflictCode(t, err) != "idempotency_conflict" {
		t.Fatal(err)
	}
}
func TestOnlineTopupPostingFailureRecordsLiabilityAtomically(t *testing.T) {
	db, ctx, actor, uid := onlineTopupFixture(t)
	out := createTopup(t, db, ctx, uid, 500, "liability-create")
	p, err := topupIntent(db, ctx, uid, out, "liability-intent")
	if err != nil {
		t.Fatal(err)
	}
	// Force the second posting write to fail after the balance UPDATE; savepoint
	// must remove that partial credit while retaining the real merchant receipt.
	if _, err = db.Pool.Exec(ctx, `CREATE FUNCTION reject_gateway_ledger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.reference_type='gateway_topup' THEN RAISE EXCEPTION 'forced ledger failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_gateway_ledger BEFORE INSERT ON balance_ledger FOR EACH ROW EXECUTE FUNCTION reject_gateway_ledger()`); err != nil {
		t.Fatal(err)
	}
	if _, err = topupSuccess(db, ctx, p, "liability-success"); err != nil {
		t.Fatal(err)
	}
	if _, err = topupSuccess(db, ctx, p, "liability-success-duplicate"); err != nil {
		t.Fatal(err)
	}
	var balance, ledger, receipts, amount int64
	var state, failure, invoice string
	err = db.Pool.QueryRow(ctx, `SELECT u.balance_cents,(SELECT count(*) FROM balance_ledger WHERE user_id=u.id),(SELECT count(*) FROM payment_transactions WHERE invoice_id=i.id AND status='succeeded'),s.amount_cents,t.status,s.failure_code,i.status FROM balance_topups t JOIN users u ON u.id=t.user_id JOIN balance_topup_settlements s ON s.topup_id=t.id JOIN invoices i ON i.id=t.invoice_id WHERE t.invoice_id=$1`, out.InvoiceID).Scan(&balance, &ledger, &receipts, &amount, &state, &failure, &invoice)
	if err != nil || balance != 0 || ledger != 0 || receipts != 1 || amount != 500 || state != "pending_liability" || failure != "balance_posting_failed" || invoice != "paid" {
		t.Fatalf("balance=%d ledger=%d receipts=%d amount=%d state=%s failure=%s invoice=%s err=%v", balance, ledger, receipts, amount, state, failure, invoice, err)
	}
	o, err := db.OnlineTopupOptions(ctx, uid)
	if err != nil || o.ReservedCents != 500 || o.AvailableCapacityCents == nil || *o.AvailableCapacityCents != 500 {
		t.Fatalf("liability capacity %+v %v", o, err)
	}
	_, _, err = db.TopUp(ctx, actor, uid, 600, "liability-admin-deny", "")
	if conflictCode(t, err) != "topup_balance_limit" {
		t.Fatal(err)
	}

}

func TestOnlineTopupSettlementRacesBalanceSpendingAndReconciliation(t *testing.T) {
	db, ctx, _, uid := onlineTopupFixture(t)
	if _, err := db.Pool.Exec(ctx, `UPDATE users SET balance_cents=300 WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	out := createTopup(t, db, ctx, uid, 700, "spending-create")
	p, err := topupIntent(db, ctx, uid, out, "spending-intent")
	if err != nil {
		t.Fatal(err)
	}
	var invoice int64
	if err = db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,amount_cents,invoice_kind) VALUES('SPEND-RACE',$1,200,'manual') RETURNING id`, uid).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents) VALUES($1,'Race spending','manual',200)`, invoice); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Trusted reconciliation and callback use the same receipt identity; neither
	// can hold a payment lock while waiting for the balance customer's row.
	check, err := db.BeginPaymentCheck(ctx, uid, p.InvoiceID, p.PaymentID, false)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 3)
	go func() { _, e := db.PayInvoice(ctx, uid, invoice, "spending-pay", 1); errs <- e }()
	go func() { _, e := topupSuccess(db, ctx, p, "spending-webhook"); errs <- e }()
	go func() {
		_, e := db.CompletePaymentCheck(ctx, check, payment.QueryResult{ProviderPaymentID: p.ProviderPaymentID, AmountCents: p.AmountCents, Status: "succeeded", ExternalTransactionID: "receipt-" + p.ProviderPaymentID})
		errs <- e
	}()
	for i := 0; i < 3; i++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	var balance, credits, spends int64
	if err = db.Pool.QueryRow(ctx, `SELECT balance_cents,(SELECT count(*) FROM balance_ledger WHERE user_id=$1 AND reference_type='gateway_topup'),(SELECT count(*) FROM balance_ledger WHERE user_id=$1 AND entry_type='payment') FROM users WHERE id=$1`, uid).Scan(&balance, &credits, &spends); err != nil || balance != 800 || credits != 1 || spends != 1 {
		t.Fatalf("balance=%d credits=%d spends=%d err=%v", balance, credits, spends, err)
	}
}

func TestOnlineTopupOverflowIsVisibleLiability(t *testing.T) {
	db, ctx, actor, uid := onlineTopupFixture(t)
	out := createTopup(t, db, ctx, uid, 500, "overflow-create")
	p, err := topupIntent(db, ctx, uid, out, "overflow-intent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE users SET balance_cents=9223372036854775802 WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE finance_settings SET max_balance_cents=1`); err != nil {
		t.Fatal(err)
	}
	o, err := db.OnlineTopupOptions(ctx, uid)
	if err != nil || o.ReservedCents != 500 || o.AvailableCapacityCents == nil || *o.AvailableCapacityCents != 0 {
		t.Fatalf("overflow capacity %+v %v", o, err)
	}
	_, _, err = db.TopUp(ctx, actor, uid, 1, "overflow-admin-deny", "")
	if conflictCode(t, err) != "topup_balance_limit" {
		t.Fatal(err)
	}
	if _, err = topupSuccess(db, ctx, p, "overflow-success"); err != nil {
		t.Fatal(err)
	}
	var balance, ledger int64
	var state, failure string
	err = db.Pool.QueryRow(ctx, `SELECT u.balance_cents,t.status,s.failure_code,(SELECT count(*) FROM balance_ledger WHERE user_id=u.id) FROM balance_topups t JOIN balance_topup_settlements s ON s.topup_id=t.id JOIN users u ON u.id=t.user_id WHERE t.invoice_id=$1`, out.InvoiceID).Scan(&balance, &state, &failure, &ledger)
	if err != nil || balance != 9223372036854775802 || state != "pending_liability" || failure != "balance_overflow" || ledger != 0 {
		t.Fatalf("balance=%d state=%s failure=%s ledger=%d err=%v", balance, state, failure, ledger, err)
	}
}

func TestOnlineTopupAdminAdmissionAndSettingsWriterLockOrder(t *testing.T) {
	db, ctx, actor, uid := onlineTopupFixture(t)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// A settings writer races both ways of increasing the customer's balance.
	// Admission may reject its stale settings snapshot; deadlocks/timeouts and
	// partial reservations are never valid outcomes.
	for round := 0; round < 16; round++ {
		out := createTopup(t, db, ctx, uid, 1, fmt.Sprintf("three-way-create-%d", round))
		start := make(chan struct{})
		errs := make(chan error, 3)
		go func(round int) {
			<-start
			_, _, e := db.TopUp(ctx, actor, uid, 1, fmt.Sprintf("three-way-admin-%d", round), "")
			errs <- e
		}(round)
		go func(round int) {
			<-start
			_, e := topupIntent(db, ctx, uid, out, fmt.Sprintf("three-way-intent-%d", round))
			if e != nil {
				var c *Conflict
				if errors.As(e, &c) && c.Code == "version_conflict" {
					e = nil
				}
			}
			errs <- e
		}(round)
		go func() {
			<-start
			_, e := db.Pool.Exec(ctx, `UPDATE finance_settings SET version=version+1 WHERE id=1`)
			errs <- e
		}()
		close(start)
		for i := 0; i < 3; i++ {
			if e := <-errs; e != nil {
				t.Fatalf("round %d: %v", round, e)
			}
		}
	}
	var balance, accepted, payments int64
	if err := db.Pool.QueryRow(ctx, `SELECT balance_cents,(SELECT count(*) FROM balance_topups WHERE status='accepted'),(SELECT count(*) FROM payment_transactions) FROM users WHERE id=$1`, uid).Scan(&balance, &accepted, &payments); err != nil || balance != 16 || accepted != payments {
		t.Fatalf("balance=%d accepted=%d payments=%d err=%v", balance, accepted, payments, err)
	}
}

func TestAdminTopupDoesNotHoldSettingsWhileWaitingForCustomer(t *testing.T) {
	db, ctx, actor, uid := onlineTopupFixture(t)
	out := createTopup(t, db, ctx, uid, 1, "lock-proof-create")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	blocker, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	var blockerPID int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if err = LockFinancialCustomer(ctx, blocker, uid); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { _, _, e := db.TopUp(ctx, actor, uid, 1, "lock-proof-admin", ""); errs <- e }()
	// Observe a real database lock wait, rather than relying on goroutine timing.
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err = db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	writer, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	// Before the fix, admin already held settings SHARE while waiting for the
	// customer. NOWAIT fails there, exposing the precise reverse lock edge.
	if _, err = writer.Exec(ctx, `SELECT id FROM finance_settings WHERE id=1 FOR UPDATE NOWAIT`); err != nil {
		t.Fatalf("admin retained settings while blocked on customer: %v", err)
	}
	go func() { _, e := topupIntent(db, ctx, uid, out, "lock-proof-intent"); errs <- e }()
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
}

func TestPaymentIntentMissingClassificationKeepsConfigurationPrecedence(t *testing.T) {
	db, ctx, _, uid := onlineTopupFixture(t)
	var version int64
	if err := db.Pool.QueryRow(ctx, `SELECT version FROM payment_settings WHERE id=1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	_, err := db.CreatePaymentIntent(WithPaymentConfigurationVersion(ctx, version+1), uid, 9223372036854775807, "epay_alipay", "missing-stale", "", "missing-stale-key", 1)
	if conflictCode(t, err) != "version_conflict" {
		t.Fatal(err)
	}
	_, err = db.CreatePaymentIntent(WithPaymentConfigurationVersion(ctx, version), uid, 9223372036854775807, "epay_alipay", "missing-current", "", "missing-current-key", 1)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("current configuration must reach owned invoice lookup: %v", err)
	}
}
