package store

import (
	"context"
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
	must(db.Pool.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at,external_transaction_id) VALUES('test','original-1',$1,$2,100,'succeeded',now()-interval '1 hour','real-merchant-original-1') RETURNING id`, invoice, customer).Scan(&payment))
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
	_, err = db.Pool.Exec(ctx, `UPDATE invoice_applications SET status='rejected',review_note='test cleanup' WHERE id=$1`, application)
	must(err)
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
	must(db.Pool.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at) VALUES('test','original-2',$1,$2,100,'succeeded',now()-interval '1 hour') RETURNING id`, secondInvoice, customer).Scan(&secondPayment))
	_, err = db.Pool.Exec(ctx, `INSERT INTO external_refunds(invoice_id,payment_id,user_id,amount_cents,method,provider,channel_instance_id,provider_payment_id,provider_refund_id,received_at,evidence_ref,evidence_sha256,note,requested_by) SELECT $2,$3,user_id,amount_cents,method,provider,channel_instance_id,'original-2',provider_refund_id,received_at,evidence_ref,evidence_sha256,note,requested_by FROM external_refunds WHERE id=$1`, pending.Refund.ID, secondInvoice, secondPayment)
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
			must(db.Pool.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at) VALUES('test','guard',$1,$2,100,'succeeded',now()-interval '1 hour') RETURNING id`, invoice, customer).Scan(&payment))
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
				_, err = db.Pool.Exec(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at) VALUES('test','guard-mixed',$1,$2,100,'succeeded',now())`, invoice, customer)
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
