package store

import (
	"context"
	"log/slog"
	"slices"

	"github.com/codex/zjmfgo/internal/scheduler"
)

type scheduledTaskExecutor struct {
	db       *Store
	log      *slog.Logger
	payments PaymentConfiguration
}

// NewScheduledTaskRunner binds compiled task entry points to this Store. The
// payment fallback stays deployment-local and is never part of the catalog.
func NewScheduledTaskRunner(db *Store, log *slog.Logger, fallback PaymentConfiguration) *scheduler.Runner {
	if db == nil {
		return scheduler.NewRunner(nil)
	}
	if log == nil {
		log = slog.Default()
	}
	fallback.Types = slices.Clone(fallback.Types)
	return scheduler.NewRunner(&scheduledTaskExecutor{db: db, log: log, payments: fallback})
}

func (e *scheduledTaskExecutor) Audit(ctx context.Context, key scheduler.Key, run func(context.Context) (int, error)) (int, error) {
	return e.db.RunScheduled(ctx, e.log, string(key), run)
}

func (e *scheduledTaskExecutor) AffiliateSettlement(ctx context.Context) (int, error) {
	return e.db.SettleAffiliateCommissions(ctx, 100)
}

func (e *scheduledTaskExecutor) ServiceExpiryNotifications(ctx context.Context) (int, error) {
	return e.db.EnqueueServiceExpiryReminders(ctx, 100)
}

func (e *scheduledTaskExecutor) BillingNotifications(ctx context.Context) (int, error) {
	return e.db.EnqueueBillingNotifications(ctx, 100)
}

func (e *scheduledTaskExecutor) SupplierSyncSchedule(ctx context.Context) (int, error) {
	count, err := e.db.EnqueuePeriodicSupplierSyncs(ctx)
	return int(count), err
}

func (e *scheduledTaskExecutor) AutomaticPaymentReconciliation(ctx context.Context) (int, error) {
	changed, err := e.db.ProcessConfiguredAutomaticPaymentCheck(ctx, e.payments)
	if changed {
		return 1, err
	}
	return 0, err
}

func (e *scheduledTaskExecutor) OverdueServiceSuspensions(ctx context.Context) (int, error) {
	return e.db.EnqueueOverdueServiceSuspensions(ctx, 100)
}

func (e *scheduledTaskExecutor) DailyAutoRenewals(ctx context.Context) (int, error) {
	return e.db.RunDailyAutoRenewals(ctx, e.log)
}
