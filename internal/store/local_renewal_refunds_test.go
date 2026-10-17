package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type localRenewalFixture struct {
	orderRefundFixture
	service, renewInvoice int64
	before                time.Time
}

func newLocalRenewalFixture(t *testing.T, external bool, amount int64, originalDue ...*time.Time) localRenewalFixture {
	t.Helper()
	f := localRenewalFixture{orderRefundFixture: newOrderRefundFixture(t, false)}
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.db.Pool.QueryRow(ctx, `SELECT id FROM services WHERE order_id=$1 ORDER BY id LIMIT 1`, f.order).Scan(&f.service))
	// Month-end and leap-year arithmetic must be restored exactly, not subtracted.
	f.before = time.Date(2028, 1, 31, 12, 0, 0, 0, time.UTC)
	var due any = f.before
	if len(originalDue) > 0 {
		due = originalDue[0]
		if originalDue[0] != nil {
			f.before = *originalDue[0]
		}
	}
	_, err := f.db.Pool.Exec(ctx, `UPDATE services SET status='active',next_due_at=$2,recurring_price_cents=$3,renewal_discount_percent=0,renewal_fixed_discount_cents=100,renewal_discount_limit=2,renewal_promo_code='LOCAL' WHERE id=$1`, f.service, due, amount+100)
	must(err)
	r, err := f.db.CreateRenewalInvoice(ctx, f.customer, f.service, "local-renewal-create")
	must(err)
	f.renewInvoice = r.InvoiceID
	if external {
		p, err := f.db.CreatePaymentIntent(ctx, f.customer, r.InvoiceID, "mock", "local-renew-original", "", "local-renew-intent")
		must(err)
		f.payment = p.PaymentID
		_, err = f.db.ProcessPaymentWebhook(ctx, "mock", "local-renew-event", "local-renew-original", r.InvoiceID, amount, "succeeded", []byte(`{"verified":"fixture"}`), "local-renew-tx")
		must(err)
	} else {
		_, err = f.db.Pool.Exec(ctx, `UPDATE users SET balance_cents=100000 WHERE id=$1`, f.customer)
		must(err)
		_, err = f.db.PayInvoice(ctx, f.customer, r.InvoiceID, "local-renew-pay")
		must(err)
	}
	return f
}
func (f localRenewalFixture) renewalContext(t *testing.T) ExternalRefundRenewalContext {
	t.Helper()
	c, err := f.db.ExternalRefundRenewalContext(WithExternalRefundSession(t.Context(), "order-refund-request"), f.actor, f.renewInvoice)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func (f localRenewalFixture) renewalCreate(t *testing.T) ExternalRefundResult {
	t.Helper()
	c := f.renewalContext(t)
	if !c.Eligible {
		t.Fatalf("not eligible: %+v", c)
	}
	in := ExternalRefundInput{PaymentID: f.payment, AmountCents: 734, Method: "original_route", ProviderRefundID: "local-payout", ReceivedAt: time.Now(), EvidenceRef: "redacted/local", EvidenceSHA256: strings.Repeat("a", 64), Note: "verified actual payout", ExpectedVersion: c.InvoiceVersion, ExpectedRenewalEffectID: c.Effect.ID, ExpectedRenewalServiceVersion: c.ServiceVersion}
	r, err := f.db.CreateExternalRefund(WithExternalRefundSession(t.Context(), "order-refund-request"), f.actor, f.renewInvoice, in, "local-refund-create")
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f localRenewalFixture) renewalReview(t *testing.T, r ExternalRefundResult, confirm bool) (ExternalRefundResult, error) {
	t.Helper()
	return f.db.ReviewExternalRefund(WithExternalRefundSession(t.Context(), "order-refund-review"), f.reviewer, f.renewInvoice, r.Refund.ID, confirm, ExternalRefundReviewInput{ExpectedVersion: r.Refund.Version, Note: "independent receipt check"}, fmt.Sprintf("local-review-%v", confirm))
}
func (f localRenewalFixture) exact(t *testing.T, due time.Time, used, generation int64) {
	t.Helper()
	var got time.Time
	var gu, gg int64
	var enabled bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT next_due_at,renewal_discounts_used,renewal_generation,auto_renew FROM services WHERE id=$1`, f.service).Scan(&got, &gu, &gg, &enabled); err != nil {
		t.Fatal(err)
	}
	if !got.Equal(due) || gu != used || gg != generation || (generation > 0 && enabled) {
		t.Fatalf("due=%s used=%d generation=%d auto=%v", got, gu, gg, enabled)
	}
}
func TestLocalRenewalExternalExactLifecycle(t *testing.T) {
	f := newLocalRenewalFixture(t, true, 734)
	ctx := t.Context()
	c := f.renewalContext(t)
	if c.Effect == nil || c.Effect.AfterDue.Month() != time.February || c.Effect.AfterDue.Day() != 29 || c.Effect.BeforeUsed != 0 || c.Effect.AfterUsed != 1 {
		t.Fatal(c)
	}
	var original string
	if err := f.db.Pool.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(p),i.paid_at,(SELECT balance_cents FROM users WHERE id=i.user_id))::text FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id WHERE p.id=$1`, f.payment).Scan(&original); err != nil {
		t.Fatal(err)
	}
	pending := f.renewalCreate(t)
	if pending.Refund.RenewalReversal == nil || pending.Refund.RenewalReversal.Status != "pending" {
		t.Fatal(pending)
	}
	for _, q := range []string{
		`UPDATE services SET auto_renew=true WHERE id=$1`,
		`UPDATE services SET next_due_at=next_due_at+interval '1 month' WHERE id=$1`,
		`INSERT INTO provision_jobs(service_id,action,idempotency_key) VALUES($1,'suspend','hold-job')`,
		`INSERT INTO service_cancellation_requests(service_id,user_id,type,reason,idempotency_key) SELECT id,user_id,'immediate','hold','hold-cancel' FROM services WHERE id=$1`,
		`UPDATE invoice_items SET amount_cents=amount_cents+1 WHERE service_id=$1`,
		`DELETE FROM invoice_items WHERE service_id=$1`,
		`UPDATE payment_transactions SET external_transaction_id='changed' WHERE invoice_id=(SELECT invoice_id FROM invoice_items WHERE service_id=$1 AND item_type='renew')`,
	} {
		if _, err := f.db.Pool.Exec(ctx, q, f.service); err == nil {
			t.Fatalf("hold bypass: %s", q)
		}
	}
	done, err := f.renewalReview(t, pending, true)
	if err != nil {
		t.Fatal(err)
	}
	if done.Refund.RenewalReversal.Status != "applied" {
		t.Fatal(done)
	}
	f.exact(t, f.before, 0, 1)
	var after string
	if err = f.db.Pool.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(p),i.paid_at,(SELECT balance_cents FROM users WHERE id=i.user_id))::text FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id WHERE p.id=$1`, f.payment).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if original != after {
		t.Fatal("external receipt/paid_at/balance changed")
	}
	replay, err := f.renewalReview(t, pending, true)
	if err != nil || !replay.Replayed {
		t.Fatal(replay, err)
	}
}
func TestLocalRenewalBalanceAndZeroExact(t *testing.T) {
	for _, amount := range []int64{0, 734} {
		t.Run(fmt.Sprint(amount), func(t *testing.T) {
			f := newLocalRenewalFixture(t, false, amount)
			r, err := f.db.RefundInvoice(t.Context(), f.actor, f.renewInvoice, "local-balance-refund")
			if err != nil {
				t.Fatal(err)
			}
			if r.AmountCents != amount {
				t.Fatal(r)
			}
			f.exact(t, f.before, 0, 1)
			r, err = f.db.RefundInvoice(t.Context(), f.actor, f.renewInvoice, "local-balance-refund")
			if err != nil || !r.Replayed {
				t.Fatal(r, err)
			}
		})
	}
}
func TestLocalRenewalLIFO(t *testing.T) {
	f := newLocalRenewalFixture(t, false, 734)
	ctx := t.Context()
	r, err := f.db.CreateRenewalInvoice(ctx, f.customer, f.service, "local-second-create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.PayInvoice(ctx, f.customer, r.InvoiceID, "local-second-pay"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.RefundInvoice(ctx, f.actor, f.renewInvoice, "local-first-block"); conflictCode(t, err) != "renewal_successor_exists" {
		t.Fatal(err)
	}
	if _, err = f.db.RefundInvoice(ctx, f.actor, r.InvoiceID, "local-second-refund"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.RefundInvoice(ctx, f.actor, f.renewInvoice, "local-first-refund"); err != nil {
		t.Fatal(err)
	}
	f.exact(t, f.before, 0, 2)
}
func TestLocalRenewalRejectAndSQLProvenance(t *testing.T) {
	f := newLocalRenewalFixture(t, true, 734)
	ctx := t.Context()
	pending := f.renewalCreate(t)
	if _, err := f.db.Pool.Exec(ctx, `UPDATE external_refund_renewal_holds SET status='released',released_at=clock_timestamp() WHERE refund_id=$1`, pending.Refund.ID); err == nil {
		t.Fatal("release without independent rejection")
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE external_refunds SET status='confirmed',reviewed_by=$2,confirmed_at=clock_timestamp() WHERE id=$1`, pending.Refund.ID, f.reviewer); err == nil {
		t.Fatal("confirmation without reversal")
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE service_renewal_effects SET before_used=1 WHERE invoice_id=$1`, f.renewInvoice); err == nil {
		t.Fatal("effect mutation")
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO service_renewal_effects(invoice_id,item_id,service_id,user_id,billing_cycle,before_due,period_start,after_due,before_used,after_used,effective_at,configuration,item_snapshot) SELECT invoice_id,item_id,service_id,user_id,billing_cycle,before_due,period_start,after_due,before_used,after_used,effective_at,configuration,item_snapshot FROM service_renewal_effects WHERE invoice_id=$1`, f.renewInvoice); err == nil {
		t.Fatal("effect forgery")
	}
	if _, err := f.renewalReview(t, pending, false); err != nil {
		t.Fatal(err)
	}
	c := f.renewalContext(t)
	if !c.Eligible || c.RenewalReversal.Status != "released" {
		t.Fatal(c)
	}
	// A paid transition cannot mint entitlement with no actual tender proof.
	r, err := f.db.CreateRenewalInvoice(ctx, f.customer, f.service, "local-unfunded-create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1`, r.InvoiceID); err == nil {
		t.Fatal("unfunded invoice minted effect")
	}
}
func TestLocalRenewalAuditFailureAndRace(t *testing.T) {
	f := newLocalRenewalFixture(t, true, 734)
	pending := f.renewalCreate(t)
	ctx := t.Context()
	if _, err := f.db.Pool.Exec(ctx, `CREATE FUNCTION fail_renewal_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='external_refund.renewal_reversed' THEN RAISE EXCEPTION 'audit unavailable'; END IF;RETURN NEW;END $$;CREATE TRIGGER fail_renewal_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION fail_renewal_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.renewalReview(t, pending, true); err == nil {
		t.Fatal("audit failure not atomic")
	}
	var n int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM service_renewal_reversals WHERE invoice_id=$1`, f.renewInvoice).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if _, err := f.db.Pool.Exec(ctx, `DROP TRIGGER fail_renewal_audit ON audit_events`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for k := 0; k < 2; k++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.renewalReview(t, pending, true); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.exact(t, f.before, 0, 1)
}
func TestLocalRenewalMigrationRepeat(t *testing.T) {
	db := isolatedTestStore(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalRenewalConsumedNullAndDeadline(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name string
		due  *time.Time
		code string
	}{{"consumed", &past, "renewal_period_consumed"}, {"null", nil, "renewal_original_due_unproven"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLocalRenewalFixture(t, true, 734, tc.due)
			c := f.renewalContext(t)
			if c.Eligible || c.ReasonCode != tc.code {
				t.Fatal(c)
			}
		})
	}
	t.Run("cross_review_deadline", func(t *testing.T) {
		// Count the fixture setup separately; create a fresh short future period
		// after its normal first renewal was exactly reversed.
		f := newLocalRenewalFixture(t, false, 734)
		ctx := t.Context()
		if _, err := f.db.RefundInvoice(ctx, f.actor, f.renewInvoice, "deadline-first-reverse"); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(1500 * time.Millisecond).Truncate(time.Microsecond)
		if _, err := f.db.Pool.Exec(ctx, `UPDATE services SET next_due_at=$2 WHERE id=$1`, f.service, deadline); err != nil {
			t.Fatal(err)
		}
		r, err := f.db.CreateRenewalInvoice(ctx, f.customer, f.service, "deadline-renew-create")
		if err != nil {
			t.Fatal(err)
		}
		f.renewInvoice = r.InvoiceID
		p, err := f.db.CreatePaymentIntent(ctx, f.customer, r.InvoiceID, "mock", "deadline-payment", "", "deadline-intent")
		if err != nil {
			t.Fatal(err)
		}
		f.payment = p.PaymentID
		if _, err = f.db.ProcessPaymentWebhook(ctx, "mock", "deadline-event", "deadline-payment", r.InvoiceID, 734, "succeeded", []byte(`{}`), "deadline-original"); err != nil {
			t.Fatal(err)
		}
		pending := f.renewalCreate(t)
		time.Sleep(time.Until(deadline) + 10*time.Millisecond)
		if _, err = f.renewalReview(t, pending, true); conflictCode(t, err) != "renewal_period_consumed" {
			t.Fatal(err)
		}
		if _, err = f.renewalReview(t, pending, false); err != nil {
			t.Fatal(err)
		}
		var due time.Time
		var used int64
		if err = f.db.Pool.QueryRow(ctx, `SELECT next_due_at,renewal_discounts_used FROM services WHERE id=$1`, f.service).Scan(&due, &used); err != nil || !due.After(deadline) || used != 1 {
			t.Fatal(due, used, err)
		}
	})
}
func TestLocalRenewalCreditExact(t *testing.T) {
	f := newLocalRenewalFixture(t, false, 734)
	ctx := t.Context()
	if _, err := f.db.RefundInvoice(ctx, f.actor, f.renewInvoice, "credit-prior-reverse"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE credit_settings SET enabled=true`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `WITH credit AS (UPDATE customer_credit_accounts SET limit_cents=100000 WHERE user_id=$1) INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('local-credit-customer',$1,now()+interval '1 hour')`, f.customer); err != nil {
		t.Fatal(err)
	}
	r, err := f.db.CreateRenewalInvoice(ctx, f.customer, f.service, "credit-local-create")
	if err != nil {
		t.Fatal(err)
	}
	f.renewInvoice = r.InvoiceID
	var version int64
	if err = f.db.Pool.QueryRow(ctx, `SELECT version FROM invoices WHERE id=$1`, r.InvoiceID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.PayInvoiceCredit(ctx, f.customer, r.InvoiceID, "credit-local-pay", version, "local-credit-customer"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.RefundInvoice(WithCreditRefundSession(ctx, "order-refund-request"), f.actor, r.InvoiceID, "credit-local-refund"); err != nil {
		t.Fatal(err)
	}
	f.exact(t, f.before, 0, 2)
	var debt, refunds int64
	if err = f.db.Pool.QueryRow(ctx, `SELECT used_cents,(SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=$2 AND entry_type='refund') FROM customer_credit_accounts WHERE user_id=$1`, f.customer, r.InvoiceID).Scan(&debt, &refunds); err != nil || debt != 0 || refunds != 0 {
		t.Fatal(debt, refunds, err)
	}
}
func TestLocalRenewalAutoGeneration(t *testing.T) {
	f := newLocalRenewalFixture(t, false, 734)
	ctx := t.Context()
	if _, err := f.db.RefundInvoice(ctx, f.actor, f.renewInvoice, "auto-prior-reverse"); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	if _, err := f.db.Pool.Exec(ctx, `UPDATE services SET next_due_at=$2,auto_renew=true WHERE id=$1`, f.service, due); err != nil {
		t.Fatal(err)
	}
	stale := autoRenewCandidate{id: f.service, userID: f.customer, due: due, generation: 0}
	if ok, _ := f.db.attemptAutoRenewal(ctx, stale); ok {
		t.Fatal("stale generation purchased")
	}
	item := stale
	item.generation = 1
	if ok, message := f.db.attemptAutoRenewal(ctx, item); !ok {
		t.Fatal(message)
	}
	var first int64
	if err := f.db.Pool.QueryRow(ctx, `SELECT id FROM invoices WHERE idempotency_key=$1`, fmt.Sprintf("auto-renew-%d-%s-g1", f.service, due.UTC().Format("20060102150405"))).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.RefundInvoice(ctx, f.actor, first, "auto-first-refund"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.SetAutoRenew(ctx, f.customer, f.service, true, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.db.attemptAutoRenewal(ctx, item); ok {
		t.Fatal("stale restored due candidate purchased")
	}
	item.generation = 2
	if ok, message := f.db.attemptAutoRenewal(ctx, item); !ok {
		t.Fatal(message)
	}
	var count int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE idempotency_key LIKE $1`, fmt.Sprintf("auto-renew-%d-%%", f.service)).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}
func TestLocalRenewalMultiCycle(t *testing.T) {
	for _, cycle := range []string{"quarterly", "annually"} {
		t.Run(cycle, func(t *testing.T) {
			f := newLocalRenewalFixture(t, false, 734)
			ctx := t.Context()
			if _, err := f.db.RefundInvoice(ctx, f.actor, f.renewInvoice, "cycle-first-reverse"); err != nil {
				t.Fatal(err)
			}
			before := time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC)
			if _, err := f.db.Pool.Exec(ctx, `UPDATE services SET billing_cycle=$2,next_due_at=$3 WHERE id=$1`, f.service, cycle, before); err != nil {
				t.Fatal(err)
			}
			r, err := f.db.CreateRenewalInvoice(ctx, f.customer, f.service, "multi-cycle-create")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.PayInvoice(ctx, f.customer, r.InvoiceID, "multi-cycle-pay"); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.RefundInvoice(ctx, f.actor, r.InvoiceID, "multi-cycle-refund"); err != nil {
				t.Fatal(err)
			}
			f.exact(t, before, 0, 2)
		})
	}
}
func TestLocalRenewalApplyingCannotCommit(t *testing.T) {
	f := newLocalRenewalFixture(t, true, 734)
	r := f.renewalCreate(t)
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE external_refund_renewal_holds SET status='applying' WHERE refund_id=$1`, r.Refund.ID); err == nil {
		t.Fatal("applying committed")
	}
}

func TestLocalRenewalParentApplicationAndLateEvidence(t *testing.T) {
	f := newLocalRenewalFixture(t, true, 734)
	ctx := t.Context()
	var profile, address, application int64
	for _, q := range []struct {
		sql  string
		dest *int64
	}{
		{`INSERT INTO invoice_profiles(user_id,profile_type,title) VALUES($1,'individual','guard') RETURNING id`, &profile},
		{`INSERT INTO invoice_addresses(user_id,recipient,phone,address) VALUES($1,'guard','123','guard address') RETURNING id`, &address},
	} {
		if err := f.db.Pool.QueryRow(ctx, q.sql, f.customer).Scan(q.dest); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO invoice_applications(user_id,profile_id,address_id,profile_snapshot,address_snapshot,amount_cents,status,review_note) VALUES($1,$2,$3,'{}','{}',734,'rejected','historical rejected') RETURNING id`, f.customer, profile, address).Scan(&application); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO invoice_application_items(application_id,source_invoice_id,invoice_no,description,amount_cents) VALUES($1,$2,'local','guard',734)`, application, f.renewInvoice); err != nil {
		t.Fatal(err)
	}
	pending := f.renewalCreate(t)
	if _, err := f.db.Pool.Exec(ctx, `UPDATE invoice_applications SET status='pending' WHERE id=$1`, application); err == nil {
		t.Fatal("parent application reactivated during hold")
	}
	// Evidence intake must remain possible; it changes neither payment nor
	// entitlement and makes a subsequent confirmation fail conservatively.
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO payment_close_anomalies(payment_id,invoice_id,user_id,provider,event_id,amount_cents,external_transaction_id,payload_sha256) VALUES($1,$2,$3,'mock','local-late-evidence',734,'local-late-tx',$4)`, f.payment, f.renewInvoice, f.customer, strings.Repeat("a", 64)); err != nil {
		t.Fatal("late evidence suppressed", err)
	}
	if _, err := f.renewalReview(t, pending, true); conflictCode(t, err) != "renewal_payment_unresolved" {
		t.Fatal(err)
	}
	if _, err := f.renewalReview(t, pending, false); err != nil {
		t.Fatal(err)
	}
}
func TestLocalRenewalAssociationReverseLocksFailPromptly(t *testing.T) {
	f := newLocalRenewalFixture(t, true, 734)
	ctx := t.Context()
	for _, kind := range []string{"invoice", "service"} {
		t.Run(kind, func(t *testing.T) {
			blocker, err := f.db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(ctx)
			if kind == "invoice" {
				_, err = blocker.Exec(ctx, `SELECT id FROM invoices WHERE id=$1 FOR UPDATE`, f.renewInvoice)
			} else {
				_, err = blocker.Exec(ctx, `SELECT id FROM services WHERE id=$1 FOR UPDATE`, f.service)
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := f.db.Pool.Exec(ctx, `UPDATE payment_transactions SET updated_at=clock_timestamp() WHERE id=$1`, f.payment)
				done <- err
			}()
			select {
			case err = <-done:
				if err == nil {
					t.Fatal("reverse parent lock bypass")
				}
			case <-time.After(time.Second):
				_ = blocker.Rollback(ctx)
				t.Fatal("reverse parent lock blocked")
			}
		})
	}
}
func TestLocalRenewalJobWinsAdmission(t *testing.T) {
	f := newLocalRenewalFixture(t, true, 734)
	ctx := t.Context()
	c := f.renewalContext(t)
	tx, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM services WHERE id=$1 FOR UPDATE`, f.service); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.db.CreateExternalRefund(WithExternalRefundSession(ctx, "order-refund-request"), f.actor, f.renewInvoice, ExternalRefundInput{PaymentID: f.payment, AmountCents: 734, Method: "original_route", ProviderRefundID: "job-race-payout", ReceivedAt: time.Now(), EvidenceRef: "redacted", EvidenceSHA256: strings.Repeat("a", 64), Note: "job race", ExpectedVersion: c.InvoiceVersion, ExpectedRenewalEffectID: c.Effect.ID, ExpectedRenewalServiceVersion: c.ServiceVersion}, "local-job-refund-race")
		done <- err
	}()
	select {
	case err = <-done:
		t.Fatalf("refund did not wait for service writer: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err = tx.Exec(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key) VALUES($1,'suspend','local-job-race')`, f.service); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if conflictCode(t, err) != "renewal_operation_pending" {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("refund stalled after service lock released")
	}
}
