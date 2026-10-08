ALTER TABLE notification_templates
  ADD COLUMN channel text NOT NULL DEFAULT 'email' CHECK (channel IN ('email','sms')),
  ADD COLUMN provider_template_id text NOT NULL DEFAULT '' CHECK (char_length(provider_template_id) <= 160);

ALTER TABLE notification_templates DROP CONSTRAINT notification_templates_kind_key;
CREATE UNIQUE INDEX notification_templates_channel_kind ON notification_templates(channel, kind);

ALTER TABLE sms_outbox
  ADD COLUMN template_id text NOT NULL DEFAULT '' CHECK (char_length(template_id) <= 160);

INSERT INTO notification_templates(channel,kind,name,subject,body,provider_template_id)
VALUES
 ('sms','password_reset','密码重置短信','短信通知', '您的验证码为：{{code}}，有效期 {{expires_minutes}} 分钟。', ''),
 ('sms','verification_code','身份验证短信','短信通知', '您的验证码为：{{code}}，有效期 {{expires_minutes}} 分钟。', '')
ON CONFLICT (channel,kind) DO NOTHING;
