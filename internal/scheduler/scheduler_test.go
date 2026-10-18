package scheduler

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

type fakeExecutor struct {
	audits   []Key
	calls    []Key
	contexts []context.Context
	workErr  error
	auditErr error
}

func (f *fakeExecutor) Audit(ctx context.Context, key Key, run func(context.Context) (int, error)) (int, error) {
	f.audits = append(f.audits, key)
	f.contexts = append(f.contexts, ctx)
	count, err := run(ctx)
	return count, errors.Join(err, f.auditErr)
}

func (f *fakeExecutor) work(ctx context.Context, key Key) (int, error) {
	f.calls = append(f.calls, key)
	f.contexts = append(f.contexts, ctx)
	return 3, f.workErr
}

func (f *fakeExecutor) AffiliateSettlement(ctx context.Context) (int, error) {
	return f.work(ctx, AffiliateSettlement)
}
func (f *fakeExecutor) ServiceExpiryNotifications(ctx context.Context) (int, error) {
	return f.work(ctx, ServiceExpiryNotifications)
}
func (f *fakeExecutor) BillingNotifications(ctx context.Context) (int, error) {
	return f.work(ctx, BillingNotifications)
}
func (f *fakeExecutor) SupplierSyncSchedule(ctx context.Context) (int, error) {
	return f.work(ctx, SupplierSyncSchedule)
}
func (f *fakeExecutor) AutomaticPaymentReconciliation(ctx context.Context) (int, error) {
	return f.work(ctx, AutomaticPaymentReconciliation)
}
func (f *fakeExecutor) OverdueServiceSuspensions(ctx context.Context) (int, error) {
	return f.work(ctx, OverdueServiceSuspensions)
}
func (f *fakeExecutor) DailyAutoRenewals(ctx context.Context) (int, error) {
	return f.work(ctx, AutomaticRenewals)
}

func TestCatalogIntegrityAndIsolation(t *testing.T) {
	items := Descriptors()
	if len(items) != 7 {
		t.Fatal("catalog must contain exactly the seven existing audited tasks", len(items))
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item.Key == "" || item.Name == "" || seen[item.Key] || item.Interval <= 0 {
			t.Fatal("catalog entry is empty, duplicated or unscheduled", item)
		}
		seen[item.Key] = true
		found, ok := Lookup(item.Key)
		if !ok || found != item {
			t.Fatal("lookup and descriptors disagree", item.Key)
		}
		switch Key(item.Key) {
		case AutomaticPaymentReconciliation:
			if item.Lane != PaymentLane || item.Interval != 10*time.Second || item.Startup {
				t.Fatal("payment lane timing changed")
			}
		case AutomaticRenewals:
			if item.Lane != RenewalLane || item.Interval != time.Minute || !item.Startup {
				t.Fatal("renewal startup or timing changed")
			}
		case SupplierSyncSchedule:
			if item.Lane != ProvisionLane || item.Interval != 2*time.Second || item.Startup {
				t.Fatal("supplier sync moved out of its provisioning tick")
			}
		default:
			if item.Lane != NotificationLane || item.Interval != time.Minute || item.Startup {
				t.Fatal("notification lane timing changed", item.Key)
			}
		}
	}
	original := items[0]
	items[0].Key, items[0].Name = "mutated", "mutated"
	if found, ok := Lookup(original.Key); !ok || found != original || Descriptors()[0] != original {
		t.Fatal("caller mutation changed the fixed catalog")
	}
	for _, excluded := range []string{"file_gc", "notification_center", "certification", "outbox", " billing_notifications", "BILLING_NOTIFICATIONS"} {
		if _, ok := Lookup(excluded); ok {
			t.Fatal("catalog accepted an unknown or excluded task", excluded)
		}
	}
}

func TestRunnerAuditOwnershipAndContext(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(t.Context(), contextKey{}, "request-context")
	for _, item := range Descriptors() {
		t.Run(item.Key, func(t *testing.T) {
			fake := &fakeExecutor{}
			count, err := NewRunner(fake).Run(ctx, Key(item.Key))
			if err != nil || count != 3 || !slices.Equal(fake.calls, []Key{Key(item.Key)}) {
				t.Fatal("runner did not dispatch the matching business entry once", count, fake.calls, err)
			}
			wantAudits := []Key{Key(item.Key)}
			if Key(item.Key) == AutomaticRenewals {
				wantAudits = nil
			}
			if !slices.Equal(fake.audits, wantAudits) {
				t.Fatal("ordinary audit missing or daily renewal double audited", fake.audits)
			}
			for _, got := range fake.contexts {
				if got != ctx || got.Value(contextKey{}) != "request-context" {
					t.Fatal("runner replaced the caller context")
				}
			}
		})
	}
}

func TestRunnerUnknownAndCancelledDoNotExecute(t *testing.T) {
	fake := &fakeExecutor{}
	runner := NewRunner(fake)
	for _, key := range []Key{"", "unknown", "billing_notifications ", "file_gc"} {
		if count, err := runner.Run(t.Context(), key); count != 0 || !errors.Is(err, ErrUnknownTask) {
			t.Fatal("unknown task did not fail closed", key, count, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if count, err := runner.Run(ctx, BillingNotifications); count != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled task entered execution", count, err)
	}
	if len(fake.calls) != 0 || len(fake.audits) != 0 {
		t.Fatal("rejected task invoked audit or business code")
	}
	for _, empty := range []*Runner{nil, NewRunner(nil)} {
		if _, err := empty.Run(t.Context(), BillingNotifications); !errors.Is(err, ErrNoExecutor) {
			t.Fatal("missing executor was accepted", err)
		}
	}
}

func TestRunnerFailuresDoNotRetry(t *testing.T) {
	failure := errors.New("injected failure")
	for _, test := range []struct {
		name string
		key  Key
		fake fakeExecutor
	}{
		{"business", BillingNotifications, fakeExecutor{workErr: failure}},
		{"audit", BillingNotifications, fakeExecutor{auditErr: failure}},
		{"daily", AutomaticRenewals, fakeExecutor{workErr: failure}},
	} {
		t.Run(test.name, func(t *testing.T) {
			count, err := NewRunner(&test.fake).Run(t.Context(), test.key)
			if count != 3 || !errors.Is(err, failure) || len(test.fake.calls) != 1 {
				t.Fatal("error hid partial work or replayed business work", count, test.fake.calls, err)
			}
			want := 1
			if test.key == AutomaticRenewals {
				want = 0
			}
			if len(test.fake.audits) != want {
				t.Fatal("error repeated auditing or double wrapped the daily gate", test.fake.audits)
			}
		})
	}
}
