package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestOrderExternalRefundJobIdentityCannotMoveIntoOrOutOfOrder(t *testing.T) {
	for _, hold := range []bool{false, true} {
		t.Run(fmt.Sprint(hold), func(t *testing.T) {
			f := newOrderRefundFixture(t, false)
			ctx := t.Context()
			target := f.in.ExpectedServiceVersions[0].ID
			var otherOrder, otherItem, otherService, job int64
			if e := f.db.Pool.QueryRow(ctx, `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,review_status) VALUES('JOB-OTHER',$1,100,'job-other-order','hash','pending') RETURNING id`, f.customer).Scan(&otherOrder); e != nil {
				t.Fatal(e)
			}
			if e := f.db.Pool.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'other','monthly',1,100,0,100) RETURNING id`, otherOrder, f.product).Scan(&otherItem); e != nil {
				t.Fatal(e)
			}
			if e := f.db.Pool.QueryRow(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle) VALUES($1,$2,$3,$4,'other','job-other-service','monthly') RETURNING id`, f.customer, otherOrder, otherItem, f.product).Scan(&otherService); e != nil {
				t.Fatal(e)
			}
			if e := f.db.Pool.QueryRow(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key,status) VALUES($1,'create','job-other-history','failed') RETURNING id`, otherService).Scan(&job); e != nil {
				t.Fatal(e)
			}
			var pending ExternalRefundResult
			if hold {
				pending = f.create(t)
			}
			for _, query := range []string{`UPDATE provision_jobs SET service_id=$2 WHERE id=$1`, `UPDATE provision_jobs SET action='sync' WHERE id=$1`} {
				args := []any{job}
				if query == `UPDATE provision_jobs SET service_id=$2 WHERE id=$1` {
					args = append(args, target)
				}
				if _, e := f.db.Pool.Exec(ctx, query, args...); e == nil {
					t.Fatal("job identity moved into eligible/held target")
				}
			}
			var owner, hist int64
			if e := f.db.Pool.QueryRow(ctx, `SELECT service_id,(SELECT count(*) FROM external_refund_delivery_history WHERE order_id=$2) FROM provision_jobs WHERE id=$1`, job, f.order).Scan(&owner, &hist); e != nil || owner != otherService || hist != 0 {
				t.Fatal(owner, hist, e)
			}
			// State/attempt changes remain legal even while an unrelated target service
			// is locked. No new job -> service parent wait is introduced by the guard.
			blocker, e := f.db.Pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback(ctx)
			if _, e = blocker.Exec(ctx, `SELECT id FROM services WHERE id=$1 FOR UPDATE`, otherService); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() {
				_, e := f.db.Pool.Exec(ctx, `UPDATE provision_jobs SET status='pending',attempts=attempts+1,locked_at=NULL WHERE id=$1`, job)
				done <- e
			}()
			select {
			case e = <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(2 * time.Second):
				_ = blocker.Rollback(ctx)
				t.Fatal("job status update acquired service lock")
			}
			if e = blocker.Rollback(ctx); e != nil {
				t.Fatal(e)
			}
			if hold {
				if _, e = f.review(t, pending, true, "job-identity-hold-confirm"); e != nil {
					t.Fatal(e)
				}
			} else {
				c, e := f.db.ExternalRefundOrderContext(WithExternalRefundSession(ctx, "order-refund-request"), f.actor, f.invoice)
				if e != nil || !c.Eligible {
					t.Fatal(c, e)
				}
			}
			// The failed create's original parent retains durable taint even after a
			// deletion, so a job cannot be moved out to erase its actual birth history.
			if _, e = f.db.Pool.Exec(ctx, `UPDATE provision_jobs SET service_id=$2 WHERE id=$1`, job, target); e == nil {
				t.Fatal("job moved out of its original order")
			}
			if _, e = f.db.Pool.Exec(ctx, `DELETE FROM provision_jobs WHERE id=$1`, job); e != nil {
				t.Fatal(e)
			}
			if e = f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM external_refund_delivery_history WHERE order_id=$1 AND event='job_history'`, otherOrder).Scan(&hist); e != nil || hist != 1 {
				t.Fatal("job history erased", hist, e)
			}
		})
	}
}

func TestOrderExternalRefundAffiliateBucketsPreserveInvoiceAccountLockOrder(t *testing.T) {
	f := newOrderRefundFixture(t, false)
	ctx := t.Context()
	f.db.ProvisionEncryptionKey = make([]byte, 32)
	var owner, account, entry int64
	if e := f.db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name) VALUES('guard-affiliate-owner','guard-affiliate@example.test','unused','Affiliate') RETURNING id`).Scan(&owner); e != nil {
		t.Fatal(e)
	}
	if e := f.db.Pool.QueryRow(ctx, `INSERT INTO affiliate_accounts(user_id,program_id,status) SELECT $1,id,'approved' FROM affiliate_programs ORDER BY id LIMIT 1 RETURNING id`, owner).Scan(&account); e != nil {
		t.Fatal(e)
	}
	if e := f.db.Pool.QueryRow(ctx, `INSERT INTO affiliate_commission_entries(account_id,referred_user_id,order_id,invoice_id,order_item_id,product_id,event_type,status,amount_cents,basis_cents,rate_bps,available_at,rule_snapshot,source_snapshot,idempotency_key,available_cents)
 SELECT $1,$2,$3,$4,oi.id,oi.product_id,'first_purchase','available',100,2400,1000,now(),'{}','{}','guard-affiliate-commission',100 FROM order_items oi WHERE order_id=$3 LIMIT 1 RETURNING id`, account, f.customer, f.order, f.invoice).Scan(&entry); e != nil {
		t.Fatal(e)
	}
	var holds int
	if e := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM external_refund_order_holds`).Scan(&holds); e != nil || holds != 0 {
		t.Fatal(holds, e)
	}
	// A financial transaction holds invoice first and will next acquire the
	// affiliate account (the RefundInvoice/commission-release order). The real
	// withdrawal holds account -> entry; its bucket write must finish without
	// trying to take this invoice back in the opposite direction.
	financial, e := f.db.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer financial.Rollback(ctx)
	if _, e = financial.Exec(ctx, `SELECT id FROM invoices WHERE id=$1 FOR UPDATE`, f.invoice); e != nil {
		t.Fatal(e)
	}
	type withdrawalResult struct {
		out map[string]any
		err error
	}
	done := make(chan withdrawalResult, 1)
	go func() {
		out, _, e := f.db.CreateAffiliateWithdrawal(ctx, owner, "guard-affiliate-withdraw", AffiliateWithdrawalInput{AmountCents: 50, Method: "alipay", Target: "isolated-withdrawal@example.test", Holder: "测试"})
		done <- withdrawalResult{out, e}
	}()
	var withdrawal map[string]any
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		withdrawal = r.out
	case <-time.After(2 * time.Second):
		_ = financial.Rollback(ctx)
		t.Fatal("real withdrawal bucket update waited for invoice parent")
	}
	if withdrawal["status"] != "submitted" {
		t.Fatal(withdrawal)
	}
	// Retain all parent checks for changes outside the explicit bucket/state
	// allowlist. This also proves the successful withdrawal did not disable guard.
	_, e = f.db.Pool.Exec(ctx, `UPDATE affiliate_commission_entries SET rate_bps=rate_bps-1 WHERE id=$1`, entry)
	var pgerr *pgconn.PgError
	if !isPGErrorCode(e, "23514", &pgerr) {
		t.Fatalf("non-bucket mutation escaped invoice guard: %v", e)
	}
	if e = financial.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	var available, reserved int64
	if e = f.db.Pool.QueryRow(ctx, `SELECT available_cents,reserved_cents FROM affiliate_commission_entries WHERE id=$1`, entry).Scan(&available, &reserved); e != nil || available != 50 || reserved != 50 {
		t.Fatal(available, reserved, e)
	}
}

// Keep the assertion local to this regression rather than weakening the
// normal application errors into assumed conflicts.
func isPGErrorCode(err error, code string, out **pgconn.PgError) bool {
	return errors.As(err, out) && (*out).Code == code
}

func TestOrderExternalRefundAffiliateInsertAllowsConcurrentSharedInvoiceReaders(t *testing.T) {
	f := newOrderRefundFixture(t, false)
	ctx := t.Context()
	var owner, account int64
	if e := f.db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name) VALUES('shared-affiliate-owner','shared-affiliate@example.test','unused','Affiliate') RETURNING id`).Scan(&owner); e != nil {
		t.Fatal(e)
	}
	if e := f.db.Pool.QueryRow(ctx, `INSERT INTO affiliate_accounts(user_id,program_id,status) SELECT $1,id,'approved' FROM affiliate_programs ORDER BY id LIMIT 1 RETURNING id`, owner).Scan(&account); e != nil {
		t.Fatal(e)
	}
	// QueueAffiliateCommissions obtains invoice SHARE before serializing on the
	// customer advisory. Both callers may therefore own SHARE simultaneously;
	// the first inserts while the second still owns its shared parent lock.
	reader, e := f.db.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Rollback(ctx)
	writer, e := f.db.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer writer.Rollback(ctx)
	for _, tx := range []pgx.Tx{reader, writer} {
		if _, e = tx.Exec(ctx, `SELECT id FROM invoices WHERE id=$1 FOR SHARE`, f.invoice); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = writer.Exec(ctx, `INSERT INTO affiliate_turnover_entries(invoice_id,account_id,basis_cents,paid_at) VALUES($1,$2,2400,now())`, f.invoice, account); e != nil {
		t.Fatal(e)
	}
	if _, e = writer.Exec(ctx, `INSERT INTO affiliate_commission_entries(account_id,referred_user_id,order_id,invoice_id,order_item_id,product_id,event_type,amount_cents,basis_cents,rate_bps,available_at,rule_snapshot,source_snapshot,idempotency_key)
 SELECT $1,$2,$3,$4,oi.id,oi.product_id,'first_purchase',100,2400,1000,now(),'{}','{}','shared-invoice-commission' FROM order_items oi WHERE order_id=$3 LIMIT 1`, account, f.customer, f.order, f.invoice); e != nil {
		t.Fatal(e)
	}
	if e = writer.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = reader.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	c, e := f.db.ExternalRefundOrderContext(WithExternalRefundSession(ctx, "order-refund-request"), f.actor, f.invoice)
	if e != nil || c.Eligible || c.ReasonCode != "external_refund_affiliate_linked" {
		t.Fatal(c, e)
	}
}

func TestOrderExternalRefundAffiliateInsertStillRejectsPendingHold(t *testing.T) {
	f := newOrderRefundFixture(t, false)
	ctx := t.Context()
	var account int64
	if e := f.db.Pool.QueryRow(ctx, `INSERT INTO affiliate_accounts(user_id,program_id,status) SELECT $1,id,'approved' FROM affiliate_programs ORDER BY id LIMIT 1 RETURNING id`, f.actor).Scan(&account); e != nil {
		t.Fatal(e)
	}
	f.create(t)
	queries := []string{`INSERT INTO affiliate_turnover_entries(invoice_id,account_id,basis_cents,paid_at) VALUES($1,$2,2400,now())`,
		`INSERT INTO affiliate_commission_entries(account_id,referred_user_id,order_id,invoice_id,order_item_id,product_id,event_type,amount_cents,basis_cents,rate_bps,available_at,rule_snapshot,source_snapshot,idempotency_key)
 SELECT $2,o.user_id,o.id,$1,oi.id,oi.product_id,'first_purchase',100,2400,1000,now(),'{}','{}','held-invoice-commission' FROM orders o JOIN order_items oi ON oi.order_id=o.id JOIN invoices i ON i.order_id=o.id WHERE i.id=$1 LIMIT 1`}
	for _, query := range queries {
		_, e := f.db.Pool.Exec(ctx, query, f.invoice, account)
		var pe *pgconn.PgError
		if !errors.As(e, &pe) || pe.Code != "23514" || pe.Message != "external_refund_order_hold" {
			t.Fatal("shared invoice insert bypassed pending hold", e)
		}
	}
}
