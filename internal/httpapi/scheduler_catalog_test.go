package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/codex/zjmfgo/internal/scheduler"
	"github.com/codex/zjmfgo/internal/store"
)

func TestSchedulerFiltersUseCompiledCatalog(t *testing.T) {
	for _, task := range scheduler.Descriptors() {
		r := httptest.NewRequest("GET", "/?task="+url.QueryEscape(task.Key), nil)
		where, args, ok := schedulerFilter(r)
		if !ok || where != "TRUE AND task_key=$1" || len(args) != 1 || args[0] != task.Key {
			t.Fatalf("registered task %q rejected: %s %v %v", task.Key, where, args, ok)
		}
	}
	for _, key := range []string{"unregistered", "billing_notifications ", "BILLING_NOTIFICATIONS", "billing_notifications' OR TRUE--"} {
		if _, _, ok := schedulerFilter(httptest.NewRequest("GET", "/?task="+url.QueryEscape(key), nil)); ok {
			t.Fatalf("unknown task %q accepted", key)
		}
	}
}

func TestSchedulerCatalogResponsePermissionsAndFiltering(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "scheduler-catalog-root", "super_admin")
	_, staff := user(t, db, h, "scheduler-catalog-staff", "admin")
	_, customer := user(t, db, h, "scheduler-catalog-customer", "customer")
	path := "/api/v1/admin/logs/jobs"
	for _, cookie := range []string{staff, customer} {
		expect(t, request(h, "GET", path, nil, cookie, ""), 403)
	}
	w := request(h, "GET", path, nil, root, "")
	expect(t, w, 200)
	result := data(t, w)
	tasks := result["tasks"].([]any)
	descriptors := scheduler.Descriptors()
	if len(tasks) != len(descriptors) || result["total"].(float64) != 0 || len(result["items"].([]any)) != 0 {
		t.Fatal(w.Body.String())
	}
	for i, descriptor := range descriptors {
		metadata := tasks[i].(map[string]any)
		if metadata["key"] != descriptor.Key || metadata["name"] != descriptor.Name {
			t.Fatalf("metadata differs from compiled catalog: %v", metadata)
		}
		if _, err := db.Pool.Exec(t.Context(), "INSERT INTO scheduler_runs(task_key) VALUES($1)", descriptor.Key); err != nil {
			t.Fatal(err)
		}
		w = request(h, "GET", path+"?task="+url.QueryEscape(descriptor.Key), nil, root, "")
		expect(t, w, 200)
		filtered := data(t, w)
		if filtered["total"].(float64) != 1 || filtered["items"].([]any)[0].(map[string]any)["task_key"] != descriptor.Key || len(filtered["tasks"].([]any)) != len(tasks) {
			t.Fatal(w.Body.String())
		}
	}
}

func TestSchedulerRunnerBusinessAuditAndDailyGate(t *testing.T) {
	db, _ := testServer(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := store.NewScheduledTaskRunner(db, log, store.PaymentConfiguration{})
	for _, descriptor := range scheduler.Descriptors() {
		if n, err := runner.Run(t.Context(), scheduler.Key(descriptor.Key)); err != nil || n != 0 {
			t.Fatalf("empty-database task %s: count=%d err=%v", descriptor.Key, n, err)
		}
		var audits, fullPasses int
		if err := db.Pool.QueryRow(t.Context(), "SELECT count(*),count(*) FILTER(WHERE is_full_pass) FROM scheduler_runs WHERE task_key=$1 AND status='succeeded'", descriptor.Key).Scan(&audits, &fullPasses); err != nil {
			t.Fatal(err)
		}
		if audits != 1 || (descriptor.Key == string(scheduler.AutomaticRenewals)) != (fullPasses == 1) {
			t.Fatalf("duplicate/missing audit or incorrect full-pass marker for %s: %d %d", descriptor.Key, audits, fullPasses)
		}
	}
	if _, err := runner.Run(t.Context(), scheduler.AutomaticRenewals); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM scheduler_runs").Scan(&count); err != nil || count != len(scheduler.Descriptors()) {
		t.Fatalf("second daily poll duplicated audit: %d %v", count, err)
	}
	if _, err := runner.Run(t.Context(), scheduler.Key("not_registered")); err == nil {
		t.Fatal("unknown task accepted")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runner.Run(cancelled, scheduler.BillingNotifications); err == nil {
		t.Fatal("cancelled task accepted")
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM scheduler_runs").Scan(&count); err != nil || count != len(scheduler.Descriptors()) {
		t.Fatalf("rejected run wrote audit: %d %v", count, err)
	}
}
