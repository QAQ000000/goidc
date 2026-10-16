package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestExternalRefundResolvedTopupDatabaseCreditProof(t *testing.T) {
	db, ctx, actor, uid, topup, payment := topupLiabilityFixture(t)
	credit, err := db.ResolveTopupLiability(ctx, actor, topup.InvoiceID, ResolveTopupLiabilityInput{ExpectedVersion: 2, Note: "isolated proof"}, "proof-resolution")
	if err != nil {
		t.Fatal(err)
	}
	requestCtx := WithExternalRefundSession(ctx, "liability-admin-session")
	in := ExternalRefundInput{PaymentID: payment.PaymentID, AmountCents: 500, Method: "original_route", ProviderRefundID: "proof-refund", ReceivedAt: time.Now(), EvidenceRef: "test://credit-proof", EvidenceSHA256: strings.Repeat("a", 64), Note: "isolated credit proof", ExpectedVersion: credit.Version}
	pending, err := db.CreateExternalRefund(requestCtx, actor, topup.InvoiceID, in, "proof-refund-create")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]string{
		"amount":    `UPDATE balance_ledger SET delta_cents=499 WHERE id=$1`,
		"owner":     `UPDATE balance_ledger SET user_id=` + fmt.Sprint(actor) + ` WHERE id=$1`,
		"invoice":   `UPDATE balance_ledger SET reference_id=reference_id+100000 WHERE id=$1`,
		"reference": `UPDATE balance_ledger SET reference_type='admin_topup' WHERE id=$1`,
		"entry":     `UPDATE balance_ledger SET entry_type='refund' WHERE id=$1`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, e := db.Pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback(context.Background())
			if _, e = tx.Exec(ctx, mutation, credit.Resolution.LedgerID); e != nil {
				t.Fatal(e)
			}
			var valid bool
			if e = tx.QueryRow(ctx, `SELECT external_refund_topup_credit_valid($1,$2,$3,500)`, topup.InvoiceID, payment.PaymentID, uid).Scan(&valid); e != nil || valid {
				t.Fatalf("invalid credit accepted: %v/%v", valid, e)
			}
			_, e = tx.Exec(ctx, `UPDATE external_refund_reversal_reservations SET status='pending' WHERE refund_id=$1`, pending.Refund.ID)
			var pgerr *pgconn.PgError
			if !errors.As(e, &pgerr) || pgerr.Code != "23514" {
				t.Fatalf("DB accepted broken source chain: %v", e)
			}
		})
	}
	// A debit with the original credit key or unrelated key is never proof of
	// the independent reversal; the DB validates its negative fact and own key.
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var negative int64
	if err = tx.QueryRow(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note) VALUES($1,'refund',-500,0,'invoice',$2,'wrong-reversal-key','fixture') RETURNING id`, uid, topup.InvoiceID).Scan(&negative); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE external_refund_reversal_reservations SET status='applied',ledger_id=$2,applied_at=clock_timestamp() WHERE refund_id=$1`, pending.Refund.ID, negative); err == nil {
		t.Fatal("DB accepted unrelated negative ledger")
	}
}
