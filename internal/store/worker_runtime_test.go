package store

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"
)

func TestWorkerRuntimeAssessmentStartupStallAndHealthyPeer(t *testing.T) {
	now := time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC)
	for _, scenario := range []string{"startup", "stale", "stalled", "peer", "stopped", "backlog"} {
		t.Run(scenario, func(t *testing.T) {
			report := WorkerRuntimeReport{ObservedAt: now, StartupGraceSeconds: 120, BacklogWarningSeconds: 300}
			for _, d := range WorkerLaneDefinitions() {
				poll := now.Add(-time.Second)
				report.Lanes = append(report.Lanes, WorkerRuntimeLaneStatus{InstanceID: "a", Name: d.Name, State: "idle", InstanceStartedAt: now.Add(-time.Minute), LastPollAt: &poll, ProgressAt: poll, PollIntervalSeconds: d.PollIntervalSeconds, StallAfterSeconds: d.StallAfterSeconds})
			}
			first := &report.Lanes[0]
			switch scenario {
			case "startup":
				first.LastPollAt = nil
			case "stale":
				first.LastPollAt = nil
				first.InstanceStartedAt = now.Add(-3 * time.Minute)
			case "stalled", "peer":
				first.State = "running"
				first.ProgressAt = now.Add(-10 * time.Minute)
				if scenario == "peer" {
					peer := *first
					peer.InstanceID = "b"
					peer.ProgressAt = now
					report.Lanes = append(report.Lanes, peer)
				}
			case "stopped":
				first.InstanceStoppedAt = &now
			case "backlog":
				old := now.Add(-301 * time.Second)
				report.Queues = []WorkerQueueStatus{{Name: "email", RunnablePending: 1, OldestRunnableAt: &old}}
			}
			assessWorkerRuntime(&report)
			want := "degraded"
			if scenario == "startup" || scenario == "peer" {
				want = "ok"
			}
			if report.Status != want {
				t.Fatalf("status=%s issues=%v lanes=%+v", report.Status, report.Issues, report.Lanes)
			}
			if scenario == "stalled" && report.Lanes[0].Status != "stalled" {
				t.Fatal("running flag hid stalled execution")
			}
			if scenario == "peer" && report.Lanes[0].Status != "stalled" {
				t.Fatal("healthy peer erased stale evidence")
			}
		})
	}
}

func TestWorkerRuntimeDiagnosticsReadOnlyQueueWindowAndRetention(t *testing.T) {
	db := isolatedTestStore(t)
	ctx := t.Context()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	runtime, err := db.StartWorkerRuntime(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	report, err := db.WorkerRuntimeDiagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || len(report.Lanes) != 7 || len(report.Queues) != 7 {
		t.Fatalf("startup: %+v", report)
	}
	lane := runtime.Lane("provision")
	lane.Observe(true)
	if _, err = db.Pool.Exec(ctx, `UPDATE worker_runtime_lanes SET progress_at=now()-interval '10 minutes' WHERE instance_id=$1 AND lane='provision'`, runtime.ID); err != nil {
		t.Fatal(err)
	}
	report, err = db.WorkerRuntimeDiagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "degraded" || !slices.Contains(report.MissingLanes, "provision") {
		t.Fatalf("stuck loop hidden: %+v", report)
	}
	WorkerRuntimeProgress(lane.Context(ctx))
	lane.Observe(false)
	// Long-lived healthy processes must remain visible after retention duration.
	if _, err = db.Pool.Exec(ctx, `UPDATE worker_runtime_instances SET started_at=now()-interval '8 days' WHERE id=$1`, runtime.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO worker_runtime_instances(id,started_at,stopped_at) VALUES('11111111111111111111111111111111',now()-interval '9 days',now()-interval '8 days')`); err != nil {
		t.Fatal(err)
	}
	peer, err := db.StartWorkerRuntime(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	var retained, pruned bool
	if err = db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker_runtime_instances WHERE id=$1),NOT EXISTS(SELECT 1 FROM worker_runtime_instances WHERE id='11111111111111111111111111111111')`, runtime.ID).Scan(&retained, &pruned); err != nil || !retained || !pruned {
		t.Fatalf("retention: retained=%v pruned=%v err=%v", retained, pruned, err)
	}
	_, err = db.Pool.Exec(ctx, `INSERT INTO email_outbox(recipient,subject,body,kind,status,available_at,attempts,locked_at,dead_letter_at) VALUES
 ('test@example.test','subject','body','system','pending',now()-interval '10 minutes',0,NULL,NULL),
 ('test@example.test','subject','body','system','pending',now()+interval '1 hour',0,NULL,NULL),
 ('test@example.test','subject','body','system','sending',now(),1,now()-interval '6 minutes',NULL),
 ('test@example.test','subject','body','system','dead_letter',now(),5,NULL,now()-interval '2 days'),
 ('test@example.test','subject','body','system','dead_letter',now(),5,NULL,now())`)
	if err != nil {
		t.Fatal(err)
	}
	report, err = db.WorkerRuntimeDiagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var email WorkerQueueStatus
	for _, q := range report.Queues {
		if q.Name == "email" {
			email = q
		}
	}
	if email.RunnablePending != 1 || email.FuturePending != 1 || email.Running != 1 || email.ExpiredLeases != 1 || email.RecentDeadLetters != 1 || email.OldestWaitingSeconds < 599 {
		t.Fatalf("queue: %+v", email)
	}
	var unchanged bool
	if err = db.Pool.QueryRow(ctx, `SELECT count(*)=5 AND count(*) FILTER(WHERE status='sending' AND locked_at<now()-interval '5 minutes')=1 FROM email_outbox`).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("diagnostics changed business queue: unchanged=%v err=%v", unchanged, err)
	}
	// A healthy peer covers one stopped process, while its stopped rows remain visible.
	runtime.Stop()
	report, err = db.WorkerRuntimeDiagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.MissingLanes) != 0 {
		t.Fatalf("stopped peer masked healthy process: %+v", report.MissingLanes)
	}
	peer.Stop()
	report, err = db.WorkerRuntimeDiagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.MissingLanes) != 7 {
		t.Fatalf("no live process reported healthy: %+v", report)
	}
}

func TestWorkerRuntimeHistoricalDeadLettersDoNotDegrade(t *testing.T) {
	db := isolatedTestStore(t)
	ctx := context.Background()
	runtime, err := db.StartWorkerRuntime(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop()
	_, err = db.Pool.Exec(ctx, `INSERT INTO email_outbox(recipient,subject,body,kind,status,attempts,dead_letter_at) VALUES('test@example.test','subject','body','system','dead_letter',5,now()-interval '2 days')`)
	if err != nil {
		t.Fatal(err)
	}
	report, err := db.WorkerRuntimeDiagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" {
		t.Fatalf("historical dead letters permanently degraded diagnostics: %+v", report)
	}
}
