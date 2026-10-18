// Package scheduler binds the application's fixed, audited background tasks.
// It does not accept executable code, schedules, or arbitrary tasks from users.
package scheduler

import (
	"context"
	"errors"
	"time"
)

type Key string

const (
	AffiliateSettlement            Key = "affiliate_settlement"
	ServiceExpiryNotifications     Key = "service_expiry_notifications"
	BillingNotifications           Key = "billing_notifications"
	SupplierSyncSchedule           Key = "supplier_sync_schedule"
	AutomaticPaymentReconciliation Key = "automatic_payment_reconciliation"
	OverdueServiceSuspensions      Key = "overdue_service_suspensions"
	AutomaticRenewals              Key = "automatic_renewals"
)

type Lane string

const (
	PaymentLane      Lane = "payment"
	NotificationLane Lane = "notification"
	RenewalLane      Lane = "renewal"
	ProvisionLane    Lane = "provision"
)

type Descriptor struct {
	Key      string        `json:"key"`
	Name     string        `json:"name"`
	Lane     Lane          `json:"lane"`
	Interval time.Duration `json:"-"`
	Startup  bool          `json:"startup"`
}

// Executor separates the task catalog from persistence and deployment config.
// Audit invokes ordinary work once; DailyAutoRenewals owns its daily gate and
// full-pass audit, so the runner must never add another audit around it.
type Executor interface {
	Audit(context.Context, Key, func(context.Context) (int, error)) (int, error)
	AffiliateSettlement(context.Context) (int, error)
	ServiceExpiryNotifications(context.Context) (int, error)
	BillingNotifications(context.Context) (int, error)
	SupplierSyncSchedule(context.Context) (int, error)
	AutomaticPaymentReconciliation(context.Context) (int, error)
	OverdueServiceSuspensions(context.Context) (int, error)
	DailyAutoRenewals(context.Context) (int, error)
}

type definition struct {
	descriptor Descriptor
	execute    func(Executor, context.Context) (int, error)
	ownsAudit  bool
}

var definitions = [...]definition{
	{Descriptor{string(AffiliateSettlement), "推介佣金结算", NotificationLane, time.Minute, false}, Executor.AffiliateSettlement, false},
	{Descriptor{string(ServiceExpiryNotifications), "服务到期通知调度", NotificationLane, time.Minute, false}, Executor.ServiceExpiryNotifications, false},
	{Descriptor{string(BillingNotifications), "账单通知调度", NotificationLane, time.Minute, false}, Executor.BillingNotifications, false},
	{Descriptor{string(SupplierSyncSchedule), "供应商同步调度", ProvisionLane, 2 * time.Second, false}, Executor.SupplierSyncSchedule, false},
	{Descriptor{string(OverdueServiceSuspensions), "逾期暂停入队", NotificationLane, time.Minute, false}, Executor.OverdueServiceSuspensions, false},
	{Descriptor{string(AutomaticRenewals), "自动续费处理", RenewalLane, time.Minute, true}, Executor.DailyAutoRenewals, true},
	{Descriptor{string(AutomaticPaymentReconciliation), "自动支付查单", PaymentLane, 10 * time.Second, false}, Executor.AutomaticPaymentReconciliation, false},
}

func Descriptors() []Descriptor {
	items := make([]Descriptor, len(definitions))
	for i, task := range definitions {
		items[i] = task.descriptor
	}
	return items
}

func lookup(key string) (definition, bool) {
	for _, task := range definitions {
		if task.descriptor.Key == key {
			return task, true
		}
	}
	return definition{}, false
}

func Lookup(key string) (Descriptor, bool) {
	task, found := lookup(key)
	return task.descriptor, found
}

var (
	ErrUnknownTask = errors.New("unknown scheduled task")
	ErrNoExecutor  = errors.New("scheduled task executor unavailable")
)

type Runner struct{ executor Executor }

func NewRunner(executor Executor) *Runner { return &Runner{executor: executor} }

// Run dispatches once. Audit failures and task errors never replay business
// work; retries, leases and idempotency remain owned by the business methods.
func (r *Runner) Run(ctx context.Context, key Key) (int, error) {
	task, found := lookup(string(key))
	if !found {
		return 0, ErrUnknownTask
	}
	if r == nil || r.executor == nil {
		return 0, ErrNoExecutor
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if task.ownsAudit {
		return task.execute(r.executor, ctx)
	}
	return r.executor.Audit(ctx, key, func(runCtx context.Context) (int, error) {
		return task.execute(r.executor, runCtx)
	})
}
