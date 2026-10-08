-- Notification scheduling and delivery retry completion. Keep the existing
-- outbox rows usable while adding a terminal dead-letter state.
ALTER TABLE email_outbox
  ADD COLUMN max_attempts integer NOT NULL DEFAULT 5 CHECK (max_attempts BETWEEN 1 AND 20),
  ADD COLUMN dead_letter_at timestamptz;
ALTER TABLE email_outbox DROP CONSTRAINT email_outbox_status_check;
ALTER TABLE email_outbox ADD CONSTRAINT email_outbox_status_check
  CHECK (status IN ('pending','sending','sent','expired','dead_letter'));

ALTER TABLE sms_outbox
  ADD COLUMN max_attempts integer NOT NULL DEFAULT 5 CHECK (max_attempts BETWEEN 1 AND 20),
  ADD COLUMN dead_letter_at timestamptz;
UPDATE sms_outbox SET status='dead_letter', dead_letter_at=COALESCE(dead_letter_at, now()) WHERE status='failed';
ALTER TABLE sms_outbox DROP CONSTRAINT sms_outbox_status_check;
ALTER TABLE sms_outbox ADD CONSTRAINT sms_outbox_status_check
  CHECK (status IN ('pending','sending','sent','expired','dead_letter'));

ALTER TABLE notification_delivery_events DROP CONSTRAINT notification_delivery_events_kind_check;
ALTER TABLE notification_delivery_events DROP CONSTRAINT notification_delivery_events_stage_check;
ALTER TABLE notification_delivery_events ADD CONSTRAINT notification_delivery_events_kind_check
CHECK (kind IN ('service_expiry_reminder','payment_success','service_provision_success','service_suspended',
                'order_payment_reminder','invoice_overdue_reminder'));
ALTER TABLE notification_delivery_events ADD CONSTRAINT notification_delivery_events_stage_check
CHECK ((kind='service_expiry_reminder' AND stage IN (1,7)) OR
       (kind IN ('payment_success','service_provision_success','service_suspended',
                 'order_payment_reminder','invoice_overdue_reminder') AND stage=0));

CREATE UNIQUE INDEX notification_delivery_events_invoice_schedule
ON notification_delivery_events(invoice_id, channel, kind, stage, due_date)
WHERE invoice_id IS NOT NULL AND kind IN ('order_payment_reminder','invoice_overdue_reminder');

INSERT INTO notification_templates(channel,kind,name,subject,body,provider_template_id)
VALUES
 ('email','order_payment_reminder','待支付订单提醒','订单待支付','您的订单 {{order_no}} 尚未支付，账单 {{invoice_no}} 金额 {{amount}}，请在 {{due_date}} 前完成支付。',''),
 ('sms','order_payment_reminder','待支付订单提醒','短信通知','您的订单{{order_no}}尚未支付，账单{{invoice_no}}金额{{amount}}，请及时支付。',''),
 ('email','invoice_overdue_reminder','账单逾期催缴','账单逾期提醒','您的账单 {{invoice_no}} 已于 {{due_date}} 到期，当前应付金额 {{amount}}，请尽快完成支付。',''),
 ('sms','invoice_overdue_reminder','账单逾期催缴','短信通知','您的账单{{invoice_no}}已逾期，金额{{amount}}，请尽快完成支付。','')
ON CONFLICT (channel,kind) DO NOTHING;
