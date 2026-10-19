package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

type WorkerLaneDefinition struct {
	Name                string `json:"name"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	StallAfterSeconds   int    `json:"stall_after_seconds"`
}

// Stall limits describe missing execution progress, not a business deadline.
// Long bounded batches report progress between real units of work. They never
// have a separate goroutine that can mask a stuck execution loop.
func WorkerLaneDefinitions() []WorkerLaneDefinition {
	return []WorkerLaneDefinition{
		{"provision", 2, 300}, {"payment", 10, 120}, {"file_cleanup", 60, 180},
		{"notification_schedule", 60, 600}, {"auto_renew", 60, 300},
		{"notification_center", 2, 300}, {"certification", 2, 300},
	}
}

type WorkerRuntime struct {
	db  *Store
	ID  string
	log *slog.Logger
}
type WorkerRuntimeLane struct {
	runtime *WorkerRuntime
	name    string
}
type workerRuntimeContextKey struct{}

// StartWorkerRuntime registers every real loop once. A telemetry failure is
// reported to the caller, which can continue ordinary business execution.
func (s *Store) StartWorkerRuntime(ctx context.Context, log *slog.Logger) (*WorkerRuntime, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	runtime := &WorkerRuntime{db: s, ID: hex.EncodeToString(raw), log: log}
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		// Bounded retention also removes abandoned instances after a crash. Recent
		// stale peers remain visible and never erase another healthy worker's state.
		if _, err := tx.Exec(ctx, `DELETE FROM worker_runtime_instances i WHERE coalesce(i.stopped_at,(SELECT max(l.progress_at) FROM worker_runtime_lanes l WHERE l.instance_id=i.id),i.started_at)<clock_timestamp()-interval '7 days'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO worker_runtime_instances(id) VALUES($1)`, runtime.ID); err != nil {
			return err
		}
		for _, lane := range WorkerLaneDefinitions() {
			if _, err := tx.Exec(ctx, `INSERT INTO worker_runtime_lanes(instance_id,lane,poll_interval_seconds,stall_after_seconds) VALUES($1,$2,$3,$4)`, runtime.ID, lane.Name, lane.PollIntervalSeconds, lane.StallAfterSeconds); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return runtime, nil
}

func (r *WorkerRuntime) Lane(name string) *WorkerRuntimeLane {
	if r == nil {
		return nil
	}
	return &WorkerRuntimeLane{runtime: r, name: name}
}

// Stop is called only after all execution goroutines have returned. Cancellation
// by itself does not make a still-blocked lane look stopped.
func (r *WorkerRuntime) Stop() {
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := r.db.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE worker_runtime_instances SET stopped_at=clock_timestamp() WHERE id=$1`, r.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE worker_runtime_lanes SET state='stopped',progress_at=clock_timestamp() WHERE instance_id=$1`, r.ID)
		return err
	})
	if err != nil {
		r.log.Error("worker runtime persistence failed", "instance_id", r.ID, "phase", "stop")
	}
}

func (l *WorkerRuntimeLane) Context(ctx context.Context) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, workerRuntimeContextKey{}, l)
}

func (l *WorkerRuntimeLane) Observe(running bool) {
	if l == nil {
		return
	}
	query := `UPDATE worker_runtime_lanes SET state='running',last_poll_at=clock_timestamp(),started_at=clock_timestamp(),progress_at=clock_timestamp() WHERE instance_id=$1 AND lane=$2 AND state<>'stopped'`
	if !running {
		query = `UPDATE worker_runtime_lanes SET state='idle',completed_at=clock_timestamp(),progress_at=clock_timestamp(),completed_passes=completed_passes+1 WHERE instance_id=$1 AND lane=$2 AND state='running'`
	}
	l.write(query)
}

func (l *WorkerRuntimeLane) write(query string) {
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tag, err := l.runtime.db.Pool.Exec(ctx, query, l.runtime.ID, l.name)
	if err != nil || tag.RowsAffected() != 1 {
		l.runtime.log.Error("worker runtime persistence failed", "instance_id", l.runtime.ID, "lane", l.name)
	}
}

// WorkerRuntimeProgress records an execution boundary inside a long pass.
// Call it from the execution goroutine only; it does not spawn a heartbeat.
func WorkerRuntimeProgress(ctx context.Context) {
	if l, _ := ctx.Value(workerRuntimeContextKey{}).(*WorkerRuntimeLane); l != nil {
		l.write(`UPDATE worker_runtime_lanes SET progress_at=clock_timestamp() WHERE instance_id=$1 AND lane=$2 AND state='running'`)
	}
}

type WorkerRuntimeLaneStatus struct {
	InstanceID          string     `json:"instance_id"`
	Name                string     `json:"name"`
	State               string     `json:"state"`
	Status              string     `json:"status"`
	InstanceStartedAt   time.Time  `json:"instance_started_at"`
	InstanceStoppedAt   *time.Time `json:"instance_stopped_at,omitempty"`
	LastPollAt          *time.Time `json:"last_poll_at,omitempty"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	ProgressAt          time.Time  `json:"progress_at"`
	CompletedPasses     int64      `json:"completed_passes"`
	PollIntervalSeconds int        `json:"poll_interval_seconds"`
	StallAfterSeconds   int        `json:"stall_after_seconds"`
}

type WorkerQueueStatus struct {
	Name                 string     `json:"name"`
	RunnablePending      int64      `json:"runnable_pending"`
	FuturePending        int64      `json:"future_pending"`
	OldestRunnableAt     *time.Time `json:"oldest_runnable_at,omitempty"`
	OldestWaitingSeconds int64      `json:"oldest_waiting_seconds"`
	Running              int64      `json:"running"`
	ExpiredLeases        int64      `json:"expired_leases"`
	RecentFailures       int64      `json:"recent_failures"`
	RecentUnknown        int64      `json:"recent_unknown"`
	RecentDeadLetters    int64      `json:"recent_dead_letters"`
}

type WorkerRuntimeReport struct {
	Status                string                    `json:"status"`
	ObservedAt            time.Time                 `json:"observed_at"`
	RecentWindowSeconds   int                       `json:"recent_window_seconds"`
	StartupGraceSeconds   int                       `json:"startup_grace_seconds"`
	BacklogWarningSeconds int                       `json:"backlog_warning_seconds"`
	RetentionDays         int                       `json:"retention_days"`
	Issues                []string                  `json:"issues"`
	MissingLanes          []string                  `json:"missing_lanes"`
	Lanes                 []WorkerRuntimeLaneStatus `json:"lanes"`
	Queues                []WorkerQueueStatus       `json:"queues"`
}

func assessWorkerRuntime(report *WorkerRuntimeReport) {
	report.Status = "ok"
	report.Issues = []string{}
	report.MissingLanes = []string{}
	available := map[string]bool{}
	for i := range report.Lanes {
		lane := &report.Lanes[i]
		switch {
		case lane.InstanceStoppedAt != nil || lane.State == "stopped":
			lane.Status = "stopped"
		case lane.LastPollAt == nil && report.ObservedAt.Sub(lane.InstanceStartedAt) <= time.Duration(report.StartupGraceSeconds)*time.Second:
			lane.Status = "starting"
			available[lane.Name] = true
		case lane.LastPollAt == nil:
			lane.Status = "stale"
		case lane.State == "running" && report.ObservedAt.Sub(lane.ProgressAt) > time.Duration(lane.StallAfterSeconds)*time.Second:
			lane.Status = "stalled"
		case lane.State == "running":
			lane.Status = "running"
			available[lane.Name] = true
		case report.ObservedAt.Sub(lane.ProgressAt) > max(3*time.Duration(lane.PollIntervalSeconds)*time.Second, 30*time.Second):
			lane.Status = "stale"
		default:
			lane.Status = "idle"
			available[lane.Name] = true
		}
	}
	for _, definition := range WorkerLaneDefinitions() {
		if !available[definition.Name] {
			report.MissingLanes = append(report.MissingLanes, definition.Name)
		}
	}
	if len(report.MissingLanes) > 0 {
		report.Issues = append(report.Issues, "worker_lanes_unavailable")
	}
	for i := range report.Queues {
		q := &report.Queues[i]
		if q.OldestRunnableAt != nil {
			q.OldestWaitingSeconds = max(0, int64(report.ObservedAt.Sub(*q.OldestRunnableAt).Seconds()))
		}
		if q.OldestWaitingSeconds > int64(report.BacklogWarningSeconds) {
			report.Issues = append(report.Issues, q.Name+":backlog_age")
		}
		if q.ExpiredLeases > 0 {
			report.Issues = append(report.Issues, q.Name+":expired_leases")
		}
		if q.RecentFailures+q.RecentUnknown+q.RecentDeadLetters > 0 {
			report.Issues = append(report.Issues, q.Name+":recent_failures")
		}
	}
	if len(report.Issues) > 0 {
		report.Status = "degraded"
	}
}

// WorkerRuntimeDiagnostics is a read-only snapshot. It neither probes external
// providers nor recovers leases, retries writes, or changes unknown outcomes.
func (s *Store) WorkerRuntimeDiagnostics(ctx context.Context) (WorkerRuntimeReport, error) {
	report := WorkerRuntimeReport{RecentWindowSeconds: 3600, StartupGraceSeconds: 120, BacklogWarningSeconds: 300, RetentionDays: 7, Lanes: []WorkerRuntimeLaneStatus{}, Queues: []WorkerQueueStatus{}}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return report, err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&report.ObservedAt); err != nil {
		return report, err
	}
	rows, err := tx.Query(ctx, `SELECT i.id,l.lane,l.state,i.started_at,i.stopped_at,l.last_poll_at,l.started_at,l.completed_at,l.progress_at,l.completed_passes,l.poll_interval_seconds,l.stall_after_seconds FROM worker_runtime_instances i JOIN worker_runtime_lanes l ON l.instance_id=i.id WHERE l.progress_at>=now()-interval '7 days' OR i.started_at>=now()-interval '7 days' ORDER BY i.started_at DESC,l.lane`)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var l WorkerRuntimeLaneStatus
		if err = rows.Scan(&l.InstanceID, &l.Name, &l.State, &l.InstanceStartedAt, &l.InstanceStoppedAt, &l.LastPollAt, &l.StartedAt, &l.CompletedAt, &l.ProgressAt, &l.CompletedPasses, &l.PollIntervalSeconds, &l.StallAfterSeconds); err != nil {
			rows.Close()
			return report, err
		}
		report.Lanes = append(report.Lanes, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	for _, query := range workerQueueQueries() {
		var q WorkerQueueStatus
		if err = tx.QueryRow(ctx, query).Scan(&q.Name, &q.RunnablePending, &q.FuturePending, &q.OldestRunnableAt, &q.Running, &q.ExpiredLeases, &q.RecentFailures, &q.RecentUnknown, &q.RecentDeadLetters); err != nil {
			return report, err
		}
		report.Queues = append(report.Queues, q)
	}
	if err = tx.Commit(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return report, err
	}
	assessWorkerRuntime(&report)
	return report, nil
}
