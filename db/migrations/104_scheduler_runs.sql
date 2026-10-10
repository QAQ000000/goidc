CREATE TABLE scheduler_runs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 task_key text NOT NULL CHECK(task_key IN ('affiliate_settlement','service_expiry_notifications','billing_notifications','supplier_sync_schedule','overdue_service_suspensions','automatic_renewals','automatic_payment_reconciliation')),
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 duration_ms bigint CHECK(duration_ms >= 0),
 status text NOT NULL DEFAULT 'running' CHECK(status IN ('running','succeeded','failed','interrupted')),
 affected_count integer CHECK(affected_count >= 0),
 error_code text NOT NULL DEFAULT '' CHECK(error_code IN ('','task_failed','context_cancelled')),
 CHECK ((status='running' AND finished_at IS NULL AND duration_ms IS NULL) OR (status<>'running' AND finished_at IS NOT NULL AND duration_ms IS NOT NULL))
);
CREATE INDEX scheduler_runs_time ON scheduler_runs(started_at DESC,id DESC);
