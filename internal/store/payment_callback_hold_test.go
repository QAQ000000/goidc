package store

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// The callback now owns its invoice before the payment write. Owning that row
// must still reject a genuine refund hold, including both sides of a move.
func TestPaymentCallbackInvoicePrelockPreservesOrderRefundHold(t *testing.T) {
	f := newOrderRefundFixture(t, false)
	ctx := t.Context()
	var otherInvoice int64
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,amount_cents) VALUES('PAYMENT-HOLD-OTHER',$1,2400) RETURNING id`, f.customer).Scan(&otherInvoice); err != nil {
		t.Fatal(err)
	}
	other, err := f.db.CreatePaymentIntent(ctx, f.customer, otherInvoice, "mock", "payment-hold-other", "", "payment-hold-other-key")
	if err != nil {
		t.Fatal(err)
	}
	pending := f.create(t) // Real checkout, settlement and refund admission.
	var before string
	if err = f.db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(p) ORDER BY id)::text FROM payment_transactions p`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, query string
		args        []any
	}{
		{"transaction_metadata", `UPDATE payment_transactions SET external_transaction_id='hold-bypass' WHERE id=$1`, []any{f.payment}},
		{"settlement_status", `UPDATE payment_transactions SET status='failed' WHERE id=$1`, []any{f.payment}},
		{"move_from_held_invoice", `UPDATE payment_transactions SET invoice_id=$2 WHERE id=$1`, []any{f.payment, otherInvoice}},
		{"move_into_held_invoice", `UPDATE payment_transactions SET invoice_id=$2 WHERE id=$1`, []any{other.PaymentID, f.invoice}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx, err := f.db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `SELECT id FROM invoices WHERE id=ANY($1) ORDER BY id FOR UPDATE`, []int64{f.invoice, otherInvoice}); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, test.query, test.args...)
			var pgerr *pgconn.PgError
			if !isPGErrorCode(err, "23514", &pgerr) || pgerr.Message != "external_refund_order_hold" {
				t.Fatalf("invoice ownership bypassed real refund hold: %v", err)
			}
		})
	}
	var after string
	var held bool
	if err = f.db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(p) ORDER BY id)::text FROM payment_transactions p`).Scan(&after); err != nil || before != after {
		t.Fatal("rejected writes changed payment facts", err)
	}
	if err = f.db.Pool.QueryRow(ctx, `SELECT status='pending' FROM external_refund_order_holds WHERE refund_id=$1`, pending.Refund.ID).Scan(&held); err != nil || !held {
		t.Fatal("real refund hold lost", err)
	}
}
