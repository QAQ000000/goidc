CREATE TABLE notification_delivery_events (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  service_id bigint NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  channel text NOT NULL CHECK (channel IN ('email','sms')),
  kind text NOT NULL CHECK (kind = 'service_expiry_reminder'),
  stage smallint NOT NULL CHECK (stage IN (1,7)),
  due_date date NOT NULL,
  email_outbox_id bigint REFERENCES email_outbox(id) ON DELETE SET NULL,
  sms_outbox_id bigint REFERENCES sms_outbox(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(service_id, channel, kind, stage, due_date)
);
CREATE INDEX notification_delivery_events_user ON notification_delivery_events(user_id, created_at DESC);

INSERT INTO notification_templates(channel,kind,name,subject,body,provider_template_id)
VALUES
 ('email','service_expiry_reminder','服务到期提醒','服务即将到期','您的服务“{{product_name}}”将在 {{days_left}} 天后到期（{{due_date}}）。如需继续使用，请及时续费。',''),
 ('sms','service_expiry_reminder','服务到期提醒','短信通知','您的服务{{product_name}}将在{{days_left}}天后到期（{{due_date}}），请及时续费。','')
ON CONFLICT (channel,kind) DO NOTHING;
