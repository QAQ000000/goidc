package store

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/codex/zjmfgo/internal/scheduler"
)

// RunScheduled records one invocation without retrying business work when audit
// writes fail. A crash leaves running evidence; no synthetic history is added.
func (s *Store) RunScheduled(ctx context.Context, log *slog.Logger, key string, run func(context.Context) (int, error)) (int, error) {
	if _, found := scheduler.Lookup(key); !found {
		return 0, scheduler.ErrUnknownTask
	}
	return s.runScheduled(ctx, log, key, false, run)
}

func (s *Store) runScheduled(ctx context.Context, log *slog.Logger, key string, fullPass bool, run func(context.Context) (int, error)) (int, error) {
	log = correlation.Logger(ctx, log)
	var id int64
	if err := s.Pool.QueryRow(ctx, "INSERT INTO scheduler_runs(task_key,is_full_pass) VALUES($1,$2) RETURNING id", key, fullPass).Scan(&id); err != nil {
		log.Error("scheduler audit start failed", "task_key", key, "error_code", "audit_write_failed")
		if fullPass {
			return 0, err
		}
	}
	count, err := run(ctx)
	if id != 0 {
		status, code := "succeeded", ""
		if err != nil {
			status, code = "failed", "task_failed"
			if ctx.Err() != nil {
				status, code = "interrupted", "context_cancelled"
			}
		}
		// Finish evidence even when the worker's execution context was cancelled.
		finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if count < 0 {
			count = 0
		}
		_, auditErr := s.Pool.Exec(finish, "UPDATE scheduler_runs SET finished_at=clock_timestamp(),duration_ms=greatest(0,floor(extract(epoch FROM (clock_timestamp()-started_at))*1000))::bigint,status=$2,affected_count=$3,error_code=$4 WHERE id=$1 AND status='running'", id, status, count, code)
		if auditErr != nil {
			log.Error("scheduler audit finish failed", "run_id", id, "task_key", key, "error_code", "audit_write_failed")
			if fullPass {
				err = errors.Join(err, auditErr)
			}
		}
	}
	return count, err
}
