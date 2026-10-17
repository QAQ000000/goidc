package httpapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/g09fixture"
)

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
