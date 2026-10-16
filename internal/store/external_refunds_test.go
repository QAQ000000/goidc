package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/codex/zjmfgo/internal/payment"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExternalRefundIndependentFact(t *testing.T) {
	db := isolatedTestStore(t)
	actor, customer := topUpFixture(t, db)
	ctx := t.Context()
	var reviewer, invoice, payment int64
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('refund-reviewer','reviewer@example.test','unused','Reviewer','super_admin') RETURNING id`).Scan(&reviewer))
	_, err := db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('refund-request',$1,now()+interval '1 hour'),('refund-review',$2,now()+interval '1 hour')`, actor, reviewer)
	must(err)
	must(db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents,paid_at) VALUES('ER-1',$1,'manual','paid',100,now()-interval '1 hour') RETURNING id`, customer).Scan(&invoice))
	_, err = db.Pool.Exec(ctx, `INSERT INTO invoice_items(invoice_id,item_type,description,amount_cents) VALUES($1,'manual','Manual external receipt',100)`, invoice)
	must(err)
	must(db.Pool.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at,external_transaction_id) VALUES('mock','original-1',$1,$2,100,'succeeded',now()-interval '1 hour','real-merchant-original-1') RETURNING id`, invoice, customer).Scan(&payment))
	requestCtx := WithExternalRefundSession(ctx, "refund-request")
	reviewCtx := WithExternalRefundSession(ctx, "refund-review")
	in := ExternalRefundInput{PaymentID: payment, AmountCents: 100, Method: "original_route", ProviderRefundID: "refund-1", ReceivedAt: time.Now().Add(-time.Minute), Note: "Actual refund receipt", EvidenceRef: "evidence/redacted", EvidenceSHA256: strings.Repeat("a", 64), ExpectedVersion: 1}
	if _, err = db.CreateExternalRefund(ctx, actor, invoice, in, "missing-session"); err == nil {
		t.Fatal("missing session accepted")
	}
	pending, err := db.CreateExternalRefund(requestCtx, actor, invoice, in, "create-refund-1")
	must(err)
	if pending.Refund.OriginalTransactionID != "real-merchant-original-1" {
		t.Fatal("original merchant transaction snapshot lost")
	}
	replay, err := db.CreateExternalRefund(requestCtx, actor, invoice, in, "create-refund-1")
	must(err)
	if !replay.Replayed {
		t.Fatal("not replayed")
	}
	changed := in
	changed.Note = "different"
	if _, err = db.CreateExternalRefund(requestCtx, actor, invoice, changed, "create-refund-1"); conflictCode(t, err) != "idempotency_conflict" {
		t.Fatal(err)
	}
	review := ExternalRefundReviewInput{ExpectedVersion: 1, Note: "Independent actual receipt verification"}
	if _, err = db.ReviewExternalRefund(requestCtx, actor, invoice, pending.Refund.ID, true, review, "self-review-1"); conflictCode(t, err) != "external_refund_self_review" {
		t.Fatal(err)
	}
	// Simulate the real invoice application transaction owning the same
	// customer advisory lock before adding a pending application.
	applicationTx, err := db.invoiceCustomerTx(ctx, customer)
	must(err)
	var profile, address, application int64
	must(applicationTx.QueryRow(ctx, `INSERT INTO invoice_profiles(user_id,profile_type,title) VALUES($1,'individual','race') RETURNING id`, customer).Scan(&profile))
	must(applicationTx.QueryRow(ctx, `INSERT INTO invoice_addresses(user_id,recipient,phone,address) VALUES($1,'race','123','race address') RETURNING id`, customer).Scan(&address))
	must(applicationTx.QueryRow(ctx, `INSERT INTO invoice_applications(user_id,profile_id,address_id,profile_snapshot,address_snapshot,amount_cents) VALUES($1,$2,$3,'{}','{}',100) RETURNING id`, customer, profile, address).Scan(&application))
	_, err = applicationTx.Exec(ctx, `INSERT INTO invoice_application_items(application_id,source_invoice_id,invoice_no,description,amount_cents) VALUES($1,$2,'ER-1','race',100)`, application, invoice)
	must(err)
	waiting := make(chan error, 1)
	go func() {
		_, e := db.ReviewExternalRefund(reviewCtx, reviewer, invoice, pending.Refund.ID, true, review, "invoice-application-race")
		waiting <- e
	}()
	select {
	case e := <-waiting:
		t.Fatalf("confirmation did not wait for application transaction: %v", e)
	case <-time.After(100 * time.Millisecond):
	}
	must(applicationTx.Commit(ctx))
	select {
	case e := <-waiting:
		if conflictCode(t, e) != "external_refund_not_supported" {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation remained blocked")
	}
	var outgoing int
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FROM external_refunds WHERE status='confirmed'`).Scan(&outgoing))
	if outgoing != 0 {
		t.Fatal("application race produced outgoing fact")
	}
	rejected, err := db.ReviewExternalRefund(reviewCtx, reviewer, invoice, pending.Refund.ID, false, review, "reject-with-active-application")
	must(err)
	if rejected.Refund.Status != "rejected" || rejected.Refund.ConfirmedAt != nil {
		t.Fatal("rejection produced outgoing fact")
	}
	rejectedReplay, err := db.ReviewExternalRefund(reviewCtx, reviewer, invoice, pending.Refund.ID, false, review, "reject-with-active-application")
	must(err)
	if !rejectedReplay.Replayed {
		t.Fatal("rejection did not replay")
	}
	var unchangedInvoice, unchangedPayment string
	var unchangedVersion, audits int64
	must(db.Pool.QueryRow(ctx, `SELECT i.status,i.version,p.status,(SELECT count(*) FROM audit_events WHERE action='external_refund.rejected' AND entity_id=$2) FROM invoices i JOIN payment_transactions p ON p.invoice_id=i.id WHERE i.id=$1`, invoice, fmt.Sprint(pending.Refund.ID)).Scan(&unchangedInvoice, &unchangedVersion, &unchangedPayment, &audits))
	if unchangedInvoice != "paid" || unchangedVersion != 1 || unchangedPayment != "succeeded" || audits != 1 {
		t.Fatal("rejection changed funds or duplicated audit")
	}
	_, err = db.Pool.Exec(ctx, `UPDATE invoice_applications SET status='rejected',review_note='test cleanup' WHERE id=$1`, application)
	must(err)
	in.EvidenceRef = "evidence/corrected-receipt"
	pending, err = db.CreateExternalRefund(requestCtx, actor, invoice, in, "create-refund-after-reject")
	must(err)
	oldReplay, e := db.CreateExternalRefund(requestCtx, actor, invoice, ExternalRefundInput{PaymentID: payment, AmountCents: 100, Method: "original_route", ProviderRefundID: "refund-1", ReceivedAt: in.ReceivedAt, EvidenceRef: "evidence/redacted", EvidenceSHA256: strings.Repeat("a", 64), Note: "Actual refund receipt", ExpectedVersion: 1}, "create-refund-1")
	must(e)
	if !oldReplay.Replayed || oldReplay.Refund.Status != "pending_review" {
		t.Fatal("original create idempotency snapshot overwritten")
	}
	oldReject, e := db.ReviewExternalRefund(reviewCtx, reviewer, invoice, rejected.Refund.ID, false, review, "reject-with-active-application")
	must(e)
	if !oldReject.Replayed || oldReject.Refund.Status != "rejected" {
		t.Fatal("old rejected operation overwritten")
	}
	var history int
	must(db.Pool.QueryRow(ctx, `SELECT count(*) FROM external_refunds WHERE invoice_id=$1`, invoice).Scan(&history))
	if history != 2 {
		t.Fatal("rejected history overwritten")
	}
	if _, e = db.CreateExternalRefund(requestCtx, actor, invoice, in, "duplicate-live-same-invoice"); conflictCode(t, e) != "external_refund_duplicate" {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	results := make(chan ExternalRefundResult, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := db.ReviewExternalRefund(reviewCtx, reviewer, invoice, pending.Refund.ID, true, review, "confirm-refund-1")
			errs <- e
			results <- r
		}()
	}
	wg.Wait()
	close(errs)
	close(results)
	for e := range errs {
		must(e)
	}
	replays := 0
	for r := range results {
		if r.Replayed {
			replays++
		}
		if r.Refund.Status != "confirmed" {
			t.Fatal(r)
		}
	}
	if replays != 1 {
		t.Fatal("concurrent confirmation did not replay exactly once")
	}
	var status, paymentStatus string
	var version, balance, count, ledger int64
	must(db.Pool.QueryRow(ctx, `SELECT i.status,i.version,u.balance_cents,p.status,(SELECT count(*) FROM external_refunds WHERE status='confirmed'),(SELECT count(*) FROM balance_ledger) FROM invoices i JOIN users u ON u.id=i.user_id JOIN payment_transactions p ON p.invoice_id=i.id WHERE i.id=$1`, invoice).Scan(&status, &version, &balance, &paymentStatus, &count, &ledger))
	if status != "refunded" || version != 2 || balance != 0 || paymentStatus != "succeeded" || count != 1 || ledger != 0 {
		t.Fatalf("fact invariant %s %d %d %s %d %d", status, version, balance, paymentStatus, count, ledger)
	}
	_, err = db.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash='refund-review'`)
	must(err)
	if _, err = db.ReviewExternalRefund(reviewCtx, reviewer, invoice, pending.Refund.ID, true, review, "confirm-refund-1"); err == nil {
		t.Fatal("revoked session replay accepted")
	}
	var secondInvoice, secondPayment int64
	must(db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents) VALUES('ER-2',$1,'manual','paid',100) RETURNING id`, customer).Scan(&secondInvoice))
	must(db.Pool.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at) VALUES('mock','original-2',$1,$2,100,'succeeded',now()-interval '1 hour') RETURNING id`, secondInvoice, customer).Scan(&secondPayment))
	_, err = db.Pool.Exec(ctx, `INSERT INTO external_refunds(invoice_id,payment_id,user_id,amount_cents,method,provider,channel_instance_id,provider_payment_id,provider_refund_id,received_at,evidence_ref,evidence_sha256,note,requested_by,merchant_scope) SELECT $2,$3,user_id,amount_cents,method,provider,channel_instance_id,'original-2',provider_refund_id,received_at,evidence_ref,evidence_sha256,note,requested_by,merchant_scope FROM external_refunds WHERE id=$1`, pending.Refund.ID, secondInvoice, secondPayment)
	if err == nil {
		t.Fatal("database allowed merchant refund reference reuse on another invoice/payment")
	}

	_, err = db.Pool.Exec(ctx, `UPDATE external_refunds SET note='overwrite' WHERE id=$1`, pending.Refund.ID)
	if err == nil {
		t.Fatal("confirmed fact mutable")
	}
	_, _, err = db.ListExternalRefunds(context.Background(), actor, invoice, 20, 0)
	must(err)
}

func TestExternalRefundBusinessGuards(t *testing.T) {
	for _, scenario := range []string{"empty_note", "refund_control", "evidence_control", "service", "order", "credit", "application", "mixed", "import", "payment_amount", "payment_status", "future", "before_payment", "version"} {
		t.Run(scenario, func(t *testing.T) {
			db := isolatedTestStore(t)
			actor, customer := topUpFixture(t, db)
			ctx := t.Context()
			var invoice, payment int64
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('guard-session',$1,now()+interval '1 hour')`, actor)
			must(err)
			must(db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents) VALUES('guard',$1,'manual','paid',100) RETURNING id`, customer).Scan(&invoice))
			_, err = db.Pool.Exec(ctx, `INSERT INTO invoice_items(invoice_id,item_type,description,amount_cents) VALUES($1,'manual','guard',100)`, invoice)
			must(err)
			must(db.Pool.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at) VALUES('mock','guard',$1,$2,100,'succeeded',now()-interval '1 hour') RETURNING id`, invoice, customer).Scan(&payment))
			in := ExternalRefundInput{PaymentID: payment, AmountCents: 100, Method: "other", ProviderRefundID: "guard-refund", ReceivedAt: time.Now().Add(-time.Minute), Note: "Actual refund receipt", EvidenceRef: "redacted", EvidenceSHA256: strings.Repeat("a", 64), ExpectedVersion: 1}
			switch scenario {
			case "empty_note":
				in.Note = "  "
			case "refund_control":
				in.ProviderRefundID = "refund\x00id"
			case "evidence_control":
				in.EvidenceRef = "evidence\x00ref"
			case "service", "order":
				var product, order, item, service int64
				must(db.Pool.QueryRow(ctx, `INSERT INTO products(name,slug,price_cents) VALUES('guard-product','guard-product',100) RETURNING id`).Scan(&product))
				must(db.Pool.QueryRow(ctx, `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash) VALUES('guard-order',$1,100,'guard-order-key','guard') RETURNING id`, customer).Scan(&order))
				must(db.Pool.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'guard','monthly',1,100,0,100) RETURNING id`, order, product).Scan(&item))
				must(db.Pool.QueryRow(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle) VALUES($1,$2,$3,$4,'guard','guard.example.test','monthly') RETURNING id`, customer, order, item, product).Scan(&service))
				if scenario == "service" {
					_, err = db.Pool.Exec(ctx, `UPDATE invoice_items SET service_id=$2 WHERE invoice_id=$1`, invoice, service)
				} else {
					_, err = db.Pool.Exec(ctx, `UPDATE invoices SET invoice_kind='automatic',order_id=$2 WHERE id=$1`, invoice, order)
				}
				must(err)
			case "credit":
				_, err = db.Pool.Exec(ctx, `INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key) VALUES($1,$2,100,100,1,'guard-credit-key')`, invoice, customer)
				must(err)
			case "application":
				var profile, address, application int64
				must(db.Pool.QueryRow(ctx, `INSERT INTO invoice_profiles(user_id,profile_type,title) VALUES($1,'individual','guard') RETURNING id`, customer).Scan(&profile))
				must(db.Pool.QueryRow(ctx, `INSERT INTO invoice_addresses(user_id,recipient,phone,address) VALUES($1,'guard','123','guard address') RETURNING id`, customer).Scan(&address))
				must(db.Pool.QueryRow(ctx, `INSERT INTO invoice_applications(user_id,profile_id,address_id,profile_snapshot,address_snapshot,amount_cents) VALUES($1,$2,$3,'{}','{}',100) RETURNING id`, customer, profile, address).Scan(&application))
				_, err = db.Pool.Exec(ctx, `INSERT INTO invoice_application_items(application_id,source_invoice_id,invoice_no,description,amount_cents) VALUES($1,$2,'guard','guard',100)`, application, invoice)
				must(err)
			case "mixed":
				_, err = db.Pool.Exec(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at) VALUES('mock','guard-mixed',$1,$2,100,'succeeded',now())`, invoice, customer)
				must(err)
			case "import":
				_, err = db.Pool.Exec(ctx, `UPDATE invoices SET invoice_kind='automatic',import_review_required=true WHERE id=$1`, invoice)
				must(err)
			case "payment_amount":
				_, err = db.Pool.Exec(ctx, "UPDATE payment_transactions SET amount_cents=99 WHERE id=$1", payment)
				must(err)
			case "payment_status":
				_, err = db.Pool.Exec(ctx, "UPDATE payment_transactions SET status='pending' WHERE id=$1", payment)
				must(err)
			case "future":
				in.ReceivedAt = time.Now().Add(time.Hour)
			case "before_payment":
				in.ReceivedAt = time.Now().Add(-2 * time.Hour)
			case "version":
				in.ExpectedVersion = 9
			}
			if _, err = db.CreateExternalRefund(WithExternalRefundSession(ctx, "guard-session"), actor, invoice, in, "guard-idempotency"); err == nil {
				t.Fatal("ineligible refund accepted")
			}
			var count int
			must(db.Pool.QueryRow(ctx, "SELECT count(*) FROM external_refunds").Scan(&count))
			if count != 0 {
				t.Fatal("failed request left fact")
			}
		})
	}
}

func TestExternalRefundTopupBalanceReservationAndReversal(t *testing.T) {
	db, ctx, actor, uid := onlineTopupFixture(t)
	var reviewer int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('topup-refund-reviewer','topup-refund-reviewer@example.test','unused','Topup refund reviewer','super_admin') RETURNING id`).Scan(&reviewer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('topup-refund-request',$1,now()+interval '1 hour'),('topup-refund-review',$2,now()+interval '1 hour')`, actor, reviewer); err != nil {
		t.Fatal(err)
	}
	requestCtx := WithExternalRefundSession(ctx, "topup-refund-request")
	reviewCtx := WithExternalRefundSession(ctx, "topup-refund-review")
	topup := createTopup(t, db, ctx, uid, 400, "external-topup-create")
	intent, err := topupIntent(db, ctx, uid, topup, "external-topup-intent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = topupSuccess(db, ctx, intent, "external-topup-success"); err != nil {
		t.Fatal(err)
	}
	in := ExternalRefundInput{PaymentID: intent.PaymentID, AmountCents: 400, Method: "original_route", ProviderRefundID: "external-topup-refund", ReceivedAt: time.Now(), EvidenceRef: "test://topup-refund", EvidenceSHA256: strings.Repeat("b", 64), Note: "verified external top-up refund", ExpectedVersion: 2}
	pending, err := db.CreateExternalRefund(requestCtx, actor, topup.InvoiceID, in, "external-topup-refund-create")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Refund.Status != "pending_review" {
		t.Fatalf("pending status=%s", pending.Refund.Status)
	}
	if pending.Refund.Reversal == nil || pending.Refund.Reversal.Status != "pending" || pending.Refund.Reversal.AmountCents != 400 || pending.Refund.Reversal.LedgerID != nil {
		t.Fatalf("missing reservation projection: %+v", pending.Refund.Reversal)
	}
	// A pending reversal must reduce spendable balance even though displayed
	// balance remains unchanged until independent review.
	manual := int64(0)
	if err = db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents) VALUES('TOPUP-RESERVED-SPEND',$1,'manual','unpaid',100) RETURNING id`, uid).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO invoice_items(invoice_id,item_type,description,amount_cents) VALUES($1,'manual','reserved spend',100)`, manual); err != nil {
		t.Fatal(err)
	}
	if _, err = db.PayInvoice(ctx, uid, manual, "topup-reserved-spend", 1); conflictCode(t, err) != "balance_reserved" {
		t.Fatalf("reserved balance spend err=%v", err)
	}
	review := ExternalRefundReviewInput{ExpectedVersion: 1, Note: "reject for corrected evidence"}
	rejected, err := db.ReviewExternalRefund(reviewCtx, reviewer, topup.InvoiceID, pending.Refund.ID, false, review, "external-topup-reject")
	if err != nil || rejected.Refund.Status != "rejected" {
		t.Fatalf("reject=%+v err=%v", rejected, err)
	}
	var balance int64
	if err = db.Pool.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1", uid).Scan(&balance); err != nil || balance != 400 {
		t.Fatalf("reject changed balance=%d err=%v", balance, err)
	}
	if _, err = db.PayInvoice(ctx, uid, manual, "topup-released-spend", 1); err != nil {
		t.Fatalf("released reservation still blocked spend: %v", err)
	}
	// Restore just the legitimately spent amount; this is a real top-up path,
	// not a direct fixture balance overwrite.
	if _, _, err = db.TopUp(ctx, actor, uid, 100, "topup-refund-replenish", "restore spend fixture"); err != nil {
		t.Fatal(err)
	}
	in.EvidenceRef = "test://topup-refund-corrected"
	pending, err = db.CreateExternalRefund(requestCtx, actor, topup.InvoiceID, in, "external-topup-refund-corrected")
	if err != nil {
		t.Fatal(err)
	}
	review.Note = "independently verified corrected top-up refund"
	if _, err = db.Pool.Exec(ctx, `CREATE FUNCTION reject_refund_reversal_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='external_refund.confirmed' THEN RAISE EXCEPTION 'isolated refund audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_refund_reversal_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_refund_reversal_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ReviewExternalRefund(reviewCtx, reviewer, topup.InvoiceID, pending.Refund.ID, true, review, "external-topup-confirm"); err == nil {
		t.Fatal("audit failure accepted confirmation")
	}
	var rollbackBalance, rollbackLedger int64
	var rollbackStatus string
	if err = db.Pool.QueryRow(ctx, `SELECT u.balance_cents,r.status,(SELECT count(*) FROM balance_ledger WHERE entry_type='refund' AND reference_id=$1) FROM users u JOIN external_refunds r ON r.user_id=u.id WHERE r.id=$2`, topup.InvoiceID, pending.Refund.ID).Scan(&rollbackBalance, &rollbackStatus, &rollbackLedger); err != nil || rollbackBalance != 400 || rollbackStatus != "pending_review" || rollbackLedger != 0 {
		t.Fatalf("partial confirmation after audit failure: balance=%d status=%s ledgers=%d err=%v", rollbackBalance, rollbackStatus, rollbackLedger, err)
	}
	if _, err = db.Pool.Exec(ctx, "DROP TRIGGER reject_refund_reversal_audit ON audit_events"); err != nil {
		t.Fatal(err)
	}
	confirmed, err := db.ReviewExternalRefund(reviewCtx, reviewer, topup.InvoiceID, pending.Refund.ID, true, review, "external-topup-confirm")
	if err != nil || confirmed.Refund.Status != "confirmed" {
		t.Fatalf("confirm=%+v err=%v", confirmed, err)
	}
	replay, err := db.ReviewExternalRefund(reviewCtx, reviewer, topup.InvoiceID, pending.Refund.ID, true, review, "external-topup-confirm")
	if err != nil || !replay.Replayed {
		t.Fatalf("confirm replay=%+v err=%v", replay, err)
	}
	var invoiceStatus, topupStatus, reservationStatus string
	var topupLedger, refundLedger, reservations int
	if err = db.Pool.QueryRow(ctx, `SELECT i.status,t.status,r.status,(SELECT count(*) FROM balance_ledger WHERE reference_type='gateway_topup' AND reference_id=i.id),(SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=i.id AND entry_type='refund'),(SELECT count(*) FROM external_refund_reversal_reservations WHERE invoice_id=i.id) FROM invoices i JOIN balance_topups t ON t.invoice_id=i.id JOIN external_refund_reversal_reservations r ON r.invoice_id=i.id AND r.status='applied' WHERE i.id=$1`, topup.InvoiceID).Scan(&invoiceStatus, &topupStatus, &reservationStatus, &topupLedger, &refundLedger, &reservations); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1", uid).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if invoiceStatus != "refunded" || topupStatus != "credited" || reservationStatus != "applied" || topupLedger != 1 || refundLedger != 1 || reservations != 2 || balance != 0 {
		t.Fatalf("invoice=%s topup=%s reservation=%s topupLedger=%d refundLedger=%d reservations=%d balance=%d", invoiceStatus, topupStatus, reservationStatus, topupLedger, refundLedger, reservations, balance)
	}
	var auditActor int64
	if err = db.Pool.QueryRow(ctx, `SELECT actor_id FROM audit_events WHERE action='external_refund.balance_reversed' AND entity_id=$1`, fmt.Sprint(pending.Refund.ID)).Scan(&auditActor); err != nil || auditActor != reviewer {
		t.Fatalf("reversal attributed to wrong actor: %d err=%v", auditActor, err)
	}
	for _, query := range []string{`UPDATE external_refund_reversal_reservations SET amount_cents=399 WHERE refund_id=$1`, `DELETE FROM external_refund_reversal_reservations WHERE refund_id=$1`} {
		if _, err = db.Pool.Exec(ctx, query, pending.Refund.ID); err == nil {
			t.Fatal("confirmed reversal fact mutable")
		}
	}
	// A negative external reversal can never be replayed as a positive local
	// balance refund by guessing its deterministic ledger idempotency key.
	if _, err = db.RefundInvoice(ctx, actor, topup.InvoiceID, fmt.Sprintf("external-refund-balance-%d", pending.Refund.ID)); conflictCode(t, err) != "topup_refund_not_supported" {
		t.Fatalf("negative reversal mistaken for balance refund: %v", err)
	}
}

func TestExternalRefundTopupReservationRacesSpendingAndAdminCredit(t *testing.T) {
	db, ctx, actor, uid := onlineTopupFixture(t)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var reviewer, spendInvoice int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('race-reviewer','race-reviewer@example.test','unused','Reviewer','super_admin') RETURNING id`).Scan(&reviewer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('race-refund-request',$1,now()+interval '1 hour'),('race-refund-review',$2,now()+interval '1 hour')`, actor, reviewer); err != nil {
		t.Fatal(err)
	}
	requestCtx := WithExternalRefundSession(ctx, "race-refund-request")
	reviewCtx := WithExternalRefundSession(ctx, "race-refund-review")
	topup := createTopup(t, db, ctx, uid, 400, "race-refund-topup")
	intent, err := topupIntent(db, ctx, uid, topup, "race-refund-intent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = topupSuccess(db, ctx, intent, "race-refund-success"); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,amount_cents) VALUES('RACE-REFUND-SPEND',$1,'manual',100) RETURNING id`, uid).Scan(&spendInvoice); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO invoice_items(invoice_id,item_type,description,amount_cents) VALUES($1,'manual','race spend',100)`, spendInvoice); err != nil {
		t.Fatal(err)
	}
	in := ExternalRefundInput{PaymentID: intent.PaymentID, AmountCents: 400, Method: "bank_transfer", ProviderRefundID: "race-refund-receipt", ReceivedAt: time.Now(), EvidenceRef: "test://race", EvidenceSHA256: strings.Repeat("c", 64), Note: "race verified receipt", ExpectedVersion: 2}
	type result struct {
		action string
		refund ExternalRefundResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 3)
	go func() {
		<-start
		r, e := db.CreateExternalRefund(requestCtx, actor, topup.InvoiceID, in, "race-refund-register")
		results <- result{"refund", r, e}
	}()
	go func() {
		<-start
		_, e := db.PayInvoice(ctx, uid, spendInvoice, "race-refund-pay", 1)
		results <- result{action: "spend", err: e}
	}()
	go func() {
		<-start
		_, _, e := db.TopUp(ctx, actor, uid, 100, "race-refund-credit", "")
		results <- result{action: "credit", err: e}
	}()
	close(start)
	var pending ExternalRefundResult
	for range 3 {
		r := <-results
		if r.err != nil {
			code := conflictCode(t, r.err)
			if !(r.action == "refund" && code == "external_refund_balance_reserved") && !(r.action == "spend" && code == "balance_reserved") {
				t.Fatalf("%s: %v", r.action, r.err)
			}
		} else if r.action == "refund" {
			pending = r.refund
		}
	}
	if pending.Refund.ID == 0 {
		pending, err = db.CreateExternalRefund(requestCtx, actor, topup.InvoiceID, in, "race-refund-register")
		if err != nil {
			t.Fatal(err)
		}
	}
	var balance, reserved, delta int64
	if err = db.Pool.QueryRow(ctx, `SELECT balance_cents,(SELECT coalesce(sum(amount_cents),0)::bigint FROM external_refund_reversal_reservations WHERE user_id=$1 AND status='pending'),(SELECT coalesce(sum(delta_cents),0)::bigint FROM balance_ledger WHERE user_id=$1) FROM users WHERE id=$1`, uid).Scan(&balance, &reserved, &delta); err != nil || balance < reserved || reserved != 400 || balance != delta {
		t.Fatalf("reservation conservation balance=%d reserve=%d ledger=%d err=%v", balance, reserved, delta, err)
	}
	if _, err = db.ReviewExternalRefund(reviewCtx, reviewer, topup.InvoiceID, pending.Refund.ID, true, ExternalRefundReviewInput{ExpectedVersion: 1, Note: "independently checked"}, "race-refund-confirm"); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT balance_cents,(SELECT coalesce(sum(delta_cents),0)::bigint FROM balance_ledger WHERE user_id=$1),(SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=$2 AND entry_type='refund') FROM users WHERE id=$1`, uid, topup.InvoiceID).Scan(&balance, &delta, &reserved); err != nil || balance != delta || reserved != 1 || balance < 0 {
		t.Fatalf("confirmed conservation balance=%d ledger=%d refunds=%d err=%v", balance, delta, reserved, err)
	}
}

func TestExternalRefundMerchantIdentity(t *testing.T) {
	db := isolatedTestStore(t)
	db.ProvisionEncryptionKey = []byte(strings.Repeat("k", 32))
	actor, customer := topUpFixture(t, db)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var reviewer int64
	must(db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('merchant-review','merchant-review@example.test','unused','Reviewer','super_admin') RETURNING id`).Scan(&reviewer))
	_, err := db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('merchant-request',$1,now()+interval '1 hour'),('merchant-review',$2,now()+interval '1 hour');`, actor, reviewer)
	must(err)
	_, err = db.Pool.Exec(ctx, `INSERT INTO channel_instances(id,category,implementation_id,display_name) VALUES('merchant-copy-a','payment','epay','A'),('merchant-copy-b','payment','epay','B')`)
	must(err)
	requestCtx := WithExternalRefundSession(ctx, "merchant-request")
	reviewCtx := WithExternalRefundSession(ctx, "merchant-review")
	fixture := func(name, provider, instance, api, pid string, corrupt bool) (int64, ExternalRefundInput) {
		var invoice, paymentID int64
		must(db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents) VALUES($1,$2,'manual','paid',100) RETURNING id`, name, customer).Scan(&invoice))
		_, e := db.Pool.Exec(ctx, `INSERT INTO invoice_items(invoice_id,item_type,description,amount_cents) VALUES($1,'manual','merchant test',100)`, invoice)
		must(e)
		var sealed []byte
		if api != "" {
			raw, _ := json.Marshal(payment.EpayConfig{APIURL: api, PID: pid, Key: "isolated-secret"})
			sealed, e = db.paymentCredentials(name, raw, true)
			must(e)
		}
		if corrupt {
			sealed = []byte("corrupt ciphertext")
		}
		must(db.Pool.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,channel_instance_id,invoice_id,user_id,amount_cents,status,completed_at,credential_snapshot) VALUES($1,$2,$3,$4,$5,100,'succeeded',now()-interval '1 hour',$6) RETURNING id`, provider, name, instance, invoice, customer, append([]byte{}, sealed...)).Scan(&paymentID))
		return invoice, ExternalRefundInput{PaymentID: paymentID, AmountCents: 100, Method: "original_route", ProviderRefundID: "same-merchant-refund", ReceivedAt: time.Now().Add(-time.Minute), EvidenceRef: "test://merchant", EvidenceSHA256: strings.Repeat("a", 64), Note: "verified receipt", ExpectedVersion: 1}
	}
	invoice, in := fixture("merchant-1", "epay_alipay", "merchant-copy-a", "HTTPS://Merchant.Example:443/gateway/", " 10001 ", false)
	first, err := db.CreateExternalRefund(requestCtx, actor, invoice, in, "merchant-register-1")
	must(err)
	for _, tc := range []struct {
		name, provider, instance, api, pid string
		allowed                            bool
	}{
		{"cross-provider", "epay_wxpay", "merchant-copy-a", "https://merchant.example/gateway", "10001", false},
		{"copy-instance", "epay_alipay", "merchant-copy-b", "https://MERCHANT.EXAMPLE:443/gateway///", "10001", false},
		{"different-pid", "epay_wxpay", "merchant-copy-b", "https://merchant.example/gateway", "10002", true},
		{"different-host", "epay_wxpay", "merchant-copy-b", "https://other.example/gateway", "10001", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invoice, in := fixture(tc.name, tc.provider, tc.instance, tc.api, tc.pid, false)
			_, e := db.CreateExternalRefund(requestCtx, actor, invoice, in, "merchant-key-"+tc.name)
			if tc.allowed {
				must(e)
			} else if conflictCode(t, e) != "external_refund_duplicate" {
				t.Fatal(e)
			}
		})
	}
	for _, tc := range []struct {
		name, provider, api string
		corrupt             bool
	}{{"missing-snapshot", "epay_alipay", "", false}, {"corrupt-snapshot", "epay_wxpay", "https://merchant.example", true}, {"unknown-provider", "unknown", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			invoice, in := fixture(tc.name, tc.provider, "merchant-copy-a", tc.api, "10001", tc.corrupt)
			if _, e := db.CreateExternalRefund(requestCtx, actor, invoice, in, "merchant-key-"+tc.name); conflictCode(t, e) != "external_refund_merchant_unavailable" {
				t.Fatal(e)
			}
		})
	}
	// Wrong runtime encryption key makes the immutable original snapshot unreadable.
	db.ProvisionEncryptionKey = []byte(strings.Repeat("x", 32))
	review := ExternalRefundReviewInput{ExpectedVersion: 1, Note: "independent verification"}
	if _, err = db.ReviewExternalRefund(reviewCtx, reviewer, invoice, first.Refund.ID, true, review, "merchant-confirm-bad-key"); conflictCode(t, err) != "external_refund_merchant_unavailable" {
		t.Fatal(err)
	}
	rejected, err := db.ReviewExternalRefund(reviewCtx, reviewer, invoice, first.Refund.ID, false, review, "merchant-reject-bad-key")
	must(err)
	if rejected.Refund.Status != "rejected" {
		t.Fatal("unreadable snapshot prevented rejection")
	}
}
