package notification

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/notify"
	"github.com/jackc/pgx/v5"
)

// NotificationCenterStore is independent from the legacy email/SMS queues.
// Store recovery turns expired external leases into unknown, never pending.
type NotificationCenterStore interface {
	ClaimNotificationJob(context.Context) (*notify.Job, error)
	CompleteNotificationJob(context.Context, notify.Job, notify.Result) error
	RecoverStaleNotificationJobs(context.Context) error
	NotificationJobActive(context.Context, notify.Job) (bool, error)
	RuntimeChannelInstance(context.Context, string, channel.Category) (module.Config, string, error)
}

// DrainNotificationCenter performs one bounded pass. Inbox deliveries are
// handled atomically by the store claim transaction and never reach adapters.
func DrainNotificationCenter(ctx context.Context, db NotificationCenterStore, limit int, logs ...*slog.Logger) (int, error) {
	if limit < 1 || limit > 100 {
		limit = 10
	}
	if err := db.RecoverStaleNotificationJobs(ctx); err != nil {
		return 0, errors.New("notification recovery failed")
	}
	completed := 0
	for i := 0; i < limit; i++ {
		if ctx.Err() != nil {
			return completed, errors.New("notification worker interrupted")
		}
		job, err := db.ClaimNotificationJob(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return completed, nil
		}
		if err != nil || job == nil {
			return completed, errors.New("notification claim failed")
		}
		jobCtx := correlation.JobContext(ctx, job.CorrelationID)
		var jobLog *slog.Logger
		if len(logs) > 0 && logs[0] != nil {
			jobLog = correlation.Logger(jobCtx, logs[0]).With("job_id", job.ID, "attempt_id", correlation.NewID(), "channel", job.Channel)
			jobLog.Info("notification job attempt started")
		}
		failure := func(code string) error {
			if jobLog != nil {
				jobLog.Error("notification job attempt failed", "code", code)
			}
			return errors.New(code)
		}
		active, err := db.NotificationJobActive(jobCtx, *job)
		if err != nil {
			return completed, failure("notification lease check failed")
		}
		if !active {
			if err = db.CompleteNotificationJob(jobCtx, *job, notify.Result{Outcome: "failed", Code: "eligibility_changed"}); err != nil {
				return completed, failure("notification completion failed")
			}
			completed++
			continue
		}
		result := notify.Result{Outcome: "failed", Code: "notification_channel_unavailable"}
		var provider interface {
			Send(context.Context, notify.Job) notify.Result
		}
		var category channel.Category
		switch job.Channel {
		case "telegram":
			category = channel.Bot
		case "web_push":
			category = channel.Push
		}
		if category != "" {
			config, implementation, resolveErr := db.RuntimeChannelInstance(jobCtx, job.InstanceID, category)
			if resolveErr == nil {
				switch category {
				case channel.Bot:
					provider, _ = NewBotProvider(implementation, config)
				case channel.Push:
					provider, _ = NewPushProvider(implementation, config)
				}
			}
		}
		if provider != nil {
			// Check again after resolving credentials, before any external write.
			active, err = db.NotificationJobActive(jobCtx, *job)
			if err != nil {
				return completed, failure("notification lease check failed")
			}
			if !active {
				if err = db.CompleteNotificationJob(jobCtx, *job, notify.Result{Outcome: "failed", Code: "eligibility_changed"}); err != nil {
					return completed, failure("notification completion failed")
				}
				completed++
				continue
			}
			if jobCtx.Err() != nil {
				return completed, failure("notification worker interrupted")
			}
			result = provider.Send(jobCtx, *job)
		}
		if err = db.CompleteNotificationJob(jobCtx, *job, result); err != nil {
			// After an external result, never resend on persistence failure. Its
			// lease remains processing and later recovery records unknown.
			return completed, failure("notification completion failed")
		}
		completed++
	}
	return completed, nil
}

// RunNotificationCenter owns its own polling goroutine in cmd/worker so slow
// Bot requests cannot delay provisioning, verification, or legacy delivery.
func RunNotificationCenter(ctx context.Context, db NotificationCenterStore, log *slog.Logger, observers ...func(bool)) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		func() {
			for _, observe := range observers {
				observe(true)
				defer observe(false)
			}
			passCtx := correlation.WithID(ctx, correlation.NewID())
			if _, err := DrainNotificationCenter(passCtx, db, 10, log); err != nil && ctx.Err() == nil && log != nil {
				// All drain errors are fixed local codes; no transport URL/body is logged.
				correlation.Logger(passCtx, log).Error("notification center processing failed", "code", err.Error())
			}
		}()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
