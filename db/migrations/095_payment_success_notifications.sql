ALTER TABLE notification_delivery_events
  ALTER COLUMN service_id DROP NOT NULL,
  ADD COLUMN invoice_id bigint REFERENCES invoices(id) ON DELETE CASCADE;
ALTER TABLE notification_delivery_events DROP CONSTRAINT notification_delivery_events_kind_check;
ALTER TABLE notification_delivery_events DROP CONSTRAINT notification_delivery_events_stage_check;
ALTER TABLE notification_delivery_events ADD CONSTRAINT notification_delivery_events_kind_check CHECK (kind IN ('service_expiry_reminder','payment_success'));
ALTER TABLE notification_delivery_events ADD CONSTRAINT notification_delivery_events_stage_check CHECK ((kind='service_expiry_reminder' AND stage IN (1,7)) OR (kind='payment_success' AND stage=0));
CREATE UNIQUE INDEX notification_delivery_events_payment ON notification_delivery_events(invoice_id,channel,kind) WHERE invoice_id IS NOT NULL;

INSERT INTO notification_templates(channel,kind,name,subject,body,provider_template_id)
VALUES
 ('email','payment_success','支付成功通知','支付成功','您的账单 {{invoice_no}} 已支付成功，金额 {{amount}}。',''),
 ('sms','payment_success','支付成功通知','短信通知','您的账单{{invoice_no}}已支付成功，金额{{amount}}。','')
ON CONFLICT (channel,kind) DO NOTHING;
