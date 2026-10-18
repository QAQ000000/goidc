package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/scheduler"
)

func TestScheduledLaneStartupTicksAndCancellation(t *testing.T) {
	for _, key := range []scheduler.Key{scheduler.AutomaticPaymentReconciliation, scheduler.AffiliateSettlement, scheduler.AutomaticRenewals} {
		t.Run(string(key), func(t *testing.T) {
			descriptor, ok := scheduler.Lookup(string(key))
			if !ok {
				t.Fatal("missing lane descriptor")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ticks := make(chan time.Time)
			ran := make(chan struct{}, 4)
			done := make(chan struct{})
			go func() {
				defer close(done)
				runScheduledTicks(ctx, descriptor.Startup, ticks, func() { ran <- struct{}{} })
			}()
			if descriptor.Startup {
				select {
				case <-ran:
				case <-time.After(2 * time.Second):
					t.Fatal("renewal lane did not run at startup")
				}
			} else {
				select {
				case <-ran:
					t.Fatal("non-startup lane ran before its first tick")
				default:
				}
			}
			// The unbuffered send proves the lane reached its tick select. An
			// unexpected startup callback would therefore remain as an extra run.
			select {
			case ticks <- time.Now():
			case <-time.After(2 * time.Second):
				t.Fatal("lane did not wait for its next tick")
			}
			close(ticks)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("lane did not finish the delivered tick")
			}
			select {
			case <-ran:
			default:
				t.Fatal("scheduled tick did not execute")
			}
			select {
			case <-ran:
				t.Fatal("lane ran more than once per delivered tick")
			default:
			}
		})
	}
	t.Run("cancel_active_lane", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		ticks := make(chan time.Time)
		done := make(chan struct{})
		go func() {
			defer close(done)
			runScheduledTicks(ctx, false, ticks, func() { t.Error("cancelled lane executed without a tick") })
		}()
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled lane did not stop")
		}
	})
	t.Run("pre_cancelled_with_queued_tick", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		ticks := make(chan time.Time, 1)
		ticks <- time.Now()
		calls := 0
		runScheduledTicks(ctx, true, ticks, func() { calls++ })
		if calls != 0 {
			t.Fatal("cancelled startup or a queued tick started business work")
		}
	})
}

type notificationExecutor struct {
	scheduler.Executor
	calls      []scheduler.Key
	audits     []scheduler.Key
	failure    scheduler.Key
	cancelAt   scheduler.Key
	cancel     context.CancelFunc
	wantCtx    context.Context
	contextBad bool
}

func (e *notificationExecutor) Audit(ctx context.Context, key scheduler.Key, run func(context.Context) (int, error)) (int, error) {
	e.audits = append(e.audits, key)
	if ctx != e.wantCtx {
		e.contextBad = true
	}
	return run(ctx)
}

func (e *notificationExecutor) work(ctx context.Context, key scheduler.Key) (int, error) {
	e.calls = append(e.calls, key)
	if ctx != e.wantCtx {
		e.contextBad = true
	}
	if key == e.cancelAt {
		e.cancel()
		return 0, context.Canceled
	}
	if key == e.failure {
		return 1, errors.New("injected task failure")
	}
	return 1, nil
}

func (e *notificationExecutor) AffiliateSettlement(ctx context.Context) (int, error) {
	return e.work(ctx, scheduler.AffiliateSettlement)
}
func (e *notificationExecutor) ServiceExpiryNotifications(ctx context.Context) (int, error) {
	return e.work(ctx, scheduler.ServiceExpiryNotifications)
}
func (e *notificationExecutor) BillingNotifications(ctx context.Context) (int, error) {
	return e.work(ctx, scheduler.BillingNotifications)
}
func (e *notificationExecutor) OverdueServiceSuspensions(ctx context.Context) (int, error) {
	return e.work(ctx, scheduler.OverdueServiceSuspensions)
}

func TestNotificationSchedulingOrderAndErrorIsolation(t *testing.T) {
	want := []scheduler.Key{scheduler.AffiliateSettlement, scheduler.ServiceExpiryNotifications, scheduler.BillingNotifications, scheduler.OverdueServiceSuspensions}
	t.Run("error_keeps_remaining_order", func(t *testing.T) {
		var output bytes.Buffer
		executor := &notificationExecutor{failure: scheduler.AffiliateSettlement, wantCtx: t.Context()}
		runNotificationSchedules(t.Context(), scheduler.NewRunner(executor), slog.New(slog.NewTextHandler(&output, nil)))
		if !slices.Equal(executor.calls, want) || !slices.Equal(executor.audits, want) || executor.contextBad {
			t.Fatal("task failure reordered, retried or prevented later scheduling", executor.calls, executor.audits)
		}
		for _, message := range []string{"affiliate commission settlement failed", "service expiry notifications queued", "billing notifications queued", "overdue service suspensions queued"} {
			if !strings.Contains(output.String(), message) {
				t.Fatal("worker lost its specific task result message", message)
			}
		}
	})
	t.Run("cancel_stops_remaining_business", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		executor := &notificationExecutor{wantCtx: ctx, cancelAt: scheduler.ServiceExpiryNotifications, cancel: cancel}
		var output bytes.Buffer
		runNotificationSchedules(ctx, scheduler.NewRunner(executor), slog.New(slog.NewTextHandler(&output, nil)))
		if !slices.Equal(executor.calls, want[:2]) || !slices.Equal(executor.audits, want[:2]) || executor.contextBad {
			t.Fatal("cancelled lane dispatched later tasks", executor.calls, executor.audits)
		}
		if strings.Contains(output.String(), "level=ERROR") {
			t.Fatal("normal cancellation was logged as a task failure")
		}
	})
}
