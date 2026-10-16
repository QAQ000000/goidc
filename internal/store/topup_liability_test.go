package store

import (
	"context"
	"sync"
	"testing"
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
