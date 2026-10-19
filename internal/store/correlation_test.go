package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/certification"
	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCorrelationPoolLeaseClearsCancelledAndConcurrentOrigins(t *testing.T) {
	db := isolatedTestStore(t)
	cfg := db.Pool.Config()
	cfg.MaxConns, cfg.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var processID int
	check := func(ctx context.Context, want string) error {
		var got string
		var pid int
		if err := pool.QueryRow(ctx, `SELECT coalesce(current_setting('goidc.correlation_id',true),''),pg_backend_pid()`).Scan(&got, &pid); err != nil {
			return err
		}
		if processID == 0 {
			processID = pid
		}
		if got != want || pid != processID {
			return fmt.Errorf("lease correlation=%q want=%q pid=%d wantpid=%d", got, want, pid, processID)
		}
		return nil
	}
	a, b := correlation.NewID(), correlation.NewID()
	for _, id := range []string{a, b, "", a, ""} {
		if err := check(correlation.WithID(t.Context(), id), id); err != nil {
			t.Fatal(err)
		}
	}
	// Cancelling a request while it holds a transaction must not contaminate
	// the next request on that same physical connection.
	ctx, cancel := context.WithCancel(correlation.WithID(t.Context(), a))
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := check(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := correlation.NewID()
			errs <- check(correlation.WithID(t.Context(), id), id)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := check(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestCorrelationPersistsAuditAndQueueOriginAcrossRetryAndRestart(t *testing.T) {
	f := lifecycleBusinessFixture(t)
	origin := correlation.NewID()
	f.ctx = correlation.WithID(f.ctx, origin)
	out, _ := lifecycleCheckout(t, f, 1)
	f.exec(t, `UPDATE users SET balance_cents=100000 WHERE id=$1`, f.customer)
	if _, err := f.db.PayInvoice(f.ctx, f.customer, out.InvoiceID, "correlation-first-pay"); err != nil {
		t.Fatal(err)
	}
	// The same transaction's direct SQL, Store writes and audit all retain the
	// original request; the business request_id inside payload remains intact.
	var emailID int64
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(f.ctx, `INSERT INTO email_outbox(recipient,subject,body,kind) VALUES('trace@example.test','Trace','Body','system') RETURNING id`).Scan(&emailID); err != nil {
			return err
		}
		sealed, err := smsCrypt(f.db.ProvisionEncryptionKey, []byte("SMS fixture"), "body:+8613800000000:", true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(f.ctx, `INSERT INTO sms_outbox(recipient,body_encrypted,kind,expires_at) VALUES('+8613800000000',$1,'system',now()+interval '1 day')`, sealed); err != nil {
			return err
		}
		return Audit(f.ctx, tx, f.actor, "trace.fixture", "order", out.OrderID, map[string]any{"request_id": "business-key-preserved"})
	}); err != nil {
		t.Fatal(err)
	}
	var actual string
	var payload []byte
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT correlation_id,payload FROM audit_events WHERE action='trace.fixture'`).Scan(&actual, &payload); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil || actual != origin || fields["request_id"] != "business-key-preserved" {
		t.Fatalf("audit lost origin or business key: correlation=%q payload=%s err=%v", actual, payload, err)
	}
	// A new pool simulates the loss of all in-memory request context on restart.
	pool, err := pgxpool.NewWithConfig(t.Context(), f.db.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	restarted := &Store{Pool: pool, ProvisionEncryptionKey: f.db.ProvisionEncryptionKey}
	provision, err := restarted.ClaimProvisionJob(t.Context())
	if err != nil || provision == nil || provision.CorrelationID != origin {
		t.Fatalf("provision claim lost durable origin: %+v err=%v", provision, err)
	}
	if err := restarted.ProcessProvisionJob(correlation.JobContext(t.Context(), provision.CorrelationID), *provision); err != nil {
		t.Fatal(err)
	}
	sms, err := restarted.ClaimSMSJob(t.Context())
	if err != nil || sms == nil || sms.CorrelationID != origin || sms.Body != "SMS fixture" {
		t.Fatalf("SMS claim lost durable origin or payload: correlation=%q err=%v", func() string {
			if sms == nil {
				return ""
			}
			return sms.CorrelationID
		}(), err)
	}
	notification, err := restarted.ClaimNotificationJob(t.Context())
	if err != nil || notification == nil || notification.CorrelationID != origin {
		t.Fatalf("notification claim lost durable origin: job_id/correlation=%+v err=%v", notification, err)
	}
	job, err := restarted.ClaimEmailJob(t.Context())
	if err != nil || job.ID != emailID || job.CorrelationID != origin {
		t.Fatalf("claim lost durable origin: job=%+v err=%v", job, err)
	}
	if err := restarted.CompleteEmailJob(correlation.JobContext(t.Context(), job.CorrelationID), *job, errors.New("fixture transient")); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE email_outbox SET available_at=now() WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = restarted.ClaimEmailJob(correlation.WithID(t.Context(), correlation.NewID()))
	if err != nil || job.CorrelationID != origin || job.Attempts != 2 {
		t.Fatalf("retry changed original request: job=%+v err=%v", job, err)
	}
	for _, table := range []string{"notification_events", "notification_deliveries"} {
		var total, matching int
		if err := pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE correlation_id=$1) FROM `+table, origin).Scan(&total, &matching); err != nil || total == 0 || total != matching {
			t.Fatalf("%s lost origin total=%d matching=%d err=%v", table, total, matching, err)
		}
	}
}

func TestCorrelationCertificationRestoresOriginAfterClaim(t *testing.T) {
	f := certificationProviderFixture(t)
	probe := &certificationProbe{submitResult: certification.Result{Status: "approved", ProviderSubject: "correlation-subject"}}
	f.instance(t, "cert-correlation", "certification_fixture", "fixture-token", probe)
	f.selectInstance(t, "cert-correlation", "certification_fixture")
	origin := correlation.NewID()
	out, err := f.db.SubmitCertification(correlation.WithID(t.Context(), origin), f.customer, "individual", map[string]any{"name": "认证夹具姓名", "id_number": "PRIVATE-ID-FIXTURE"})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := f.db.ProcessCertificationJob(correlation.WithID(t.Context(), correlation.NewID())); err != nil || !processed {
		t.Fatalf("certification processed=%v err=%v", processed, err)
	}
	var actual string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT correlation_id FROM certification_provider_jobs WHERE submission_id=$1`, out["id"]).Scan(&actual); err != nil || actual != origin {
		t.Fatalf("certification origin=%q err=%v", actual, err)
	}
	var mismatched int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE entity_type='certification_submission' AND entity_id=$1::text AND correlation_id IS DISTINCT FROM $2::text`, fmt.Sprint(out["id"]), origin).Scan(&mismatched); err != nil || mismatched != 0 {
		t.Fatalf("certification audit origin changed: mismatched=%d err=%v", mismatched, err)
	}
}

func TestCorrelationRejectsMalformedDatabaseValues(t *testing.T) {
	db := isolatedTestStore(t)
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,correlation_id) VALUES('trace@example.test','Trace','Body','system','not-a-trace')`); err == nil {
		t.Fatal("database accepted malformed origin")
	}
	var id *string
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind) VALUES('trace@example.test','Trace','Body','system') RETURNING correlation_id`).Scan(&id); err != nil || id != nil {
		t.Fatalf("unrelated writer inherited origin: %v err=%v", id, err)
	}
}

func TestCorrelationUpgrade176DoesNotFabricateHistoricalOrigins(t *testing.T) {
	db := supplierExpirySchema171(t)
	for _, name := range []string{supplierExpiryMigration172, "173_notification_deadline_recovery.sql", "174_web_push.sql", "175_supplier_catalog_import.sql", "176_supplier_calendar_cycles.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	supplierLifecycleUpgradeHistory(t, db)
	before := supplierLifecycleUpgradeFacts(t, db)
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind) VALUES('historical@example.test','Historical','Body','system');
INSERT INTO sms_outbox(recipient,body_encrypted,kind,expires_at) VALUES('+8613800000000','fixture'::bytea,'system',now()+interval '1 day');
INSERT INTO audit_events(action,entity_type,entity_id,payload) VALUES('historical.fixture','fixture','1','{}');
INSERT INTO scheduler_runs(task_key) VALUES('automatic_renewals');
INSERT INTO admin_api_logs(method,path,status_code,duration_ms) VALUES('GET','/historical',200,1)`); err != nil {
		t.Fatal(err)
	}
	cfg := db.Pool.Config()
	ConfigurePoolCorrelation(cfg)
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	upgraded := &Store{Pool: pool}
	if err := upgraded.Migrate(correlation.WithID(t.Context(), correlation.NewID())); err != nil {
		t.Fatal(err)
	}
	if before != supplierLifecycleUpgradeFacts(t, upgraded) {
		t.Fatal("correlation migration changed existing service/payment/job facts")
	}
	for _, table := range []string{"email_outbox", "sms_outbox", "provision_jobs", "audit_events", "scheduler_runs", "admin_api_logs"} {
		var total, fabricated int
		if err := pool.QueryRow(t.Context(), `SELECT count(*),count(correlation_id) FROM `+table).Scan(&total, &fabricated); err != nil || total == 0 || fabricated != 0 {
			t.Fatalf("%s historical origins fabricated: total=%d known=%d err=%v", table, total, fabricated, err)
		}
	}
	if err := upgraded.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCorrelationPoolHookOverhead(t *testing.T) {
	db := isolatedTestStore(t)
	start := time.Now()
	for range 100 {
		var n int
		if err := db.Pool.QueryRow(correlation.WithID(t.Context(), correlation.NewID()), `SELECT 1`).Scan(&n); err != nil || n != 1 {
			t.Fatal("pool smoke failed", err)
		}
	}
	t.Logf("100 serial leases including correlation setup/cleanup: %s", time.Since(start))
}
