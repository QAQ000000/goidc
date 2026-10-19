-- Preserve old date-only deduplication facts without guessing the original
-- instant from today's service or invoice. New reminders snapshot the exact
-- deadline so a same-day extension invalidates the previous message.
ALTER TABLE notification_delivery_events ADD COLUMN resource_due_at timestamptz;
ALTER TABLE notification_delivery_events ADD CONSTRAINT notification_delivery_events_deadline_kind_check
 CHECK(resource_due_at IS NULL OR kind IN ('service_expiry_reminder','order_payment_reminder','invoice_overdue_reminder'));

CREATE INDEX notification_events_expired_service ON notification_events(service_id,kind)
 WHERE kind='service.expired';

ALTER TABLE notification_delivery_events DROP CONSTRAINT notification_delivery_events_stage_check;
ALTER TABLE notification_delivery_events ADD CONSTRAINT notification_delivery_events_stage_check
 CHECK((kind='service_expiry_reminder' AND stage BETWEEN 1 AND 365) OR
       (kind IN ('payment_success','service_provision_success','service_suspended',
                 'order_payment_reminder','invoice_overdue_reminder') AND stage=0));

-- PostgreSQL chooses the original multi-column constraint name by truncating
-- identifiers. Identify the exact original column set rather than guessing it.
DO $$
DECLARE old_constraint text;
BEGIN
 FOR old_constraint IN
  SELECT conname FROM pg_constraint
  WHERE conrelid='notification_delivery_events'::regclass AND contype='u'
   AND conkey=ARRAY[
    (SELECT attnum FROM pg_attribute WHERE attrelid='notification_delivery_events'::regclass AND attname='service_id'),
    (SELECT attnum FROM pg_attribute WHERE attrelid='notification_delivery_events'::regclass AND attname='channel'),
    (SELECT attnum FROM pg_attribute WHERE attrelid='notification_delivery_events'::regclass AND attname='kind'),
    (SELECT attnum FROM pg_attribute WHERE attrelid='notification_delivery_events'::regclass AND attname='stage'),
    (SELECT attnum FROM pg_attribute WHERE attrelid='notification_delivery_events'::regclass AND attname='due_date')
   ]::smallint[]
 LOOP
  EXECUTE format('ALTER TABLE notification_delivery_events DROP CONSTRAINT %I',old_constraint);
 END LOOP;
END $$;
CREATE UNIQUE INDEX notification_delivery_events_service_legacy_date
 ON notification_delivery_events(service_id,channel,kind,stage,due_date)
 WHERE service_id IS NOT NULL AND resource_due_at IS NULL;
CREATE UNIQUE INDEX notification_delivery_events_service_exact_due
 ON notification_delivery_events(service_id,channel,kind,stage,resource_due_at)
 WHERE service_id IS NOT NULL AND resource_due_at IS NOT NULL;

DROP INDEX notification_delivery_events_invoice_schedule;
CREATE UNIQUE INDEX notification_delivery_events_invoice_legacy_date
 ON notification_delivery_events(invoice_id,channel,kind,stage,due_date)
 WHERE invoice_id IS NOT NULL AND resource_due_at IS NULL
  AND kind IN ('order_payment_reminder','invoice_overdue_reminder');
CREATE UNIQUE INDEX notification_delivery_events_invoice_exact_due
 ON notification_delivery_events(invoice_id,channel,kind,stage,due_date,resource_due_at)
 WHERE invoice_id IS NOT NULL AND resource_due_at IS NOT NULL
  AND kind IN ('order_payment_reminder','invoice_overdue_reminder');
CREATE INDEX notification_delivery_events_deadline_email
 ON notification_delivery_events(email_outbox_id)
 WHERE email_outbox_id IS NOT NULL AND resource_due_at IS NOT NULL;
CREATE INDEX notification_delivery_events_deadline_sms
 ON notification_delivery_events(sms_outbox_id)
 WHERE sms_outbox_id IS NOT NULL AND resource_due_at IS NOT NULL;

-- A date-only pending message cannot prove which precise entitlement it
-- describes. Invalidate only these reminders; retain sent rows and every
-- original deduplication fact, and leave other outbox families untouched.
UPDATE email_outbox o SET status='expired',body='[expired]',body_encrypted=''::bytea,
 locked_at=NULL,dead_letter_at=NULL,last_error='deadline snapshot unavailable'
 WHERE o.kind IN ('service_expiry_reminder','order_payment_reminder','invoice_overdue_reminder')
  AND o.status IN ('pending','sending','dead_letter');
UPDATE sms_outbox o SET status='expired',body_encrypted=''::bytea,
 locked_at=NULL,dead_letter_at=NULL,last_error='deadline snapshot unavailable'
 WHERE o.kind IN ('service_expiry_reminder','order_payment_reminder','invoice_overdue_reminder')
  AND o.status IN ('pending','sending','dead_letter');

CREATE FUNCTION preserve_notification_deadline_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.kind IN ('service_expiry_reminder','order_payment_reminder','invoice_overdue_reminder')
  AND ROW(NEW.service_id,NEW.invoice_id,NEW.user_id,NEW.channel,NEW.kind,NEW.stage,NEW.due_date,NEW.resource_due_at)
   IS DISTINCT FROM ROW(OLD.service_id,OLD.invoice_id,OLD.user_id,OLD.channel,OLD.kind,OLD.stage,OLD.due_date,OLD.resource_due_at)
 THEN RAISE EXCEPTION 'notification_deadline_snapshot_immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER notification_deadline_snapshot_immutable BEFORE UPDATE ON notification_delivery_events
 FOR EACH ROW EXECUTE FUNCTION preserve_notification_deadline_snapshot();
