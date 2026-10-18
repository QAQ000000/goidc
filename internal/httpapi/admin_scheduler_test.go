package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestSchedulerHistoryLifecycleAndPermissions(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "scheduler-root", "super_admin")
	_, staff := user(t, db, h, "scheduler-staff", "admin")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	calls := 0
	db.RunScheduled(t.Context(), log, "billing_notifications", func(context.Context) (int, error) { calls++; return 3, nil })
	db.RunScheduled(t.Context(), log, "service_expiry_notifications", func(context.Context) (int, error) { calls++; return 0, errors.New("SECRET_TOKEN") })
	w := request(h, "GET", "/api/v1/admin/logs/jobs", nil, root, "")
	expect(t, w, 200)
	if calls != 2 || data(t, w)["total"].(float64) != 2 || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatal(w.Body.String())
	}
	w = request(h, "GET", "/api/v1/admin/logs/jobs?task=billing_notifications&status=succeeded&limit=1", nil, root, "")
	expect(t, w, 200)
	if data(t, w)["total"].(float64) != 1 {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"/api/v1/admin/logs/jobs", "/api/v1/admin/logs/jobs/trend"} {
		expect(t, request(h, "GET", path, nil, staff, ""), 403)
	}
	expect(t, request(h, "GET", "/api/v1/admin/logs/jobs/trend", nil, root, ""), 200)
	for _, suffix := range []string{"?task=bad", "?status=bad", "?from=bad", "?limit=0"} {
		expect(t, request(h, "GET", "/api/v1/admin/logs/jobs"+suffix, nil, root, ""), 400)
	}
	w = request(h, "GET", "/api/v1/admin/logs/jobs?offset=100", nil, root, "")
	expect(t, w, 200)
	if len(data(t, w)["items"].([]any)) != 0 {
		t.Fatal(w.Body.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	db.RunScheduled(ctx, log, "affiliate_settlement", func(context.Context) (int, error) { cancel(); return 0, context.Canceled })
	var status, code string
	if err := db.Pool.QueryRow(t.Context(), "SELECT status,error_code FROM scheduler_runs WHERE task_key='affiliate_settlement'").Scan(&status, &code); err != nil || status != "interrupted" || code != "context_cancelled" {
		t.Fatal(status, code, err)
	}
	// A finish-write fault cannot repeat business; the run remains unconfirmed.
	db.RunScheduled(t.Context(), log, "supplier_sync_schedule", func(context.Context) (int, error) {
		calls++
		_, err := db.Pool.Exec(t.Context(), "ALTER TABLE scheduler_runs RENAME TO scheduler_runs_finish_fault")
		return 1, err
	})
	if calls != 3 {
		t.Fatal("business repeated after finish fault")
	}
	if _, err := db.Pool.Exec(t.Context(), "ALTER TABLE scheduler_runs_finish_fault RENAME TO scheduler_runs"); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT status FROM scheduler_runs WHERE task_key='supplier_sync_schedule'").Scan(&status); err != nil || status != "running" {
		t.Fatal(status, err)
	}

	// Start-audit failure must still execute business once and never retry it.
	if _, err := db.Pool.Exec(t.Context(), "ALTER TABLE scheduler_runs RENAME TO scheduler_runs_unavailable"); err != nil {
		t.Fatal(err)
	}
	db.RunScheduled(t.Context(), log, "billing_notifications", func(context.Context) (int, error) { calls++; return 1, nil })
	if calls != 4 {
		t.Fatal("business repeated")
	}
}

func TestSchedulerTrendUsesShanghaiDateBoundary(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "scheduler-tz-root", "super_admin")
	// Stay within the rolling trend window while straddling Shanghai midnight
	// (16:00 UTC). Use the database clock so host clock differences cannot age
	// the fixture out of the query's now()-31 days boundary.
	var midnight time.Time
	if err := db.Pool.QueryRow(t.Context(), "SELECT (date_trunc('day',now() AT TIME ZONE 'Asia/Shanghai')-interval '1 day') AT TIME ZONE 'Asia/Shanghai'").Scan(&midnight); err != nil {
		t.Fatal(err)
	}
	before, after := midnight.Add(-30*time.Minute), midnight.Add(30*time.Minute)
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	previousDay, nextDay := before.In(zone).Format("2006-01-02"), after.In(zone).Format("2006-01-02")
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO scheduler_runs(task_key,started_at,finished_at,duration_ms,status,affected_count) VALUES ('billing_notifications',$1,$2,60000,'succeeded',1),('billing_notifications',$3,$4,60000,'failed',0)`, before, before.Add(time.Minute), after, after.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{previousDay, nextDay} {
		w := request(h, "GET", "/api/v1/admin/logs/jobs?from="+day+"&to="+day, nil, root, "")
		expect(t, w, 200)
		if data(t, w)["total"].(float64) != 1 {
			t.Fatal(w.Body.String())
		}
	}

	w := request(h, "GET", "/api/v1/admin/logs/jobs/trend?from="+nextDay+"&to="+nextDay, nil, root, "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"day":"`+nextDay+`"`) || strings.Contains(w.Body.String(), `"day":"`+previousDay+`"`) || !strings.Contains(w.Body.String(), `"timezone":"Asia/Shanghai"`) {
		t.Fatal(w.Body.String())
	}
	w = request(h, "GET", "/api/v1/admin/logs/jobs/trend?from="+previousDay+"&to="+previousDay, nil, root, "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"day":"`+previousDay+`"`) || strings.Contains(w.Body.String(), `"day":"`+nextDay+`"`) {
		t.Fatal(w.Body.String())
	}
}
