package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func auditRenewalApplication(t *testing.T, f localRenewalFixture) int64 {
	t.Helper()
	ctx := t.Context()
	var profile, address, application int64
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.db.Pool.QueryRow(ctx, `INSERT INTO invoice_profiles(user_id,profile_type,title) VALUES($1,'individual','independent audit') RETURNING id`, f.customer).Scan(&profile))
	must(f.db.Pool.QueryRow(ctx, `INSERT INTO invoice_addresses(user_id,recipient,phone,address) VALUES($1,'audit','123','audit address') RETURNING id`, f.customer).Scan(&address))
	must(f.db.Pool.QueryRow(ctx, `INSERT INTO invoice_applications(user_id,profile_id,address_id,profile_snapshot,address_snapshot,amount_cents,status,review_note) VALUES($1,$2,$3,'{}','{}',734,'rejected','rejected before refund') RETURNING id`, f.customer, profile, address).Scan(&application))
	_, err := f.db.Pool.Exec(ctx, `INSERT INTO invoice_application_items(application_id,source_invoice_id,invoice_no,description,amount_cents) SELECT $1,id,invoice_no,'audit',amount_cents FROM invoices WHERE id=$2`, application, f.renewInvoice)
	must(err)
	return application
}

func auditRenewalConstraint(t *testing.T, err error, reason string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != reason {
		t.Fatalf("expected 23514/%s, got %v", reason, err)
	}
}

func TestIndependentRenewalHalfApplyingCannotCommit(t *testing.T) {
	f := newLocalRenewalFixture(t, true, 734)
	pending := f.renewalCreate(t)
	ctx := t.Context()
	tx, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE external_refund_renewal_holds SET status='applying' WHERE refund_id=$1`, pending.Refund.ID); err != nil {
		t.Fatal(err)
	}
	auditRenewalConstraint(t, tx.Commit(ctx), "renewal_refund_incomplete")
	var status string
	if err = f.db.Pool.QueryRow(ctx, `SELECT status FROM external_refund_renewal_holds WHERE refund_id=$1`, pending.Refund.ID).Scan(&status); err != nil || status != "pending" {
		t.Fatal(status, err)
	}
	// A second half-finished attempt cannot change the service without the
	// invoice-trigger-created immutable reversal, even with the exact target due.
	tx2, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback(ctx)
	if _, err = tx2.Exec(ctx, `UPDATE external_refund_renewal_holds SET status='applying' WHERE refund_id=$1`, pending.Refund.ID); err != nil {
		t.Fatal(err)
	}
	_, err = tx2.Exec(ctx, `UPDATE services SET next_due_at=$2,renewal_discounts_used=0,auto_renew=false,auto_renew_error='续费已冲正，自动续费已关闭',renewal_generation=renewal_generation+1 WHERE id=$1`, f.service, f.before)
	auditRenewalConstraint(t, err, "renewal_refund_hold")
	if err = tx2.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.renewalReview(t, pending, true); err != nil {
		t.Fatal(err)
	}
	f.exact(t, f.before, 0, 1)
}

func TestIndependentRenewalParentApplicationFrozen(t *testing.T) {
	f := newLocalRenewalFixture(t, true, 734)
	application := auditRenewalApplication(t, f)
	pending := f.renewalCreate(t)
	for _, sql := range []string{`UPDATE invoice_applications SET status='pending' WHERE id=$1`, `DELETE FROM invoice_applications WHERE id=$1`} {
		_, err := f.db.Pool.Exec(t.Context(), sql, application)
		auditRenewalConstraint(t, err, "renewal_refund_hold")
	}
	if _, err := f.renewalReview(t, pending, false); err != nil {
		t.Fatal(err)
	}
	// Release really removes the freeze, rather than rejecting every parent write.
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE invoice_applications SET status='pending' WHERE id=$1`, application); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentRenewalParentInvoiceLockDoesNotWait(t *testing.T) {
	f := newLocalRenewalFixture(t, true, 734)
	application := auditRenewalApplication(t, f)
	tx, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `SELECT id FROM invoices WHERE id=$1 FOR UPDATE`, f.renewInvoice); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	waitCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err = f.db.Pool.Exec(waitCtx, `UPDATE invoice_applications SET status='pending' WHERE id=$1`, application)
	auditRenewalConstraint(t, err, "renewal_refund_hold")
	if time.Since(start) > 2*time.Second {
		t.Fatal("parent writer waited for the already-locked invoice")
	}
}
