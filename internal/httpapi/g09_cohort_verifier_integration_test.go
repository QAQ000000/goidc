package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/g09fixture"
	"github.com/codex/zjmfgo/internal/store"
)

// Report a stable source-query hash and numeric entity IDs, never raw SQL,
// driver errors, addresses or the string arguments containing rendered content.
func g09CohortAssertSQL(t *testing.T, db *store.Store, query string, args ...any) {
	t.Helper()
	var valid bool
	err := db.Pool.QueryRow(t.Context(), query, args...).Scan(&valid)
	if err == nil && valid {
		return
	}
	ids := map[int]any{}
	for i, arg := range args {
		switch arg.(type) {
		case int, int64:
			ids[i+1] = arg
		}
	}
	t.Fatalf("G09 cohort SQL invariant failed query_sha256=%s numeric_args=%v unavailable=%t", g09SourceHash(query), ids, err != nil)
}

func (e *g09CohortEnv) logFailureState(t *testing.T, workers []*g09SourceWorkerChild) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var raw []byte
	err := e.db.Pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'provision',(SELECT jsonb_agg(x) FROM (SELECT action,status,attempts,count(*) FROM provision_jobs GROUP BY action,status,attempts ORDER BY action,status,attempts) x),
 'email',(SELECT jsonb_agg(x) FROM (SELECT status,attempts,count(*) FROM email_outbox GROUP BY status,attempts ORDER BY status,attempts) x),
 'sms',(SELECT jsonb_agg(x) FROM (SELECT status,attempts,count(*) FROM sms_outbox GROUP BY status,attempts ORDER BY status,attempts) x),
 'orders',(SELECT jsonb_agg(x) FROM (SELECT status,count(*) FROM orders GROUP BY status ORDER BY status) x))`).Scan(&raw)
	state := map[string]any{"snapshotAvailable": err == nil, "candidateVerified": false, "capacityVerified": false, "verificationSamples": e.verificationSamples}
	if err == nil {
		state["queues"] = json.RawMessage(raw)
	}
	pids := make([]int, 0, len(workers))
	clean := true
	for _, w := range workers {
		pids = append(pids, w.cmd.Process.Pid)
		clean = clean && w.stopped && w.cleanTERM
	}
	state["workerPIDs"], state["workersExitedViaTERM"] = pids, clean
	state["workerLogs"] = g09CohortWorkerLogEvidence(workers)
	if public, err := json.Marshal(state); err == nil {
		t.Logf("G09_COHORT_FAILURE %s", public)
	}
}

var g09CohortSQLState = regexp.MustCompile(`SQLSTATE ([A-Z0-9]{5})`)

func g09CohortWorkerLogEvidence(workers []*g09SourceWorkerChild) map[string]any {
	var entries []map[string]any
	for _, w := range workers {
		log, err := os.ReadFile(w.logPath)
		if err != nil || len(log) > 1<<20 {
			return map[string]any{"available": false, "reason": "private_log_unavailable_or_budget"}
		}
		for _, line := range bytes.Split(log, []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var entry struct{ Level, Msg, Error string }
			if json.Unmarshal(line, &entry) != nil {
				return map[string]any{"available": false, "reason": "private_log_invalid"}
			}
			if entry.Level != "ERROR" {
				continue
			}
			code := "unknown"
			if match := g09CohortSQLState.FindStringSubmatch(entry.Error); len(match) == 2 {
				code = match[1]
			}
			entries = append(entries, map[string]any{"pid": w.cmd.Process.Pid, "messageSHA256": g09SourceHash(entry.Msg), "errorSHA256": g09SourceHash(entry.Error), "sqlstate": code})
			if len(entries) > 100 {
				return map[string]any{"available": false, "reason": "private_log_error_budget"}
			}
		}
	}
	return map[string]any{"available": true, "errorCount": len(entries), "errors": entries}
}

// These observations describe only the sealed source-Handler cohort. They do
// not turn an in-process API, loopback channels or race timings into capacity.
type g09CohortVerificationEvidence struct {
	Phase          string
	Sample         g09fixture.CohortVerification
	Queues         g09fixture.PGObservation
	ElapsedSeconds float64
}

func (e *g09CohortEnv) verifierInput(t *testing.T, records []g09CohortSource) (g09fixture.PGObserverConfig, g09fixture.Cohort, g09fixture.CohortBaseline) {
	t.Helper()
	sources := make([]g09fixture.CohortSourceRecord, len(records))
	for i, r := range records {
		sources[i] = g09fixture.CohortSourceRecord(r)
	}
	cohort, err := g09fixture.SealCohortSources(sources)
	if err != nil {
		t.Fatal("independent source seal failed")
	}
	raw, err := os.ReadFile(filepath.Join(e.private, "baseline.private.json"))
	var baseline g09fixture.CohortBaseline
	if err != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &baseline) != nil {
		t.Fatal("independent opening baseline unavailable")
	}
	accounts := make([]int64, len(e.suppliers))
	for i, s := range e.suppliers {
		accounts[i] = s.accountID
	}
	cfg := g09fixture.PGObserverConfig{Database: e.db.Pool.Config().ConnConfig.Database, Schema: e.schema, SupplierAccountIDs: accounts, MaxSamples: 400, Timeout: 5 * time.Second}
	return cfg, cohort, baseline
}

func (e *g09CohortEnv) initVerifier(t *testing.T, records []g09CohortSource) {
	t.Helper()
	cfg, cohort, baseline := e.verifierInput(t, records)
	var err error
	e.verifier, err = g09fixture.NewCohortVerifier(e.db.Pool, cfg, cohort, baseline)
	if err != nil {
		t.Fatal("independent cohort verifier refused setup")
	}
	e.observer, err = g09fixture.NewPGObserver(e.db.Pool, cfg)
	if err != nil {
		t.Fatal("queue observer refused setup")
	}
	complete := false
	e.observeVerifier(t, "sealed_pending", &complete, true)
}

func (e *g09CohortEnv) observeVerifier(t *testing.T, phase string, expected *bool, logTasks bool) {
	t.Helper()
	started := time.Now()
	sample, err := e.verifier.Sample(t.Context())
	if err != nil {
		t.Fatalf("independent cohort verification failed at %s: %s", phase, sample.FailureStage)
	}
	if sample.Complete == nil || sample.InvariantsVerified == nil || !*sample.InvariantsVerified || len(sample.Tasks) != 600 || (expected != nil && *sample.Complete != *expected) {
		t.Fatalf("independent cohort verification denominator/status failed at %s", phase)
	}
	if phase == "sealed_pending" {
		for _, task := range sample.Tasks {
			if task.Status != "pending" || task.Attempts != 0 {
				t.Fatal("independent verifier found a task claimed before seal")
			}
		}
	}
	queues, err := e.observer.Sample(t.Context())
	if err != nil {
		t.Fatal("independent queue observation failed")
	}
	// The public evidence must not publish the original mutation/idempotency key.
	// Task IDs remain individually auditable; only the flow label is hashed.
	for i := range sample.Tasks {
		sample.Tasks[i].FlowID = g09SourceHash(sample.Tasks[i].FlowID)
	}
	evidence := g09CohortVerificationEvidence{Phase: phase, Sample: sample, Queues: queues, ElapsedSeconds: time.Since(started).Seconds()}
	if logTasks {
		raw, err := json.Marshal(evidence)
		if err != nil {
			t.Fatal("public verifier evidence encoding failed")
		}
		t.Logf("G09_COHORT_VERIFICATION %s", raw)
	}
	// Full per-task observations are logged at the three evidence boundaries;
	// intermediate queue samples retain availability/null semantics and status.
	evidence.Sample.Tasks = nil
	e.verificationSamples = append(e.verificationSamples, evidence)
	baseline := e.verificationSamples[0].Queues.Database
	if queues.Database.Deadlocks == nil || baseline.Deadlocks == nil || *queues.Database.Deadlocks != *baseline.Deadlocks || !sameG09OptionalTime(queues.Database.StatsReset, baseline.StatsReset) {
		t.Fatal("cohort database deadlock counter changed or became unavailable/reset")
	}
}

func sameG09OptionalTime(a, b *time.Time) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && a.Equal(*b))
}

func (e *g09CohortEnv) verifyWrongOwnerRejected(t *testing.T, records []g09CohortSource) {
	t.Helper()
	// This deliberately wrong manifest changes no database facts. A previously
	// valid total cannot compensate for binding a paid invoice to another owner.
	wrong := append([]g09CohortSource(nil), records...)
	if len(wrong) < 2 || wrong[0].CustomerID == wrong[1].CustomerID {
		t.Fatal("wrong-owner negative fixture requires distinct customers")
	}
	wrong[0].CustomerID = records[1].CustomerID
	cfg, cohort, baseline := e.verifierInput(t, wrong)
	verifier, err := g09fixture.NewCohortVerifier(e.db.Pool, cfg, cohort, baseline)
	if err != nil {
		t.Fatal("wrong-owner verifier fixture failed before SQL")
	}
	sample, err := verifier.Sample(t.Context())
	if err == nil || sample.Status != "FAIL" || sample.FailureStage != "purchase" || sample.Complete != nil || sample.Tasks != nil || sample.InvariantsVerified != nil {
		t.Fatal("independent SQL verifier accepted a cross-customer purchase or fabricated data")
	}
	if second, err := verifier.Sample(t.Context()); err == nil || second.Status != "FAIL" || second.Complete != nil || second.Tasks != nil {
		t.Fatal("independent SQL verifier failed to latch rejection")
	}
	t.Log("PASS: independent read-only SQL verifier rejected wrong-owner manifest and latched FAIL; database unchanged")
}
