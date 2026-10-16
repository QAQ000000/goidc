package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

func topupLiabilityFixture(t *testing.T) (*Store, context.Context, int64, int64, OnlineTopupResult, PaymentIntentResult) {
	t.Helper()
	db, ctx, actor, uid := onlineTopupFixture(t)
	out := createTopup(t, db, ctx, uid, 500, "liability-create")
	p, err := topupIntent(db, ctx, uid, out, "liability-payment")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `CREATE FUNCTION reject_gateway_ledger() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.reference_type='gateway_topup' THEN RAISE EXCEPTION 'forced ledger failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_gateway_ledger BEFORE INSERT ON balance_ledger FOR EACH ROW EXECUTE FUNCTION reject_gateway_ledger()`); err != nil {
		t.Fatal(err)
	}
	if _, err = topupSuccess(db, ctx, p, "liability-paid"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('liability-admin-session',$1,now()+interval '1 hour')`, actor); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `DROP TRIGGER reject_gateway_ledger ON balance_ledger`); err != nil {
		t.Fatal(err)
	}
	return db, WithTopupLiabilitySession(ctx, "liability-admin-session"), actor, uid, out, p
}
func TestTopupLiabilityResolutionFixedReceiptAndImmutableFacts(t *testing.T) {
	db, ctx, actor, uid, out, p := topupLiabilityFixture(t)
	before, err := db.GetTopupLiability(ctx, actor, out.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	var paidAt string
	if err = db.Pool.QueryRow(ctx, `SELECT paid_at::text FROM invoices WHERE id=$1`, out.InvoiceID).Scan(&paidAt); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE finance_settings SET topup_enabled=false,max_balance_cents=1`); err != nil {
		t.Fatal(err)
	}
	in := ResolveTopupLiabilityInput{ExpectedVersion: before.Version, Note: "核实已收款，补入原账户"}
	first, err := db.ResolveTopupLiability(ctx, actor, out.InvoiceID, in, "resolve-fixed-key")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "credited" || first.Version != before.Version+1 || first.Resolution == nil || first.Settlement.Status != "pending_liability" {
		t.Fatalf("unexpected result %+v", first)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE users SET balance_cents=balance_cents+1 WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	replay, err := db.ResolveTopupLiability(ctx, actor, out.InvoiceID, in, "resolve-fixed-key")
	if err != nil || !replay.Replayed || replay.Version != first.Version || *replay.Resolution != *first.Resolution {
		t.Fatalf("receipt changed %+v %v", replay, err)
	}
	_, err = db.ResolveTopupLiability(ctx, actor, out.InvoiceID, in, "resolve-other-key")
	if conflictCode(t, err) != "topup_not_pending" {
		t.Fatal(err)
	}
	if _, err = topupSuccess(db, ctx, p, "liability-replay-paid"); err != nil {
		t.Fatal(err)
	}
	var balance, ledger, facts, audits int64
	var paidAfter, status string
	if err = db.Pool.QueryRow(ctx, `SELECT u.balance_cents,u.status,(SELECT count(*) FROM balance_ledger WHERE reference_type='gateway_topup'),(SELECT count(*) FROM balance_topup_liability_resolutions),(SELECT count(*) FROM audit_events WHERE action='balance.topup.liability_resolved'),i.paid_at::text FROM users u JOIN invoices i ON i.user_id=u.id WHERE i.id=$1`, out.InvoiceID).Scan(&balance, &status, &ledger, &facts, &audits, &paidAfter); err != nil {
		t.Fatal(err)
	}
	if balance != 501 || status != "disabled" || ledger != 1 || facts != 1 || audits != 1 || paidAfter != paidAt {
		t.Fatalf("invariants %d %s %d %d %d %s/%s", balance, status, ledger, facts, audits, paidAfter, paidAt)
	}
	for _, query := range []string{`UPDATE balance_topup_liability_resolutions SET note='changed'`, `DELETE FROM balance_topup_liability_resolutions`, `UPDATE balance_topup_settlements SET failure_code='changed'`} {
		if _, err = db.Pool.Exec(ctx, query); err == nil {
			t.Fatal("immutable fact changed", query)
		}
	}
	if _, err = db.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash='liability-admin-session'`); err != nil {
		t.Fatal(err)
	}
	_, err = db.ResolveTopupLiability(ctx, actor, out.InvoiceID, in, "resolve-fixed-key")
	if conflictCode(t, err) != "permission_denied" {
		t.Fatal(err)
	}
}
func TestTopupLiabilityResolutionConcurrentAndRollback(t *testing.T) {
	db, ctx, actor, uid, out, _ := topupLiabilityFixture(t)
	before, err := db.GetTopupLiability(ctx, actor, out.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	in := ResolveTopupLiabilityInput{ExpectedVersion: before.Version, Note: "并发核实"}
	if _, err = db.Pool.Exec(ctx, `CREATE FUNCTION reject_resolution_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='balance.topup.liability_resolved' THEN RAISE EXCEPTION 'forced audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_resolution_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_resolution_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ResolveTopupLiability(ctx, actor, out.InvoiceID, in, "rollback-resolve"); err == nil {
		t.Fatal("audit failure accepted")
	}
	var balance, ledger, facts int64
	if err = db.Pool.QueryRow(ctx, `SELECT balance_cents,(SELECT count(*) FROM balance_ledger WHERE reference_type='gateway_topup'),(SELECT count(*) FROM balance_topup_liability_resolutions) FROM users WHERE id=$1`, uid).Scan(&balance, &ledger, &facts); err != nil {
		t.Fatal(err)
	}
	after, err := db.GetTopupLiability(ctx, actor, out.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	if balance != 0 || ledger != 0 || facts != 0 || after.Status != "pending_liability" || after.Version != before.Version {
		t.Fatalf("partial failure %+v %d/%d/%d", after, balance, ledger, facts)
	}
	if _, err = db.Pool.Exec(ctx, `DROP TRIGGER reject_resolution_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := db.ResolveTopupLiability(ctx, actor, out.InvoiceID, in, "concurrent-resolve")
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
}

func TestTopupLiabilityRejectsPaymentOwnerDriftAndDatabaseMismatch(t *testing.T) {
	db, ctx, actor, uid, out, p := topupLiabilityFixture(t)
	before, err := db.GetTopupLiability(ctx, actor, out.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE payment_transactions SET user_id=$2 WHERE id=$1`, p.PaymentID, actor); err != nil {
		t.Fatal(err)
	}
	_, err = db.ResolveTopupLiability(ctx, actor, out.InvoiceID, ResolveTopupLiabilityInput{ExpectedVersion: before.Version, Note: "关联核实"}, "drift-resolution")
	if conflictCode(t, err) != "topup_not_pending" {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE payment_transactions SET user_id=$2 WHERE id=$1`, p.PaymentID, uid); err != nil {
		t.Fatal(err)
	}
	var ledger int64
	if err = db.Pool.QueryRow(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note) VALUES($1,'topup',1,1,'gateway_topup',$2,'mismatch-ledger','fixture') RETURNING id`, uid, out.InvoiceID).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, `INSERT INTO balance_topup_liability_resolutions(topup_id,invoice_id,payment_id,user_id,amount_cents,ledger_id,actor_id,note,idempotency_key,request_hash,result) SELECT id,invoice_id,$2,user_id,amount_cents,$3,$4,'invalid ledger amount','invalid-association','hash','{}' FROM balance_topups WHERE invoice_id=$1`, out.InvoiceID, p.PaymentID, ledger, actor)
	if err == nil {
		t.Fatal("database accepted inconsistent ledger")
	}
	_, err = db.ResolveTopupLiability(ctx, actor, out.InvoiceID, ResolveTopupLiabilityInput{ExpectedVersion: before.Version, Note: "异常流水核实"}, "existing-ledger-resolution")
	if conflictCode(t, err) != "topup_not_pending" {
		t.Fatal(err)
	}
}

// assertLiabilityWrites checks the durable money, fact, audit and version boundary
// together: a failed attempt must leave no subset of a resolution behind.
func assertLiabilityWrites(t *testing.T, db *Store, ctx context.Context, uid, invoice, balance, version, writes int64) {
	t.Helper()
	var gotBalance, gotVersion, ledgers, facts, audits int64
	var status string
	err := db.Pool.QueryRow(ctx, `SELECT u.balance_cents,i.version,t.status,(SELECT count(*) FROM balance_ledger WHERE reference_type='gateway_topup' AND reference_id=i.id),(SELECT count(*) FROM balance_topup_liability_resolutions WHERE invoice_id=i.id),(SELECT count(*) FROM audit_events WHERE action='balance.topup.liability_resolved' AND entity_id=i.id::text) FROM invoices i JOIN users u ON u.id=i.user_id JOIN balance_topups t ON t.invoice_id=i.id WHERE i.id=$1 AND u.id=$2`, invoice, uid).Scan(&gotBalance, &gotVersion, &status, &ledgers, &facts, &audits)
	if err != nil {
		t.Fatal(err)
	}
	wantStatus := "pending_liability"
	if writes == 1 {
		wantStatus = "credited"
	}
	if gotBalance != balance || gotVersion != version || status != wantStatus || ledgers != writes || facts != writes || audits != writes {
		t.Fatalf("durable boundary balance=%d version=%d status=%s ledger/fact/audit=%d/%d/%d want balance=%d version=%d status=%s writes=%d", gotBalance, gotVersion, status, ledgers, facts, audits, balance, version, wantStatus, writes)
	}
}
func TestTopupLiabilityOverflowAndLedgerFailureRetrySameKey(t *testing.T) {
	db, ctx, actor, uid, out, _ := topupLiabilityFixture(t)
	before, err := db.GetTopupLiability(ctx, actor, out.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	in := ResolveTopupLiabilityInput{ExpectedVersion: before.Version, Note: "排除故障后补账"}
	key := "failed-then-retry"
	const high int64 = 9223372036854775800
	if _, err = db.Pool.Exec(ctx, `UPDATE users SET balance_cents=$2 WHERE id=$1`, uid, high); err != nil {
		t.Fatal(err)
	}
	_, err = db.ResolveTopupLiability(ctx, actor, out.InvoiceID, in, key)
	if conflictCode(t, err) != "balance_overflow" {
		t.Fatal(err)
	}
	assertLiabilityWrites(t, db, ctx, uid, out.InvoiceID, high, before.Version, 0)
	if _, err = db.Pool.Exec(ctx, `UPDATE users SET balance_cents=0 WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `CREATE TRIGGER reject_gateway_ledger BEFORE INSERT ON balance_ledger FOR EACH ROW EXECUTE FUNCTION reject_gateway_ledger()`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ResolveTopupLiability(ctx, actor, out.InvoiceID, in, key); err == nil {
		t.Fatal("ledger failure accepted")
	}
	assertLiabilityWrites(t, db, ctx, uid, out.InvoiceID, 0, before.Version, 0)
	if _, err = db.Pool.Exec(ctx, `DROP TRIGGER reject_gateway_ledger ON balance_ledger`); err != nil {
		t.Fatal(err)
	}
	result, err := db.ResolveTopupLiability(ctx, actor, out.InvoiceID, in, key)
	if err != nil || result.Replayed {
		t.Fatalf("same original request cannot recover %+v %v", result, err)
	}
	assertLiabilityWrites(t, db, ctx, uid, out.InvoiceID, 500, before.Version+1, 1)
}
func TestTopupLiabilityDifferentKeysRaceExactlyOnce(t *testing.T) {
	db, ctx, actor, uid, out, _ := topupLiabilityFixture(t)
	before, err := db.GetTopupLiability(ctx, actor, out.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, key := range []string{"competing-key-one", "competing-key-two"} {
		go func(key string) {
			<-start
			_, e := db.ResolveTopupLiability(ctx, actor, out.InvoiceID, ResolveTopupLiabilityInput{ExpectedVersion: before.Version, Note: "独立操作竞争"}, key)
			errs <- e
		}(key)
	}
	close(start)
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		e := <-errs
		if e == nil {
			success++
		} else if conflictCode(t, e) == "topup_not_pending" {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success/conflicts=%d/%d", success, conflicts)
	}
	assertLiabilityWrites(t, db, ctx, uid, out.InvoiceID, 500, before.Version+1, 1)
}
func TestTopupLiabilityRacesAdminCreditAndCustomerSpending(t *testing.T) {
	db, ctx, actor, uid, out, _ := topupLiabilityFixture(t)
	before, err := db.GetTopupLiability(ctx, actor, out.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE users SET balance_cents=300 WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE finance_settings SET max_balance_cents=0`); err != nil {
		t.Fatal(err)
	}
	var invoice int64
	if err = db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,amount_cents,invoice_kind) VALUES('LIABILITY-SPEND',$1,200,'manual') RETURNING id`, uid).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents) VALUES($1,'并发消费','manual',200)`, invoice); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, 3)
	go func() {
		<-start
		_, e := db.ResolveTopupLiability(ctx, actor, out.InvoiceID, ResolveTopupLiabilityInput{ExpectedVersion: before.Version, Note: "资金锁竞争"}, "racing-resolution")
		errs <- e
	}()
	go func() { <-start; _, _, e := db.TopUp(ctx, actor, uid, 100, "racing-admin-credit", ""); errs <- e }()
	go func() { <-start; _, e := db.PayInvoice(ctx, uid, invoice, "racing-balance-spend", 1); errs <- e }()
	close(start)
	for i := 0; i < 3; i++ {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	assertLiabilityWrites(t, db, ctx, uid, out.InvoiceID, 700, before.Version+1, 1)
	var delta int64
	var count int
	if err = db.Pool.QueryRow(ctx, `SELECT sum(delta_cents)::bigint,count(*) FROM balance_ledger WHERE user_id=$1`, uid).Scan(&delta, &count); err != nil {
		t.Fatal(err)
	}
	if delta != 400 || count != 3 {
		t.Fatalf("money not conserved delta=%d count=%d", delta, count)
	}
}
func TestTopupLiabilityCrossCustomerSameActorKeyRace(t *testing.T) {
	db, ctx, actor, uid, first, _ := topupLiabilityFixture(t)
	var other int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('liability-other','liability-other@example.test','unused','Other','customer') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('liability-other-session',$1,now()+interval '1 hour')`, other); err != nil {
		t.Fatal(err)
	}
	otherCtx := WithTopupSession(ctx, "liability-other-session")
	second := createTopup(t, db, otherCtx, other, 500, "liability-other-create")
	p, err := topupIntent(db, otherCtx, other, second, "liability-other-payment")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `CREATE TRIGGER reject_gateway_ledger BEFORE INSERT ON balance_ledger FOR EACH ROW EXECUTE FUNCTION reject_gateway_ledger()`); err != nil {
		t.Fatal(err)
	}
	if _, err = topupSuccess(db, otherCtx, p, "liability-other-paid"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `DROP TRIGGER reject_gateway_ledger ON balance_ledger`); err != nil {
		t.Fatal(err)
	}
	a, err := db.GetTopupLiability(ctx, actor, first.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.GetTopupLiability(ctx, actor, second.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	type attempt struct {
		invoice, uid, version int64
		err                   error
	}
	start := make(chan struct{})
	results := make(chan attempt, 2)
	for _, item := range []attempt{{invoice: first.InvoiceID, uid: uid, version: a.Version}, {invoice: second.InvoiceID, uid: other, version: b.Version}} {
		go func(item attempt) {
			<-start
			_, item.err = db.ResolveTopupLiability(ctx, actor, item.invoice, ResolveTopupLiabilityInput{ExpectedVersion: item.version, Note: "同员工跨客户同键"}, "global-shared-key")
			results <- item
		}(item)
	}
	close(start)
	success, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		item := <-results
		if item.err == nil {
			success++
			assertLiabilityWrites(t, db, ctx, item.uid, item.invoice, 500, item.version+1, 1)
		} else {
			if conflictCode(t, item.err) != "idempotency_conflict" {
				t.Fatal(item.err)
			}
			conflicts++
			assertLiabilityWrites(t, db, ctx, item.uid, item.invoice, 0, item.version, 0)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("cross customer success/conflicts=%d/%d", success, conflicts)
	}
}
