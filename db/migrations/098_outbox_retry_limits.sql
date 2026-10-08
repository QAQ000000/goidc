-- Payment success remains once per invoice/channel; scheduled reminders use
-- the dated unique index introduced by 098_notification_scheduling_outbox.
DROP INDEX notification_delivery_events_payment;
CREATE UNIQUE INDEX notification_delivery_events_payment
ON notification_delivery_events(invoice_id,channel,kind)
WHERE invoice_id IS NOT NULL AND kind NOT IN ('order_payment_reminder','invoice_overdue_reminder');

-- Repair legacy pending rows already at the retry limit. Keep all existing
-- payloads and live sending leases available for the normal worker recovery.
UPDATE email_outbox SET status='dead_letter',dead_letter_at=now(),locked_at=NULL
WHERE status='pending' AND attempts>=max_attempts;
UPDATE sms_outbox SET status='dead_letter',dead_letter_at=now(),locked_at=NULL
WHERE status='pending' AND attempts>=max_attempts;

CREATE INDEX IF NOT EXISTS invoices_unpaid_notification_due
ON invoices(due_at,id) WHERE status='unpaid' AND NOT import_review_required;
CREATE INDEX IF NOT EXISTS invoice_items_service_notification
ON invoice_items(service_id,invoice_id) WHERE item_type IN ('renew','service','upgrade');
