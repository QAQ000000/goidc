package httpapi

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func activeSupplierRenewalFixture(t *testing.T) (*supplierFixture, int64) {
	t.Helper()
	f := supplierSetup(t)
	f.publish(t, f.pid, "renewal-concurrency")
	sid, _ := f.purchase(t, f.pid, "renewal-concurrency-order")
	if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
		t.Fatal(err)
	}
	return f, sid
}

func TestSupplierRenewalUsesOneConnection(t *testing.T) {
	f, sid := activeSupplierRenewalFixture(t)
	cfg := f.db.Pool.Config()
	cfg.MaxConns, cfg.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.db.Pool.Close()
	f.db.Pool = pool
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := f.db.SetAutoRenew(ctx, f.uid, sid, true, false); err != nil {
		t.Fatal("auto-renew setting needs a second connection", err)
	}
	out, err := f.db.CreateRenewalInvoice(ctx, f.uid, sid, "single-connection-renewal")
	if err != nil || out.InvoiceID == 0 {
		t.Fatal("renewal needs a second connection", out, err)
	}
}

func TestSupplierRenewalFullPoolMakesProgress(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_key_%v", sameKey), func(t *testing.T) {
			f, sid := activeSupplierRenewalFixture(t)
			original := f.db.Pool
			cfg := original.Config()
			cfg.MaxConns, cfg.MinConns = 10, 0
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			f.db.Pool = pool
			defer func() { f.db.Pool = original; pool.Close() }()
			blocker, err := original.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(t.Context(), "SELECT id FROM services WHERE id=$1 FOR UPDATE", sid); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			errorsOut := make(chan error, 10)
			for i := range 10 {
				go func() {
					key := fmt.Sprintf("pool-renewal-%d", i)
					if sameKey {
						key = "pool-renewal-same-key"
					}
					_, err := f.db.CreateRenewalInvoice(ctx, f.uid, sid, key)
					errorsOut <- err
				}()
			}
			for pool.Stat().AcquiredConns() < 10 && ctx.Err() == nil {
				time.Sleep(10 * time.Millisecond)
			}
			if ctx.Err() != nil {
				t.Fatal("requests did not reach transaction barrier")
			}
			if err = blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			succeeded := 0
			for range 10 {
				err := <-errorsOut
				if err == nil {
					succeeded++
					continue
				}
				var c *store.Conflict
				if !errors.As(err, &c) || c.Code != "renewal_invoice_pending" {
					t.Errorf("expected pending-invoice conflict, got %v", err)
				}
			}
			want := 1
			if sameKey {
				want = 10
			}
			if succeeded != want {
				t.Fatalf("successful requests=%d, want %d", succeeded, want)
			}
			var invoices, audits int
			if err := original.QueryRow(t.Context(), "SELECT count(*) FROM invoice_items WHERE service_id=$1 AND item_type='renew'", sid).Scan(&invoices); err != nil {
				t.Fatal(err)
			}
			if err := original.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='service.renewal.created' AND entity_id=$1", fmt.Sprint(sid)).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if invoices != 1 || audits != 1 {
				t.Fatalf("invoices=%d audits=%d, want one of each", invoices, audits)
			}
		})
	}
}

func TestSupplierRenewalQuotationRechecksConcurrentChanges(t *testing.T) {
	for _, change := range []string{"service_version", "due", "account_version", "account_disabled", "auto_renew_disabled", "same_key"} {
		t.Run(change, func(t *testing.T) {
			f, sid := activeSupplierRenewalFixture(t)
			var due time.Time
			if err := f.db.Pool.QueryRow(t.Context(), "UPDATE services SET auto_renew=true,next_due_at=now()+interval '1 hour' WHERE id=$1 RETURNING next_due_at", sid).Scan(&due); err != nil {
				t.Fatal(err)
			}
			f.syncEntered, f.syncBlocked = make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-f.syncBlocked:
				default:
					close(f.syncBlocked)
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			type result struct {
				invoice store.RenewalResult
				err     error
			}
			done := make(chan result, 1)
			go func() {
				var out store.RenewalResult
				var err error
				if change == "auto_renew_disabled" {
					out, err = f.db.CreateAutoRenewalInvoice(ctx, f.uid, sid, due, "concurrent-quote")
				} else {
					out, err = f.db.CreateRenewalInvoice(ctx, f.uid, sid, "concurrent-quote")
				}
				done <- result{out, err}
			}()
			select {
			case <-f.syncEntered:
			case <-ctx.Done():
				t.Fatal("quotation did not reach upstream")
			}
			// The blocked HTTP request must own no pool connection, service lock
			// or account lock. A concurrent writer must finish immediately.
			if n := f.db.Pool.Stat().AcquiredConns(); n != 0 {
				t.Errorf("quotation holds %d database connections", n)
			}
			update, finish := context.WithTimeout(t.Context(), time.Second)
			var err error
			switch change {
			case "service_version":
				_, err = f.db.Pool.Exec(update, "UPDATE services SET version=version+1 WHERE id=$1", sid)
			case "due":
				_, err = f.db.Pool.Exec(update, "UPDATE services SET next_due_at=next_due_at+interval '1 month' WHERE id=$1", sid)
			case "account_version":
				_, err = f.db.Pool.Exec(update, "UPDATE supplier_accounts SET version=version+1 WHERE id=$1", f.supplierID)
			case "account_disabled":
				_, err = f.db.Pool.Exec(update, "UPDATE supplier_accounts SET active=false,version=version+1 WHERE id=$1", f.supplierID)
			case "auto_renew_disabled":
				err = f.db.SetAutoRenew(update, f.uid, sid, false, false)
			}
			finish()
			if err != nil {
				t.Fatal("local update blocked by upstream quotation", err)
			}
			close(f.syncBlocked)
			first := <-done
			if change == "same_key" {
				if first.err != nil {
					t.Fatal(first.err)
				}
				// Replay must work without another upstream read, even if the
				// account was disabled after invoice creation.
				if _, err = f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET active=false WHERE id=$1", f.supplierID); err != nil {
					t.Fatal(err)
				}
				replay, err := f.db.CreateRenewalInvoice(ctx, f.uid, sid, "concurrent-quote")
				if err != nil || !replay.Replayed || replay.InvoiceID != first.invoice.InvoiceID {
					t.Fatal("idempotent receipt lost", replay, err)
				}
				return
			}
			want := "supplier_renewal_quote_changed"
			switch change {
			case "auto_renew_disabled":
				// The generation/switch guard now rejects this before the older
				// missing-row admission check; require its precise race contract.
				want = "auto_renewal_race"
			case "account_disabled":
				want = "supplier_disabled"
			}
			var c *store.Conflict
			if !errors.As(first.err, &c) || c.Code != want {
				t.Fatalf("expected %s, got %v", want, first.err)
			}
			if first.invoice.InvoiceID != 0 {
				t.Fatal("rejected quotation returned an invoice", first.invoice.InvoiceID)
			}
			var invoices, receipts, audits int
			if err = f.db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM invoice_items WHERE service_id=$1 AND item_type='renew'),
 (SELECT count(*) FROM invoices WHERE user_id=$2 AND idempotency_key='concurrent-quote'),
 (SELECT count(*) FROM audit_events WHERE action='service.renewal.created' AND entity_id=$1::bigint::text)`, sid, f.uid).Scan(&invoices, &receipts, &audits); err != nil {
				t.Fatal(err)
			}
			if invoices != 0 || receipts != 0 || audits != 0 {
				t.Fatalf("rejected quotation persisted items=%d invoices=%d audits=%d", invoices, receipts, audits)
			}
			if change == "auto_renew_disabled" {
				var enabled bool
				if err = f.db.Pool.QueryRow(t.Context(), "SELECT auto_renew FROM services WHERE id=$1", sid).Scan(&enabled); err != nil || enabled {
					t.Fatal("rejected quotation restored automatic renewal", enabled, err)
				}
			}
		})
	}
}
