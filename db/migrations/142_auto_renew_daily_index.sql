-- Earlier workers recorded a successful run for each limited batch. Those
-- receipts do not prove that a complete daily pass has finished.
ALTER TABLE scheduler_runs ADD COLUMN is_full_pass boolean NOT NULL DEFAULT false
CHECK (NOT is_full_pass OR task_key='automatic_renewals');
CREATE INDEX scheduler_auto_renew_completed ON scheduler_runs(started_at DESC)
WHERE task_key='automatic_renewals' AND status='succeeded' AND is_full_pass;
