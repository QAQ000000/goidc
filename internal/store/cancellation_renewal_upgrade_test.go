package store

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const cancellationSchema168 = "168_captcha_providers.sql"
const cancellationSchema169 = "169_cancellation_renewal_admission.sql"

// This schema is born from the real embedded SQL through 153, then advances
// through the actual 154..168 migrations. Migration 169 has never run here.
func cancellationUpgradeSchema168(t *testing.T) *Store {
	t.Helper()
	db := isolatedOrderRefundSchema153(t)
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		if name > "153_resolved_topup_external_refunds.sql" && name <= cancellationSchema168 {
			applyNotificationUpgradeMigration(t, db, name)
		}
	}
	var boundary string
	var hasNewObjects bool
	if err = db.Pool.QueryRow(t.Context(), `SELECT max(version),
EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema()
 AND ((table_name='service_cancellation_requests' AND column_name='approval_generation')
 OR (table_name='provision_jobs' AND column_name='cancellation_request_id')))
 OR to_regprocedure('cancellation_renewal_payment_unresolved(bigint)') IS NOT NULL
FROM schema_migrations`).Scan(&boundary, &hasNewObjects); err != nil || boundary != cancellationSchema168 || hasNewObjects {
		t.Fatalf("not an actual untouched schema168: boundary=%s has169=%v err=%v", boundary, hasNewObjects, err)
	}
	return db
}

type cancellationUpgradeFixture struct {
	actor, customer, service, request, job, invoice, payment int64
}

// Upgrade and database-guard fixtures share explicit funding facts on both
// predecessor and current schemas. Seed them in a transaction, independently of
// today's authenticated API and the schema's administrator identity model.
func seedUpgradeFixtureAdminTopUp(t *testing.T, db *Store, actor, customer, amount int64, key, note string) {
	t.Helper()
	err := db.WithTx(t.Context(), func(tx pgx.Tx) error {
		var balance int64
		if err := tx.QueryRow(t.Context(), "UPDATE users SET balance_cents=balance_cents+$2,updated_at=now() WHERE id=$1 AND role='customer' RETURNING balance_cents", customer, amount).Scan(&balance); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,idempotency_key,note) VALUES($1,'topup',$2,$3,'admin_topup',$4,$5)`, customer, amount, balance, key, note); err != nil {
			return err
		}
		return Audit(t.Context(), tx, actor, "balance.topped_up", "user", customer, map[string]any{"amount_cents": amount, "balance_cents": balance})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Seed a small, explicit historical service. Historical receipt settlement uses
// the real schema168 DB trigger, not current Go admission code or fabricated
// renewal_effect rows. No external merchant or provisioner is contacted.
func cancellationUpgradeService(t *testing.T, db *Store) cancellationUpgradeFixture {
	t.Helper()
	f := cancellationUpgradeFixture{}
	f.actor, f.customer = topUpFixture(t, db)
	seedUpgradeFixtureAdminTopUp(t, db, f.actor, f.customer, 5000, "cancellation-upgrade-funding", "isolated upgrade fixture")
	var product, order, item int64
	queries := []struct {
		query string
		args  []any
		id    *int64
	}{
		{`INSERT INTO products(name,slug,price_cents,stock,provision_mode) VALUES('Cancellation upgrade','cancellation-upgrade',1000,2,'simulation') RETURNING id`, nil, &product},
		{`INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES('CANCEL-UPGRADE-O',$1,'active',1000,'cancellation-upgrade-order','fixture') RETURNING id`, []any{f.customer}, &order},
		{`INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'Cancellation upgrade','monthly',1,1000,0,1000) RETURNING id`, []any{order, product}, &item},
	}
	// The dependent IDs are populated between statements.
	for i := range queries {
		if i == 2 {
			queries[i].args = []any{order, product}
		}
		if err := db.Pool.QueryRow(t.Context(), queries[i].query, queries[i].args...).Scan(queries[i].id); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode,next_due_at)
VALUES($1,$2,$3,$4,'Cancellation upgrade','cancellation-upgrade.example.test','monthly','active','simulation',clock_timestamp()+interval '10 days') RETURNING id`, f.customer, order, item, product).Scan(&f.service); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,idempotency_key)
VALUES('CANCEL-UPGRADE-R',$1,NULL,'unpaid',1000,'cancellation-upgrade-renewal') RETURNING id`, f.customer).Scan(&f.invoice); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id)
VALUES($1,'Historical renewal','renew',1000,$2)`, f.invoice, f.service); err != nil {
		t.Fatal(err)
	}
	return f
}

func cancellationUpgradeApproved(t *testing.T, db *Store, f *cancellationUpgradeFixture) {
	t.Helper()
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO service_cancellation_requests(service_id,user_id,type,reason,status,idempotency_key,resolved_by,resolved_at,resolution_note)
VALUES($1,$2,'end_of_billing','Historical approved cancellation','approved','cancellation-upgrade-request',$3,clock_timestamp()-interval '1 minute','Historical approval note') RETURNING id`, f.service, f.customer, f.actor).Scan(&f.request); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,available_at)
SELECT id,'terminate',$2,next_due_at FROM services WHERE id=$1 RETURNING id`, f.service, fmt.Sprintf("service-cancellation-%d", f.request)).Scan(&f.job); err != nil {
		t.Fatal(err)
	}
}

func cancellationUpgradePaidRenewal(t *testing.T, db *Store, f cancellationUpgradeFixture) {
	t.Helper()
	if err := db.WithTx(t.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), "UPDATE users SET balance_cents=balance_cents-1000 WHERE id=$1", f.customer); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note)
SELECT id,'payment',-1000,balance_cents,'invoice',$2,'cancellation-upgrade-old-payment','schema168 historical payment' FROM users WHERE id=$1`, f.customer, f.invoice); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), "UPDATE invoices SET status='paid',paid_at=clock_timestamp() WHERE id=$1", f.invoice)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func cancellationUpgradeIntent(t *testing.T, db *Store, f *cancellationUpgradeFixture, status string) {
	t.Helper()
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,idempotency_key,checkout_url,credential_snapshot)
VALUES('epay_alipay','cancellation-upgrade-provider-payment',$1,$2,1000,$3,'cancellation-upgrade-intent','https://merchant.example.test/isolated',decode('cafe','hex')) RETURNING id`, f.invoice, f.customer, status).Scan(&f.payment); err != nil {
		t.Fatal(err)
	}
}

func cancellationUpgradeFinancialSnapshot(t *testing.T, db *Store) string {
	t.Helper()
	var value string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
'users',`+historicalAccountSnapshotRows(t, db)+`,
'services',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM services x),
'invoices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),
'items',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoice_items x),
'payments',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM payment_transactions x),
'ledger',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM balance_ledger x),
'effects',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM service_renewal_effects x))::text`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCancellationRenewalUpgradeFrom168(t *testing.T) {
	for _, state := range []string{"paid", "pending", "expired"} {
		t.Run(state, func(t *testing.T) {
			db := cancellationUpgradeSchema168(t)
			f := cancellationUpgradeService(t, db)
			cancellationUpgradeApproved(t, db, &f)
			if state == "paid" {
				cancellationUpgradePaidRenewal(t, db, f)
			} else {
				cancellationUpgradeIntent(t, db, &f, state)
			}
			financial := cancellationUpgradeFinancialSnapshot(t, db)
			var oldRequest, oldJob string
			if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT to_jsonb(c)::text FROM service_cancellation_requests c WHERE id=$1),
(SELECT (to_jsonb(j)-'correlation_id')::text FROM provision_jobs j WHERE id=$2)`, f.request, f.job).Scan(&oldRequest, &oldJob); err != nil {
				t.Fatal(err)
			}
			if err := db.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := db.CheckSchema(t.Context()); err != nil {
				t.Fatal(err)
			}
			if actual := cancellationUpgradeFinancialSnapshot(t, db); actual != financial {
				t.Fatal("upgrade changed original money, intent, receipt or entitlement facts")
			}
			var requestStatus, jobStatus, auditRequest, auditJob string
			var fk, generation int64
			if err := db.Pool.QueryRow(t.Context(), `SELECT c.status,c.approval_generation,j.status,j.cancellation_request_id,
((a.payload->'previous_request')-'approval_generation')::text,((a.payload->'previous_job')-'cancellation_request_id')::text
FROM service_cancellation_requests c JOIN provision_jobs j ON j.id=$2
JOIN audit_events a ON a.action='service.cancellation.reopened_for_renewal' AND a.payload->>'request_id'=c.id::text
WHERE c.id=$1`, f.request, f.job).Scan(&requestStatus, &generation, &jobStatus, &fk, &auditRequest, &auditJob); err != nil {
				t.Fatal(err)
			}
			if requestStatus != "pending" || generation != 0 || jobStatus != "cancelled" || fk != f.request || auditRequest != oldRequest || auditJob != oldJob {
				t.Fatalf("history not reopened with exact old snapshots: request=%s generation=%d job=%s fk=%d requestSnapshot=%v jobSnapshot=%v", requestStatus, generation, jobStatus, fk, auditRequest == oldRequest, auditJob == oldJob)
			}
			if state != "paid" {
				if _, err := db.ResolveServiceCancellation(t.Context(), f.actor, f.request, "approved", "cannot approve before receipt"); conflictCode(t, err) != "invoice_payment_pending" {
					t.Fatal(err)
				}
				// Store settlement assumes the caller verified the callback; this is
				// synthetic verified evidence, not a real merchant operation.
				receipt := []byte(`{"fixture":"cancellation-upgrade-receipt"}`)
				if _, err := db.ProcessPaymentWebhook(t.Context(), "epay_alipay", "cancellation-upgrade-event", "cancellation-upgrade-provider-payment", f.invoice, 1000, "succeeded", receipt, "cancellation-upgrade-external-receipt"); err != nil {
					t.Fatalf("pre-existing receipt lost after migration: %v", err)
				}
			}
			approved, err := db.ResolveServiceCancellation(t.Context(), f.actor, f.request, "approved", "review current entitlement after upgrade")
			if err != nil || approved.JobID == nil || *approved.JobID == f.job {
				t.Fatalf("reapproval could not create a new job: %v err=%v", approved.JobID, err)
			}
			var newKey string
			var available, due time.Time
			if err = db.Pool.QueryRow(t.Context(), `SELECT c.approval_generation,j.idempotency_key,j.available_at,s.next_due_at,j.cancellation_request_id
FROM service_cancellation_requests c JOIN provision_jobs j ON j.id=$2 JOIN services s ON s.id=c.service_id WHERE c.id=$1`, f.request, *approved.JobID).Scan(&generation, &newKey, &available, &due, &fk); err != nil {
				t.Fatal(err)
			}
			if generation != 1 || newKey != fmt.Sprintf("service-cancellation-%d-1", f.request) || fk != f.request || !available.Equal(due) {
				t.Fatalf("new generation lost fresh entitlement: generation=%d key=%s fk=%d available=%s due=%s", generation, newKey, fk, available, due)
			}
			if err = db.Migrate(t.Context()); err != nil {
				t.Fatalf("repeat migration failed: %v", err)
			}
			t.Logf("actual168->169 state=%s original_money_preserved=true old_job=%d cancelled new_job=%d generation=%d current_due=%s", state, f.job, *approved.JobID, generation, due.Format(time.RFC3339))
		})
	}
}

func TestCancellationRenewalUpgradeRejectsAmbiguousHistory(t *testing.T) {
	for _, state := range []string{"running", "running_dispatched", "pending_dispatched", "waiting_manual", "waiting_verification", "another_pending"} {
		t.Run(state, func(t *testing.T) {
			db := cancellationUpgradeSchema168(t)
			f := cancellationUpgradeService(t, db)
			cancellationUpgradeApproved(t, db, &f)
			cancellationUpgradePaidRenewal(t, db, f)
			if state == "another_pending" {
				if _, err := db.Pool.Exec(t.Context(), `INSERT INTO service_cancellation_requests(service_id,user_id,type,reason,idempotency_key)
VALUES($1,$2,'immediate','Another pending cancellation','cancellation-upgrade-another-request')`, f.service, f.customer); err != nil {
					t.Fatal(err)
				}
			} else {
				jobState := strings.TrimSuffix(state, "_dispatched")
				if _, err := db.Pool.Exec(t.Context(), `UPDATE provision_jobs SET status=$2,external_started_at=CASE WHEN $3 THEN clock_timestamp() ELSE NULL END WHERE id=$1`, f.job, jobState, strings.HasSuffix(state, "_dispatched") || state == "waiting_verification"); err != nil {
					t.Fatal(err)
				}
			}
			before := cancellationUpgradeFinancialSnapshot(t, db)
			var oldRequests, oldJobs string
			if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT jsonb_agg(to_jsonb(c) ORDER BY id)::text FROM service_cancellation_requests c),
(SELECT jsonb_agg(to_jsonb(j)-'correlation_id' ORDER BY id)::text FROM provision_jobs j)`).Scan(&oldRequests, &oldJobs); err != nil {
				t.Fatal(err)
			}
			err := db.Migrate(t.Context())
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "cancellation_renewal_requires_reconciliation" || !strings.Contains(pg.Detail, fmt.Sprintf("service_id=%d request_id=%d job_id=%d", f.service, f.request, f.job)) {
				t.Fatalf("unsafe migration lacked precise reconciliation error: %v", err)
			}
			var boundary, requests, jobs string
			var hasNewObjects bool
			var auditCount int
			if err = db.Pool.QueryRow(t.Context(), `SELECT max(version),
EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND
 ((table_name='service_cancellation_requests' AND column_name='approval_generation') OR (table_name='provision_jobs' AND column_name='cancellation_request_id')))
 OR to_regprocedure('cancellation_renewal_payment_unresolved(bigint)') IS NOT NULL,
(SELECT count(*) FROM audit_events WHERE action='service.cancellation.reopened_for_renewal'),
(SELECT jsonb_agg(to_jsonb(c) ORDER BY id)::text FROM service_cancellation_requests c),
(SELECT jsonb_agg(to_jsonb(j)-'correlation_id' ORDER BY id)::text FROM provision_jobs j)
FROM schema_migrations`).Scan(&boundary, &hasNewObjects, &auditCount, &requests, &jobs); err != nil {
				t.Fatal(err)
			}
			if boundary != cancellationSchema168 || hasNewObjects || auditCount != 0 || requests != oldRequests || jobs != oldJobs || cancellationUpgradeFinancialSnapshot(t, db) != before {
				t.Fatalf("unsafe upgrade was not atomic: boundary=%s has169=%v audit=%d requestsSame=%v jobsSame=%v", boundary, hasNewObjects, auditCount, requests == oldRequests, jobs == oldJobs)
			}
			t.Logf("actual168 unsafe=%s migration169 fully rolled back with IDs in error detail", state)
		})
	}
}

func cancellationUpgradeExpectSQLGuard(t *testing.T, err error, code string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != code {
		t.Fatalf("wanted SQL guard %s, got %v", code, err)
	}
}

func TestCancellationRenewalDatabaseGuards(t *testing.T) {
	t.Run("approved_blocks_paid_and_intent", func(t *testing.T) {
		db := isolatedTestStore(t)
		f := cancellationUpgradeService(t, db)
		request, err := db.RequestServiceCancellation(t.Context(), f.customer, f.service, "end_of_billing", "Stop after current entitlement", "cancellation-sql-request")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ResolveServiceCancellation(t.Context(), f.actor, request.ID, "approved", "current entitlement checked"); err != nil {
			t.Fatal(err)
		}
		before := cancellationUpgradeFinancialSnapshot(t, db)
		err = db.WithTx(t.Context(), func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), "UPDATE users SET balance_cents=balance_cents-1000 WHERE id=$1", f.customer); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), "UPDATE invoices SET status='paid',paid_at=clock_timestamp() WHERE id=$1", f.invoice)
			return err
		})
		cancellationUpgradeExpectSQLGuard(t, err, "service_cancellation_approved")
		if cancellationUpgradeFinancialSnapshot(t, db) != before {
			t.Fatal("direct paid SQL altered balance or entitlement despite refusal")
		}
		for _, provider := range []string{"epay_alipay", "mock"} {
			_, err = db.Pool.Exec(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,idempotency_key)
VALUES($1,$2,$3,$4,1000,'pending',$2)`, provider, "cancellation-sql-blocked-"+provider, f.invoice, f.customer)
			cancellationUpgradeExpectSQLGuard(t, err, "service_cancellation_approved")
		}
		if cancellationUpgradeFinancialSnapshot(t, db) != before {
			t.Fatal("direct intent SQL persisted payment or modified financial facts")
		}
	})
	t.Run("pending_real_intent_blocks_direct_approval", func(t *testing.T) {
		db := isolatedTestStore(t)
		f := cancellationUpgradeService(t, db)
		request, err := db.RequestServiceCancellation(t.Context(), f.customer, f.service, "end_of_billing", "Review outstanding collection", "cancellation-sql-pending-request")
		if err != nil {
			t.Fatal(err)
		}
		cancellationUpgradeIntent(t, db, &f, "pending")
		before := cancellationUpgradeFinancialSnapshot(t, db)
		_, err = db.Pool.Exec(t.Context(), "UPDATE service_cancellation_requests SET status='approved',resolved_at=clock_timestamp(),resolved_by=$2 WHERE id=$1", request.ID, f.actor)
		cancellationUpgradeExpectSQLGuard(t, err, "invoice_payment_pending")
		// A second approved row must not bypass the update trigger while the
		// original request and its real collection are still pending.
		_, err = db.Pool.Exec(t.Context(), `INSERT INTO service_cancellation_requests(service_id,user_id,type,reason,status,idempotency_key,resolved_at,resolved_by)
VALUES($1,$2,'end_of_billing','Direct SQL approval must check pending money','approved','cancellation-sql-insert-approved',clock_timestamp(),$3)`, f.service, f.customer, f.actor)
		cancellationUpgradeExpectSQLGuard(t, err, "invoice_payment_pending")
		var status string
		var requests int
		if err = db.Pool.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM service_cancellation_requests WHERE service_id=$2)
FROM service_cancellation_requests WHERE id=$1`, request.ID, f.service).Scan(&status, &requests); err != nil || status != "pending" || requests != 1 || cancellationUpgradeFinancialSnapshot(t, db) != before {
			t.Fatalf("direct approval guard lost pending collection: status=%s requests=%d err=%v", status, requests, err)
		}
	})
}
