package notification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/notify"
	"github.com/jackc/pgx/v5"
)

type centerFixtureProvider struct {
	result notify.Result
	calls  int
}

func (p *centerFixtureProvider) Send(context.Context, notify.Job) notify.Result {
	p.calls++
	return p.result
}

type centerFixtureStore struct {
	jobs                                    []*notify.Job
	claimErr, recoverErr, activeErr         error
	resolveErr, completeErr                 error
	active                                  []bool
	checks, claims, recoveries, resolutions int
	implementation                          string
	completed                               []notify.Result
	completeHook                            func()
}

func (s *centerFixtureStore) ClaimNotificationJob(context.Context) (*notify.Job, error) {
	s.claims++
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if len(s.jobs) == 0 {
		return nil, pgx.ErrNoRows
	}
	job := s.jobs[0]
	s.jobs = s.jobs[1:]
	return job, nil
}
func (s *centerFixtureStore) CompleteNotificationJob(_ context.Context, _ notify.Job, result notify.Result) error {
	if s.completeHook != nil {
		s.completeHook()
	}
	if s.completeErr != nil {
		return s.completeErr
	}
	s.completed = append(s.completed, result)
	return nil
}
func (s *centerFixtureStore) RecoverStaleNotificationJobs(context.Context) error {
	s.recoveries++
	return s.recoverErr
}
func (s *centerFixtureStore) NotificationJobActive(context.Context, notify.Job) (bool, error) {
	s.checks++
	if s.activeErr != nil {
		return false, s.activeErr
	}
	if len(s.active) > 0 {
		out := s.active[0]
		s.active = s.active[1:]
		return out, nil
	}
	return true, nil
}
func (s *centerFixtureStore) RuntimeChannelInstance(_ context.Context, id string, category channel.Category) (module.Config, string, error) {
	s.resolutions++
	if id != "bot-fixture" || category != channel.Bot {
		return module.Config{}, "", errors.New("wrong instance/category")
	}
	return module.Config{}, s.implementation, s.resolveErr
}

var centerFactorySequence atomic.Int64

func centerFixture(t *testing.T, result notify.Result) (*centerFixtureStore, *centerFixtureProvider) {
	t.Helper()
	p := &centerFixtureProvider{result: result}
	id := fmt.Sprintf("center_fixture_%d", centerFactorySequence.Add(1))
	if err := RegisterBotProvider(channel.Descriptor{ID: id, Name: "Center fixture", Category: channel.Bot}, func(module.Config) (BotProvider, error) { return p, nil }); err != nil {
		t.Fatal(err)
	}
	job := telegramFixtureJob()
	return &centerFixtureStore{jobs: []*notify.Job{&job}, implementation: id}, p
}

func TestNotificationCenterDispatchPreservesProviderKnowledge(t *testing.T) {
	for _, result := range []notify.Result{
		{Outcome: "accepted", Code: "telegram_accepted", ProviderMessageID: "77"},
		{Outcome: "unknown", Code: "telegram_response_unknown"},
		{Outcome: "failed", Code: "telegram_rejected"},
		{Outcome: "rate_limited", Code: "telegram_rate_limited", RetryAfter: 42000000000},
	} {
		t.Run(result.Outcome, func(t *testing.T) {
			db, provider := centerFixture(t, result)
			count, err := DrainNotificationCenter(context.Background(), db, 10)
			if err != nil || count != 1 || provider.calls != 1 || len(db.completed) != 1 || db.completed[0] != result || db.checks != 2 || db.recoveries != 1 {
				t.Fatalf("incorrect dispatch: count=%d error=%v store=%+v sends=%d", count, err, db, provider.calls)
			}
			if _, err = DrainNotificationCenter(context.Background(), db, 10); err != nil || provider.calls != 1 {
				t.Fatal("worker independently retried an external write")
			}
		})
	}
}

func TestNotificationCenterRejectsUnavailableOrChangedEligibilityBeforeSending(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*centerFixtureStore)
		code   string
	}{
		{"revoked_before_resolve", func(s *centerFixtureStore) { s.active = []bool{false} }, "eligibility_changed"},
		{"revoked_after_resolve", func(s *centerFixtureStore) { s.active = []bool{true, false} }, "eligibility_changed"},
		{"schema_or_credentials_unavailable", func(s *centerFixtureStore) { s.resolveErr = errors.New("secret_marker") }, "notification_channel_unavailable"},
		{"factory_unavailable", func(s *centerFixtureStore) { s.implementation = "missing_secret_marker" }, "notification_channel_unavailable"},
		{"unsupported_channel", func(s *centerFixtureStore) { s.jobs[0].Channel = "sms" }, "notification_channel_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, provider := centerFixture(t, notify.Result{Outcome: "accepted"})
			tc.mutate(db)
			count, err := DrainNotificationCenter(context.Background(), db, 10)
			if err != nil || count != 1 || provider.calls != 0 || len(db.completed) != 1 || db.completed[0].Outcome != "failed" || db.completed[0].Code != tc.code {
				t.Fatalf("ineligible job sent: %+v error=%v count=%d sends=%d", db, err, count, provider.calls)
			}
		})
	}
}

func TestNotificationCenterPersistenceAndLeaseFailuresNeverResendOrExposeErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*centerFixtureStore)
		sends  int
	}{
		{"recovery", func(s *centerFixtureStore) { s.recoverErr = errors.New("secret_marker") }, 0},
		{"claim", func(s *centerFixtureStore) { s.claimErr = errors.New("secret_marker") }, 0},
		{"lease", func(s *centerFixtureStore) { s.activeErr = errors.New("secret_marker") }, 0},
		{"completion", func(s *centerFixtureStore) { s.completeErr = errors.New("secret_marker") }, 1},
		{"expired_lease_completion", func(s *centerFixtureStore) { s.active = []bool{false}; s.completeErr = errors.New("secret_marker") }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, provider := centerFixture(t, notify.Result{Outcome: "unknown", Code: "telegram_transport_unknown"})
			tc.mutate(db)
			_, err := DrainNotificationCenter(context.Background(), db, 10)
			if err == nil || bytes.Contains([]byte(err.Error()), []byte("secret_marker")) || provider.calls != tc.sends || len(db.completed) != 0 {
				t.Fatalf("unsafe recovery error=%v sends=%d", err, provider.calls)
			}
		})
	}
	db, provider := centerFixture(t, notify.Result{Outcome: "accepted"})
	ctx, cancel := context.WithCancel(context.Background())
	db.completeHook = cancel
	var logs bytes.Buffer
	RunNotificationCenter(ctx, db, slog.New(slog.NewTextHandler(&logs, nil)))
	if provider.calls != 1 || bytes.Contains(logs.Bytes(), []byte("secret_marker")) {
		t.Fatal("isolated loop did not stop or exposed credentials")
	}
}
