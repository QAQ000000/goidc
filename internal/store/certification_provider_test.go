package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/certification"
	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var certificationFixtureOnce sync.Once
var certificationFixtureProviders sync.Map

type certificationProbe struct {
	db                  *Store
	mu                  sync.Mutex
	submits, queries    int
	tokens              []string
	requests            []string
	submitResult        certification.Result
	queryResult         certification.Result
	submitErr, queryErr error
}

type certificationProbeProvider struct{ probe *certificationProbe }

func (p certificationProbeProvider) checkNoTransaction(ctx context.Context, requestID string) error {
	return p.probe.db.WithTx(ctx, func(tx pgx.Tx) error {
		var id int64
		return tx.QueryRow(ctx, `SELECT p.id FROM certification_profiles p JOIN certification_submissions sub ON sub.profile_id=p.id WHERE sub.request_id=$1 FOR UPDATE OF p NOWAIT`, requestID).Scan(&id)
	})
}

func (p certificationProbeProvider) Submit(ctx context.Context, in certification.Submission) (certification.Result, error) {
	if err := p.checkNoTransaction(ctx, in.RequestID); err != nil {
		return certification.Result{}, errors.New("external call retained a profile transaction")
	}
	p.probe.mu.Lock()
	defer p.probe.mu.Unlock()
	p.probe.submits++
	p.probe.requests = append(p.probe.requests, in.RequestID)
	return p.probe.submitResult, p.probe.submitErr
}

func (p certificationProbeProvider) Query(ctx context.Context, in certification.Query) (certification.Result, error) {
	if err := p.checkNoTransaction(ctx, in.RequestID); err != nil {
		return certification.Result{}, errors.New("external query retained a profile transaction")
	}
	p.probe.mu.Lock()
	defer p.probe.mu.Unlock()
	p.probe.queries++
	p.probe.requests = append(p.probe.requests, in.RequestID)
	return p.probe.queryResult, p.probe.queryErr
}

type certificationRuntimeFixture struct {
	db              *Store
	actor, customer int64
}

func certificationProviderFixture(t *testing.T) certificationRuntimeFixture {
	t.Helper()
	certificationFixtureOnce.Do(func() {
		for _, definition := range []struct {
			id   string
			caps []string
		}{{"certification_fixture", []string{"submit", "query", "query_by_request_id"}}, {"certification_no_query_fixture", []string{"submit"}}} {
			err := certification.RegisterFactory(channel.Descriptor{ID: definition.id, Name: definition.id, Category: channel.Certification, Capabilities: definition.caps, Fields: []channel.Field{
				{Key: "account", Label: "Account", Type: channel.Text, Required: true},
				{Key: "token", Label: "Token", Type: channel.Text, Required: true, Secret: true},
			}}, func(cfg module.Config) (certification.Provider, error) {
				value, exists := certificationFixtureProviders.Load(configString(cfg.Values, "account"))
				if !exists {
					return nil, errors.New("fixture account unavailable")
				}
				probe := value.(*certificationProbe)
				probe.mu.Lock()
				probe.tokens = append(probe.tokens, cfg.Secrets["token"])
				probe.mu.Unlock()
				return certificationProbeProvider{probe}, nil
			})
			if err != nil {
				panic(err)
			}
		}
	})
	db := isolatedTestStore(t)
	db.ProvisionEncryptionKey = make([]byte, 32)
	actor, customer := topUpFixture(t, db)
	return certificationRuntimeFixture{db, actor, customer}
}

func (f certificationRuntimeFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.db.Pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (f certificationRuntimeFixture) instance(t *testing.T, id, implementation, token string, probe *certificationProbe) {
	t.Helper()
	probe.db = f.db
	account := channelID()
	certificationFixtureProviders.Store(account, probe)
	t.Cleanup(func() { certificationFixtureProviders.Delete(account) })
	sealed, err := channelSeal(f.db.ProvisionEncryptionKey, id, token, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"account": account})
	f.exec(t, `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config,secret_ciphertext,secret_aad) VALUES($1,'certification',$2,$1,true,$3,$4,$5)`, id, implementation, raw, sealed, "zjmfgo:channel-instance:v1:"+id)
}

func (f certificationRuntimeFixture) selectInstance(t *testing.T, id, implementation string) {
	t.Helper()
	if err := f.db.WithTx(t.Context(), func(tx pgx.Tx) error {
		return f.db.ValidateCertificationSelectionTx(t.Context(), tx, implementation, id)
	}); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE certification_settings SET default_provider_id=$1,default_instance_id=$2,version=version+1 WHERE id=1`, implementation, id)
}

func (f certificationRuntimeFixture) submit(t *testing.T) map[string]any {
	t.Helper()
	out, err := f.db.SubmitCertification(t.Context(), f.customer, "individual", map[string]any{"name": "认证夹具姓名", "id_number": "PRIVATE-ID-FIXTURE"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f certificationRuntimeFixture) process(t *testing.T) {
	t.Helper()
	if processed, err := f.db.ProcessCertificationJob(t.Context()); err != nil || !processed {
		t.Fatalf("process=%v err=%v", processed, err)
	}
}

func (f certificationRuntimeFixture) job(t *testing.T, submissionID int64) (string, int, int) {
	t.Helper()
	var state string
	var submits, queries int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT status,submit_attempts,query_attempts FROM certification_provider_jobs WHERE submission_id=$1`, submissionID).Scan(&state, &submits, &queries); err != nil {
		t.Fatal(err)
	}
	return state, submits, queries
}

func (f certificationRuntimeFixture) summary(t *testing.T) map[string]any {
	t.Helper()
	out, err := f.db.Certification(t.Context(), f.customer)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCertificationProviderInstancesAndFrozenCredentials(t *testing.T) {
	f := certificationProviderFixture(t)
	a := &certificationProbe{submitResult: certification.Result{Status: "processing", ProviderSubject: "account-a-subject"}, queryResult: certification.Result{Status: "approved", ProviderSubject: "account-a-subject", Message: "DO-NOT-LOG-IDENTITY"}}
	b := &certificationProbe{submitResult: certification.Result{Status: "approved", ProviderSubject: "account-b-subject"}}
	f.instance(t, "cert-account-a", "certification_fixture", "fixture-original-a", a)
	f.instance(t, "cert-account-b", "certification_fixture", "fixture-original-b", b)
	f.selectInstance(t, "cert-account-a", "certification_fixture")
	one := f.submit(t)
	f.exec(t, `UPDATE certification_settings SET auto_update_name=true`)
	f.process(t)
	if f.summary(t)["status"] != "pending" || f.summary(t)["processing_state"] != "processing" {
		t.Fatal("processing provider prematurely certified customer")
	}
	rotated, err := channelSeal(f.db.ProvisionEncryptionKey, "cert-account-a", "fixture-rotated-a", true)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE channel_instances SET secret_ciphertext=$1,version=version+1 WHERE id='cert-account-a'`, rotated)
	// A current default change cannot reroute an existing application's query.
	f.selectInstance(t, "cert-account-b", "certification_fixture")
	f.exec(t, `UPDATE certification_provider_jobs SET next_run_at=now()-interval '1 second' WHERE submission_id=$1`, one["id"])
	f.process(t)
	if f.summary(t)["status"] != "approved" {
		t.Fatal("frozen provider query did not approve")
	}
	var name string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT display_name FROM users WHERE id=$1`, f.customer).Scan(&name); err != nil || name != "认证夹具姓名" {
		t.Fatal("automatic approved name writeback failed", err)
	}
	a.mu.Lock()
	if a.submits != 1 || a.queries != 1 || len(a.tokens) != 2 || a.tokens[0] != "fixture-original-a" || a.tokens[1] != "fixture-original-a" || a.requests[0] != a.requests[1] {
		t.Error("application did not preserve its instance, credentials and request identity")
	}
	a.mu.Unlock()
	two := f.submit(t)
	f.process(t)
	b.mu.Lock()
	if b.submits != 1 || b.queries != 0 || len(b.tokens) != 1 || b.tokens[0] != "fixture-original-b" {
		t.Error("separate configured instance shared credentials")
	}
	b.mu.Unlock()
	state, submits, queries := f.job(t, two["id"].(int64))
	if state != "done" || submits != 1 || queries != 0 {
		t.Fatal("direct approval did not finish exactly once")
	}
	var leaked bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM audit_events WHERE payload::text LIKE '%DO-NOT-LOG-IDENTITY%' OR payload::text LIKE '%认证夹具姓名%' OR payload::text LIKE '%fixture-original%')`).Scan(&leaked); err != nil || leaked {
		t.Fatal("audit exposed provider or identity data", err)
	}
	var ciphertext []byte
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT config_snapshot_encrypted FROM certification_submissions WHERE id=$1`, one["id"]).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, []byte("fixture-original-a")) {
		t.Fatal("credential snapshot was not encrypted", err)
	}
	f.exec(t, `UPDATE certification_settings SET default_provider_id='manual',default_instance_id='manual-default'`)
	f.exec(t, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('certification-delete-session',$1,now()+interval '1 hour')`, f.actor)
	if err := f.db.DeleteChannelInstance(t.Context(), f.actor, "certification-delete-session", "cert-account-a", 2); err == nil || conflictCode(t, err) != "channel_in_use" {
		t.Fatal("historically referenced certification instance was deletable")
	}
}

func TestCertificationProviderUnknownSubmitQueriesOnly(t *testing.T) {
	f := certificationProviderFixture(t)
	p := &certificationProbe{submitErr: context.DeadlineExceeded, queryResult: certification.Result{Status: "approved", ProviderSubject: "recovered-by-request"}}
	f.instance(t, "cert-timeout", "certification_fixture", "fixture-timeout", p)
	f.selectInstance(t, "cert-timeout", "certification_fixture")
	out := f.submit(t)
	f.process(t)
	state, submits, queries := f.job(t, out["id"].(int64))
	if state != "uncertain" || submits != 1 || queries != 0 {
		t.Fatal("unknown submit was not durably separated from retry")
	}
	if processed, err := f.db.ProcessCertificationJob(t.Context()); err != nil || processed {
		t.Fatal("processing polled before the 30-second delay", err)
	}
	// In-flight verification continues against its snapshot after both switches
	// are disabled; it must not be replaced by a fresh external submit.
	f.exec(t, `UPDATE channel_instances SET enabled=false WHERE id='cert-timeout'`)
	f.exec(t, `UPDATE certification_settings SET enabled=false`)
	f.exec(t, `UPDATE certification_provider_jobs SET next_run_at=now()-interval '1 second' WHERE submission_id=$1`, out["id"])
	f.process(t)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.submits != 1 || p.queries != 1 || len(p.requests) != 2 || p.requests[0] == "" || p.requests[0] != p.requests[1] || f.summary(t)["status"] != "approved" {
		t.Fatal("unknown submit was resent or could not reconcile by stable request ID")
	}
}

func TestCertificationProviderLeaseAndManualFencing(t *testing.T) {
	for _, mode := range []string{"lease_expired", "new_application", "manual_decision"} {
		t.Run(mode, func(t *testing.T) {
			f := certificationProviderFixture(t)
			p := &certificationProbe{queryResult: certification.Result{Status: "approved", ProviderSubject: "recovered"}}
			f.instance(t, "cert-fence", "certification_fixture", "fixture-fence", p)
			f.selectInstance(t, "cert-fence", "certification_fixture")
			out := f.submit(t)
			job, processed, err := f.db.claimCertificationJob(t.Context())
			if err != nil || !processed || job == nil {
				t.Fatal("could not prepare abandoned submit", err)
			}
			want := "pending"
			switch mode {
			case "lease_expired":
				f.exec(t, `UPDATE certification_provider_jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, job.id)
				f.process(t)
				want = "approved"
			case "new_application":
				f.exec(t, `UPDATE certification_settings SET default_provider_id='manual',default_instance_id='manual-default'`)
				f.submit(t)
			case "manual_decision":
				if err := f.db.ReviewCertification(t.Context(), f.actor, out["profile_id"].(int64), "rejected", "人工拒绝", 1); err != nil {
					t.Fatal(err)
				}
				want = "rejected"
			}
			if err := f.db.finishCertificationJob(t.Context(), *job, certification.Result{Status: "approved", ProviderSubject: "late-result"}, nil); err != nil {
				t.Fatal(err)
			}
			if f.summary(t)["status"] != want {
				t.Fatal("late external result replaced the current decision")
			}
			var ignored int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM certification_provider_results WHERE submission_id=$1 AND NOT applied AND ignore_reason<>''`, out["id"]).Scan(&ignored); err != nil || ignored != 1 {
				t.Fatal("late result was not retained as ignored evidence", err)
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.submits != 0 || mode == "lease_expired" && p.queries != 1 {
				t.Fatal("lease recovery replayed an uncertain submission")
			}
		})
	}
}

func TestCertificationProviderDispatchGatesAndQueryLimits(t *testing.T) {
	for _, mode := range []string{"master_disabled", "instance_disabled", "query_limit", "no_query"} {
		t.Run(mode, func(t *testing.T) {
			f := certificationProviderFixture(t)
			p := &certificationProbe{submitResult: certification.Result{Status: "processing", ProviderSubject: "pending"}, queryResult: certification.Result{Status: "processing", ProviderSubject: "pending"}}
			implementation := "certification_fixture"
			if mode == "no_query" {
				implementation = "certification_no_query_fixture"
				p.submitErr = context.DeadlineExceeded
			}
			f.instance(t, "cert-gated", implementation, "fixture-gated", p)
			f.selectInstance(t, "cert-gated", implementation)
			out := f.submit(t)
			if mode == "master_disabled" {
				f.exec(t, `UPDATE certification_settings SET enabled=false`)
			} else if mode == "instance_disabled" {
				f.exec(t, `UPDATE channel_instances SET enabled=false WHERE id='cert-gated'`)
			}
			f.process(t)
			if mode == "query_limit" {
				for range 5 {
					f.exec(t, `UPDATE certification_provider_jobs SET next_run_at=now()-interval '1 second' WHERE submission_id=$1`, out["id"])
					f.process(t)
				}
			}
			state, submits, queries := f.job(t, out["id"].(int64))
			if state != "needs_review" || f.summary(t)["status"] != "pending" || f.summary(t)["processing_state"] != "needs_review" {
				t.Fatal("technical or dispatch failure was interpreted as a certification decision")
			}
			if mode == "master_disabled" || mode == "instance_disabled" {
				if submits != 0 || queries != 0 {
					t.Fatal("disabled queued application made an external request")
				}
			} else if submits != 1 || mode == "query_limit" && queries != 5 {
				t.Fatal("query count or submit maximum was not enforced")
			}
		})
	}
}

func TestCertificationProviderResultSubjectAndRollback(t *testing.T) {
	for _, mode := range []string{"subject_mismatch", "audit_rollback"} {
		t.Run(mode, func(t *testing.T) {
			f := certificationProviderFixture(t)
			p := &certificationProbe{submitResult: certification.Result{Status: "processing", ProviderSubject: "original"}, queryResult: certification.Result{Status: "approved", ProviderSubject: "different"}}
			if mode == "audit_rollback" {
				p.submitResult = certification.Result{Status: "approved", ProviderSubject: "original"}
				p.queryResult.ProviderSubject = "original"
				f.exec(t, `UPDATE certification_settings SET auto_update_name=true`)
				f.exec(t, `CREATE FUNCTION reject_provider_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='certification.provider_completed' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_provider_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_provider_audit();`)
			}
			f.instance(t, "cert-result", "certification_fixture", "fixture-result", p)
			f.selectInstance(t, "cert-result", "certification_fixture")
			out := f.submit(t)
			if mode == "audit_rollback" {
				if processed, err := f.db.ProcessCertificationJob(t.Context()); !processed || err == nil {
					t.Fatal("injected audit failure did not roll back provider decision")
				}
				var name string
				var results int
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT display_name,(SELECT count(*) FROM certification_provider_results) FROM users WHERE id=$1`, f.customer).Scan(&name, &results); err != nil || name != "Topup customer" || results != 0 || f.summary(t)["status"] != "pending" {
					t.Fatal("provider approval escaped transaction rollback", err)
				}
				f.exec(t, `DROP TRIGGER reject_provider_audit ON audit_events`)
				f.exec(t, `UPDATE certification_provider_jobs SET lease_until=now()-interval '1 second' WHERE submission_id=$1`, out["id"])
				f.process(t)
				if f.summary(t)["status"] != "approved" {
					t.Fatal("rolled-back approval was not recovered by query")
				}
			} else {
				f.process(t)
				f.exec(t, `UPDATE certification_provider_jobs SET next_run_at=now()-interval '1 second' WHERE submission_id=$1`, out["id"])
				f.process(t)
				state, _, _ := f.job(t, out["id"].(int64))
				if state != "needs_review" || f.summary(t)["status"] != "pending" {
					t.Fatal("mismatched provider subject certified another application")
				}
			}
		})
	}
}

func certificationLockObserver(t *testing.T, db *Store) *pgx.Conn {
	t.Helper()
	// An observer must not compete with the transactions whose locks it observes.
	// In the settings scenario those transactions deliberately fill a small pool.
	cfg := db.Pool.Config().ConnConfig.Copy()
	cfg.ConnectTimeout = time.Second
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	observer, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = observer.Close(ctx)
	})
	return observer
}

func waitCertificationQueryLock(t *testing.T, observer *pgx.Conn, fragment string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		queryCtx, cancelQuery := context.WithTimeout(ctx, time.Second)
		err := observer.QueryRow(queryCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND datname=current_database() AND wait_event_type='Lock' AND query LIKE '%'||$1||'%')`, fragment).Scan(&waiting)
		cancelQuery()
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("expected lock wait was not observed", fragment)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestCertificationProviderLeaseRecheckedAfterJobLockWait(t *testing.T) {
	for _, finish := range []bool{true, false} {
		t.Run(fmt.Sprint("result_", finish), func(t *testing.T) {
			f := certificationProviderFixture(t)
			f.instance(t, "cert-expired-wait", "certification_fixture", "fixture-expired", &certificationProbe{})
			f.selectInstance(t, "cert-expired-wait", "certification_fixture")
			out := f.submit(t)
			observer := certificationLockObserver(t, f.db)
			job, _, err := f.db.claimCertificationJob(t.Context())
			if err != nil || job == nil {
				t.Fatal("could not claim expiry fixture", err)
			}
			var expires time.Time
			if err := f.db.Pool.QueryRow(t.Context(), `UPDATE certification_provider_jobs SET lease_until=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING lease_until`, job.id).Scan(&expires); err != nil {
				t.Fatal(err)
			}
			blocker, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err := blocker.Exec(t.Context(), `SELECT id FROM certification_provider_jobs WHERE id=$1 FOR UPDATE`, job.id); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if finish {
					done <- f.db.finishCertificationJob(t.Context(), *job, certification.Result{Status: "approved"}, nil)
				} else {
					done <- f.db.certificationJobNeedsReview(t.Context(), *job, "provider_unavailable")
				}
			}()
			waitCertificationQueryLock(t, observer, "SELECT id FROM certification_provider_jobs WHERE id=$1 FOR UPDATE")
			if remaining := time.Until(expires) + 100*time.Millisecond; remaining > 0 {
				time.Sleep(remaining)
			}
			if err := blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("result did not finish after lease lock was released")
			}
			state, _, _ := f.job(t, out["id"].(int64))
			if state != "submitting" || f.summary(t)["status"] != "pending" {
				t.Fatal("expired waiting worker changed current state")
			}
			if finish {
				var reason string
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT ignore_reason FROM certification_provider_results WHERE submission_id=$1`, out["id"]).Scan(&reason); err != nil || reason != "lease_lost" {
					t.Fatal("expired result did not record lost lease", err)
				}
			}
		})
	}
}

func TestCertificationProviderSettingsClaimLockOrder(t *testing.T) {
	f := certificationProviderFixture(t)
	f.instance(t, "cert-lock-order", "certification_fixture", "fixture-lock-order", &certificationProbe{})
	f.selectInstance(t, "cert-lock-order", "certification_fixture")
	out := f.submit(t)
	// Keep this independent of host CPU count: blocker, claim, submission and
	// settings must occupy every business connection while observation continues.
	cfg := f.db.Pool.Config().Copy()
	cfg.MaxConns, cfg.MinConns = 4, 0
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	previous := f.db.Pool
	f.db.Pool = pool
	previous.Close()
	observer := certificationLockObserver(t, f.db)
	blocker, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(t.Context(), `SELECT id FROM certification_provider_jobs WHERE submission_id=$1 FOR UPDATE`, out["id"]); err != nil {
		t.Fatal(err)
	}
	claimDone, submitDone, settingsDone := make(chan error, 1), make(chan error, 1), make(chan error, 1)
	go func() {
		_, _, err := f.db.claimCertificationJob(t.Context())
		claimDone <- err
	}()
	waitCertificationQueryLock(t, observer, "WHERE j.id=$1 FOR UPDATE OF j")
	go func() {
		_, err := f.db.SubmitCertification(t.Context(), f.customer, "individual", map[string]any{"name": "并发申请", "id_number": "LOCK-ORDER-PRIVATE"})
		submitDone <- err
	}()
	waitCertificationQueryLock(t, observer, "INSERT INTO certification_profiles(user_id,certification_type,status,provider_id)")
	go func() {
		_, err := f.db.Pool.Exec(t.Context(), `UPDATE certification_settings SET version=version+1 WHERE id=1`)
		settingsDone <- err
	}()
	waitCertificationQueryLock(t, observer, "UPDATE certification_settings SET version=version+1 WHERE id=1")
	if acquired := f.db.Pool.Stat().AcquiredConns(); acquired != cfg.MaxConns {
		t.Fatalf("lock scenario did not fill the small business pool: %d/%d", acquired, cfg.MaxConns)
	}
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, done := range []chan error{claimDone, submitDone, settingsDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal("settings, claim and submission formed a lock cycle", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("settings, claim and submission did not drain")
		}
	}
}

func TestCertificationProviderNameContentionRollsBack(t *testing.T) {
	for _, manual := range []bool{true, false} {
		t.Run(fmt.Sprint("manual_", manual), func(t *testing.T) {
			f := certificationProviderFixture(t)
			p := &certificationProbe{submitResult: certification.Result{Status: "approved", ProviderSubject: "same-subject"}, queryResult: certification.Result{Status: "approved", ProviderSubject: "same-subject"}}
			if !manual {
				f.instance(t, "cert-name-busy", "certification_fixture", "fixture-name-busy", p)
				f.selectInstance(t, "cert-name-busy", "certification_fixture")
			}
			f.exec(t, `UPDATE certification_settings SET auto_update_name=true`)
			out := f.submit(t)
			admission, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer admission.Rollback(context.Background())
			if _, err := admission.Exec(t.Context(), `SELECT id FROM users WHERE id=$1 FOR SHARE`, f.customer); err != nil {
				t.Fatal(err)
			}
			if manual {
				err = f.db.ReviewCertification(t.Context(), f.actor, out["profile_id"].(int64), "approved", "", 1)
			} else {
				_, err = f.db.ProcessCertificationJob(t.Context())
			}
			if err == nil || conflictCode(t, err) != "certification_busy" {
				t.Fatal("name contention did not return a retryable certification conflict")
			}
			var profileStatus, submissionStatus, name string
			var results, reviews, completionAudits int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT p.status,sub.status,u.display_name,
 (SELECT count(*) FROM certification_provider_results),(SELECT count(*) FROM certification_reviews),
 (SELECT count(*) FROM audit_events WHERE action IN ('certification.provider_completed','certification.reviewed','certification.name_updated'))
 FROM certification_profiles p JOIN certification_submissions sub ON sub.profile_id=p.id JOIN users u ON u.id=p.user_id WHERE sub.id=$1`, out["id"]).Scan(&profileStatus, &submissionStatus, &name, &results, &reviews, &completionAudits); err != nil || profileStatus != "pending" || submissionStatus == "approved" || name != "Topup customer" || results != 0 || reviews != 0 || completionAudits != 0 {
				t.Fatal("name contention left a partially committed certification", err)
			}
			if err := admission.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if manual {
				if err := f.db.ReviewCertification(t.Context(), f.actor, out["profile_id"].(int64), "approved", "", 1); err != nil {
					t.Fatal("manual retry after customer contention failed", err)
				}
			} else {
				state, submits, _ := f.job(t, out["id"].(int64))
				if state != "submitting" || submits != 1 {
					t.Fatal("rollback cleared the durable external submission marker")
				}
				f.exec(t, `UPDATE certification_provider_jobs SET lease_until=now()-interval '1 second' WHERE submission_id=$1`, out["id"])
				f.process(t)
				p.mu.Lock()
				if p.submits != 1 || p.queries != 1 {
					t.Error("name conflict recovery resent an external submission")
				}
				p.mu.Unlock()
			}
			if f.summary(t)["status"] != "approved" {
				t.Fatal("safe retry did not finish certification")
			}
		})
	}
}

func certificationUpgradeSnapshot(t *testing.T, db *Store) string {
	t.Helper()
	var out string
	err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'settings',(SELECT to_jsonb(c)-'default_instance_id' FROM certification_settings c),
 'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM certification_profiles p),
 'submissions',(SELECT jsonb_agg(to_jsonb(s)-ARRAY['channel_instance_id','channel_instance_version','channel_schema_version','profile_version','request_id','config_snapshot_encrypted','config_snapshot_aad'] ORDER BY id) FROM certification_submissions s),
 'results',(SELECT jsonb_agg(to_jsonb(r)-ARRAY['applied','ignore_reason','job_lease_token'] ORDER BY id) FROM certification_provider_results r),
 'reviews',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM certification_reviews r),
 'instances',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM channel_instances c WHERE NOT(id='manual-default' AND category='certification' AND implementation_id='manual'))
 )::text`).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCertificationProviderUpgrade163PreservesManualApplications(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(fmt.Sprint("occupied_id_", occupied), func(t *testing.T) {
			db := notificationSchema160(t)
			for _, name := range []string{notificationUpgrade161, notificationUpgrade162, "163_notification_lifecycle.sql"} {
				applyNotificationUpgradeMigration(t, db, name)
			}
			db.ProvisionEncryptionKey = make([]byte, 32)
			actor, customer := topUpFixture(t, db)
			var profile, submission int64
			if err := db.Pool.QueryRow(t.Context(), `INSERT INTO certification_profiles(user_id,status,provider_id) VALUES($1,'approved','manual') RETURNING id`, customer).Scan(&profile); err != nil {
				t.Fatal(err)
			}
			sealed, aad, err := sealCertification(db.ProvisionEncryptionKey, fmt.Sprint(profile), map[string]any{"name": "旧人工资料", "id_number": "LEGACY-PRIVATE"})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Pool.QueryRow(t.Context(), `INSERT INTO certification_submissions(profile_id,user_id,certification_type,provider_id,status,payload_encrypted,payload_aad,reviewed_at,reviewed_by,review_note) VALUES($1,$2,'individual','manual','approved',$3,$4,now(),$5,'旧人工审核') RETURNING id`, profile, customer, sealed, aad, actor).Scan(&submission); err != nil {
				t.Fatal(err)
			}
			for _, sql := range []string{
				`INSERT INTO certification_reviews(submission_id,profile_id,reviewer_id,decision,note) VALUES($1,$2,$3,'approved','旧审核保留')`,
				`INSERT INTO certification_provider_results(submission_id,provider_id,status,result_encrypted,result_aad) VALUES($1,'manual','processing','\x010203','old-aad')`,
			} {
				args := []any{submission, profile, actor}
				if strings.Contains(sql, "provider_results") {
					args = args[:1]
				}
				if _, err := db.Pool.Exec(t.Context(), sql, args...); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Pool.Exec(t.Context(), `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled) VALUES('manual-existing','certification','manual','人工实名认证',true)`); err != nil {
				t.Fatal(err)
			}
			if occupied {
				if _, err := db.Pool.Exec(t.Context(), `INSERT INTO channel_instances(id,category,implementation_id,display_name) VALUES('manual-default','mail','smtp','Existing unrelated channel')`); err != nil {
					t.Fatal(err)
				}
			}
			before := certificationUpgradeSnapshot(t, db)
			err = db.Migrate(t.Context())
			if occupied {
				if err == nil || certificationUpgradeSnapshot(t, db) != before {
					t.Fatal("occupied legacy instance ID was overwritten or migration partially committed")
				}
				return
			}
			if err != nil || certificationUpgradeSnapshot(t, db) != before {
				t.Fatal("schema 163 certification history changed during upgrade", err)
			}
			var jobs int
			var instance string
			if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM certification_provider_jobs),default_instance_id FROM certification_settings WHERE id=1`).Scan(&jobs, &instance); err != nil || jobs != 0 || instance != "manual-default" {
				t.Fatal("upgrade backfilled external work or changed manual routing", err)
			}
			if err := db.Migrate(t.Context()); err != nil || certificationUpgradeSnapshot(t, db) != before {
				t.Fatal("migration replay changed old certification facts", err)
			}
			if _, err := db.SubmitCertification(t.Context(), customer, "individual", map[string]any{"name": "新人工申请", "id_number": "NEW-PRIVATE"}); err != nil {
				t.Fatal("manual submission did not remain compatible", err)
			}
		})
	}
}
