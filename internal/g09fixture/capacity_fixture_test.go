package g09fixture

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func requireCapacityOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("ZJMFGO_G09_CAPACITY_FIXTURE_PROTOCOL") != "1" {
		t.Skip("explicit capacity fixture protocol opt-in required")
	}
}

func capacityProjection(c Cohort, completed bool) []CapacityTaskProjection {
	observed := time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)
	finished := observed.Add(-time.Second)
	maxAttempts := 5
	var out []CapacityTaskProjection
	for _, source := range c.sources {
		if source.Kind == "ticket" {
			expires := source.AcceptedAtUTC.Add(7 * 24 * time.Hour)
			for _, item := range []struct {
				kind string
				id   int64
			}{{"email", source.EmailOutboxID}, {"sms", source.SMSOutboxID}} {
				status, attempts := "sent", 1
				var end *time.Time = &finished
				if !completed && item.kind == "email" && source.TicketID == 1 {
					status, attempts, end = "pending", 0, nil
				}
				out = append(out, CapacityTaskProjection{FlowID: source.FlowID, Kind: item.kind, ID: item.id, Status: status, Attempts: attempts, MaxAttempts: &maxAttempts, CreatedAt: source.AcceptedAtUTC, AvailableAt: source.AcceptedAtUTC, FinishedAt: end, ExpiresAt: &expires})
			}
			continue
		}
		status, attempts := "succeeded", 1
		var end *time.Time = &finished
		if !completed && source.ProvisionJobID == 1 {
			status, attempts, end = "pending", 0, nil
		}
		out = append(out, CapacityTaskProjection{FlowID: source.FlowID, Kind: "provision", ID: source.ProvisionJobID, Status: status, Attempts: attempts, CreatedAt: source.AcceptedAtUTC, AvailableAt: source.AcceptedAtUTC, FinishedAt: end})
	}
	return out
}

func TestCapacityFixtureProtocolDrainAndCleanup(t *testing.T) {
	requireCapacityOptIn(t)
	cohort, _ := pureCohort(t)
	f, err := NewCapacityFixture("capacity-run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.RegisterSources(cohort.Sources()[:200]); err != nil {
		t.Fatal(err)
	}
	duplicate := cohort.Sources()[:1]
	duplicate[0].FlowID = "other-flow"
	if err = f.RegisterSources(duplicate); err == nil {
		t.Fatal("duplicate entity registration accepted")
	}
	if err = f.RegisterSources(cohort.Sources()[200:]); err != nil {
		t.Fatal(err)
	}
	if len(f.RegisteredSources()) != 450 {
		t.Fatal("registration not detached/full")
	}
	if err = f.SealSources(); err != nil {
		t.Fatal(err)
	}
	if err = f.RegisterSources(cohort.Sources()[:1]); err == nil {
		t.Fatal("sealed registration accepted")
	}
	if err = f.BeginDrain(); err != nil {
		t.Fatal(err)
	}

	observed := time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)
	pending, err := f.VerifyDrain(observed, capacityProjection(cohort, false))
	if err != nil || pending.Status != "PENDING" || pending.Complete || f.State() != CapacityDraining {
		t.Fatalf("pending projection fabricated completion: %+v %v", pending, err)
	}
	passed, err := f.VerifyDrain(observed.Add(time.Second), capacityProjection(cohort, true))
	if err != nil || passed.Status != "PASS" || !passed.Complete || f.State() != CapacityVerified {
		t.Fatalf("terminal projection did not pass: %+v %v", passed, err)
	}
	passed.Tasks[0].FlowID = "mutated"
	if f.verification.Tasks[0].FlowID == "mutated" {
		t.Fatal("verification retained caller-owned memory")
	}
	if err = f.Cleanup(); err != nil || f.State() != CapacityCleaned {
		t.Fatalf("cleanup gate: %v", err)
	}
	if err = f.Cleanup(); err == nil {
		t.Fatal("cleanup repeated")
	}
}

func TestCapacityFixtureRejectsIncompleteAndForgedProjection(t *testing.T) {
	requireCapacityOptIn(t)
	cohort, _ := pureCohort(t)
	newDraining := func(t *testing.T) *CapacityFixture {
		t.Helper()
		f, err := NewCapacityFixture("capacity-run-2")
		if err != nil {
			t.Fatal(err)
		}
		if err = f.RegisterSources(cohort.Sources()); err != nil {
			t.Fatal(err)
		}
		if err = f.SealSources(); err != nil {
			t.Fatal(err)
		}
		if err = f.BeginDrain(); err != nil {
			t.Fatal(err)
		}
		return f
	}
	observed := time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)
	t.Run("missing", func(t *testing.T) {
		f := newDraining(t)
		if _, err := f.VerifyDrain(observed, capacityProjection(cohort, true)[:599]); err == nil || f.State() != CapacityFailed {
			t.Fatal("missing task did not fail closed")
		}
		if err := f.Cleanup(); err != nil {
			t.Fatal("failed fixture cleanup gate", err)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		f := newDraining(t)
		rows := capacityProjection(cohort, true)
		rows[1] = rows[0]
		if _, err := f.VerifyDrain(observed, rows); err == nil || f.State() != CapacityFailed {
			t.Fatal("duplicate entity accepted")
		}
	})
	t.Run("wrong-flow", func(t *testing.T) {
		f := newDraining(t)
		rows := capacityProjection(cohort, true)
		rows[0].FlowID = "other-flow"
		if _, err := f.VerifyDrain(observed, rows); err == nil || f.State() != CapacityFailed {
			t.Fatal("wrong source mapping accepted")
		}
	})
	t.Run("outside-observation", func(t *testing.T) {
		f := newDraining(t)
		rows := capacityProjection(cohort, true)
		rows[0].CreatedAt = observed.Add(time.Second)
		if _, err := f.VerifyDrain(observed, rows); err == nil || f.State() != CapacityFailed {
			t.Fatal("future entity accepted")
		}
	})
}

func TestCapacityRequestContractAndSequenceConsumption(t *testing.T) {
	requireCapacityOptIn(t)
	cohort, _ := pureCohort(t)
	raw, err := json.Marshal(CapacityRequest{RunID: "capacity-run-3", Sequence: 1, Action: "register-sources", Sources: cohort.Sources()[:10]})
	if err != nil {
		t.Fatal(err)
	}
	req, err := ValidateCapacityRequest(raw, "capacity-run-3", 0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewCapacityFixture("capacity-run-3")
	if err != nil {
		t.Fatal(err)
	}
	response, err := f.Apply(req)
	if err != nil || !response.OK || response.Sequence != 1 || response.Registered != 10 {
		t.Fatalf("apply: %+v %v", response, err)
	}
	if _, err = f.Apply(req); err == nil {
		t.Fatal("request replay accepted")
	}
	for _, raw := range []string{
		`{"runId":"capacity-run-3","sequence":1,"action":"abort","sql":"DROP SCHEMA x"}`,
		`{"runId":"capacity-run-3","sequence":1,"action":"abort","action":"cleanup"}`,
		`{"runId":"capacity-run-3","sequence":1,"action":"abort"} {}`,
		`{"runId":"capacity-run-3","sequence":1,"action":"verify-drain","tasks":[]}`,
	} {
		if _, err := ValidateCapacityRequest([]byte(raw), "capacity-run-3", 0); err == nil {
			t.Fatalf("unsafe request accepted: %s", raw)
		}
	}
	if strings.Contains(response.Error, "SQL") {
		t.Fatal("response exposed arbitrary execution text")
	}
}

func TestCapacityFixtureSealAndCleanupRequireRecordedOutcome(t *testing.T) {
	requireCapacityOptIn(t)
	f, err := NewCapacityFixture("capacity-run-4")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Cleanup(); err == nil {
		t.Fatal("unverified cleanup accepted")
	}
	cohort, _ := pureCohort(t)
	if err = f.RegisterSources(cohort.Sources()[:1]); err != nil {
		t.Fatal(err)
	}
	if err = f.SealSources(); err == nil || f.State() != CapacityFailed {
		t.Fatal("invalid source set was not latched")
	}
}
