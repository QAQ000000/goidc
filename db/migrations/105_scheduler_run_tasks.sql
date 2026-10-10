ALTER TABLE scheduler_runs DROP CONSTRAINT scheduler_runs_task_key_check;
ALTER TABLE scheduler_runs ADD CONSTRAINT scheduler_runs_task_key_check CHECK (task_key IN (
  'affiliate_settlement',
  'service_expiry_notifications',
  'billing_notifications',
  'supplier_sync_schedule',
  'automatic_payment_reconciliation',
  'overdue_service_suspensions',
  'automatic_renewals'
));
