-- Browser subscriptions are capabilities, kept separately from Telegram chat
-- targets and encrypted at rest. Existing routes/consent remain unchanged.
ALTER TABLE channel_instances DROP CONSTRAINT channel_instances_category_check;
ALTER TABLE channel_instances ADD CONSTRAINT channel_instances_category_check
 CHECK(category IN ('payment','oauth','sms','mail','certification','provision','bot','push'));
ALTER TABLE notification_message_templates DROP CONSTRAINT notification_message_templates_channel_check;
ALTER TABLE notification_message_templates ADD CONSTRAINT notification_message_templates_channel_check CHECK(channel IN ('telegram','inbox','web_push'));
ALTER TABLE notification_routes DROP CONSTRAINT notification_routes_channel_check;
ALTER TABLE notification_routes ADD CONSTRAINT notification_routes_channel_check CHECK(channel IN ('telegram','inbox','web_push'));
ALTER TABLE user_notification_preferences DROP CONSTRAINT user_notification_preferences_channel_check;
ALTER TABLE user_notification_preferences ADD CONSTRAINT user_notification_preferences_channel_check CHECK(channel IN ('telegram','inbox','web_push'));
ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_channel_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check CHECK(channel IN ('telegram','inbox','web_push','mail','sms'));

CREATE TABLE notification_web_push_subscriptions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id bigint NOT NULL REFERENCES users(id),
 instance_id text NOT NULL REFERENCES channel_instances(id),
 endpoint_hash text NOT NULL UNIQUE CHECK(endpoint_hash ~ '^[a-f0-9]{64}$'),
 subscription_ciphertext bytea NOT NULL CHECK(octet_length(subscription_ciphertext)>28),
 session_token_hash text NOT NULL,
 vapid_public_key text NOT NULL,
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notification_web_push_subscriptions_user ON notification_web_push_subscriptions(user_id,instance_id,id) WHERE active;
CREATE INDEX notification_events_user_recent ON notification_events(user_id,id DESC);
ALTER TABLE notification_deliveries ADD COLUMN push_subscription_id bigint REFERENCES notification_web_push_subscriptions(id);
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_push_subscription_check
 CHECK(push_subscription_id IS NULL OR (channel='web_push' AND target='' AND target_id IS NULL));

INSERT INTO notification_message_templates(id,event_kind,channel,subject,body)
 SELECT replace(replace(event_kind,'.','-'),'_','-')||'-web_push',event_kind,'web_push','账户通知','您有新的账户通知，请登录查看。'
 FROM notification_message_templates WHERE channel='inbox';
INSERT INTO notification_routes(id,event_kind,channel,template_id)
 SELECT id,event_kind,channel,id FROM notification_message_templates WHERE channel='web_push';
