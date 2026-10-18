ALTER TABLE channel_instances DROP CONSTRAINT channel_instances_category_check;
ALTER TABLE channel_instances ADD CONSTRAINT channel_instances_category_check CHECK (category IN ('payment','oauth','sms','mail','certification','provision','bot'));

CREATE TABLE notification_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 kind text NOT NULL CHECK (kind='invoice.paid'),
 dedupe_key text NOT NULL UNIQUE,
 user_id bigint NOT NULL REFERENCES users(id),
 invoice_id bigint NOT NULL REFERENCES invoices(id),
 payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object'),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE notification_message_templates (
 id text PRIMARY KEY,
 event_kind text NOT NULL CHECK(event_kind='invoice.paid'),
 channel text NOT NULL CHECK(channel IN ('telegram','inbox')),
 subject text NOT NULL CHECK(char_length(subject) BETWEEN 1 AND 240),
 body text NOT NULL CHECK(char_length(body) BETWEEN 1 AND 4000),
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(id,event_kind,channel)
);
CREATE TABLE notification_routes (
 id text PRIMARY KEY,
 event_kind text NOT NULL CHECK(event_kind='invoice.paid'),
 channel text NOT NULL CHECK(channel IN ('telegram','inbox')),
 instance_id text REFERENCES channel_instances(id),
 template_id text NOT NULL,
 enabled boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(template_id,event_kind,channel) REFERENCES notification_message_templates(id,event_kind,channel),
 CHECK(channel<>'inbox' OR instance_id IS NULL)
);
CREATE TABLE user_notification_preferences (
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 channel text NOT NULL CHECK(channel IN ('telegram','inbox')),
 enabled boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,channel)
);
CREATE TABLE notification_targets (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id bigint NOT NULL REFERENCES users(id),
 channel text NOT NULL CHECK(channel='telegram'),
 instance_id text NOT NULL REFERENCES channel_instances(id),
 target text NOT NULL CHECK(target ~ '^[1-9][0-9]{0,19}$'),
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(instance_id,target)
);
CREATE UNIQUE INDEX notification_targets_user_active ON notification_targets(user_id,instance_id) WHERE active;
CREATE TABLE notification_binding_challenges (
 token_hash text PRIMARY KEY CHECK(length(token_hash)=64),
 user_id bigint NOT NULL REFERENCES users(id),
 instance_id text NOT NULL REFERENCES channel_instances(id),
 session_token_hash text NOT NULL,
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz,
 revoked_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notification_binding_challenges_user ON notification_binding_challenges(user_id,created_at DESC);
CREATE TABLE notification_deliveries (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 event_id bigint NOT NULL REFERENCES notification_events(id),
 route_key text NOT NULL,
 route_id text REFERENCES notification_routes(id),
 route_version bigint,
 template_version bigint,
 preference_version bigint,
 target_version bigint,
 channel text NOT NULL CHECK(channel IN ('telegram','inbox','mail','sms')),
 target_id bigint REFERENCES notification_targets(id),
 instance_id text REFERENCES channel_instances(id),
 target text NOT NULL DEFAULT '',
 subject text NOT NULL DEFAULT '',
 body text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sending','accepted','failed','unknown','skipped')),
 code text NOT NULL DEFAULT '',
 provider_message_id text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 lease_token text,
 lease_until timestamptz,
 expires_at timestamptz NOT NULL DEFAULT (now()+interval '24 hours'),
 legacy_email_outbox_id bigint REFERENCES email_outbox(id) ON DELETE SET NULL,
 legacy_sms_outbox_id bigint REFERENCES sms_outbox(id) ON DELETE SET NULL,
 inbox_message_id bigint REFERENCES admin_inbox_messages(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(event_id,route_key),
 CHECK((status='sending')=(lease_token IS NOT NULL AND lease_until IS NOT NULL)),
 CHECK(channel='mail' OR legacy_email_outbox_id IS NULL),
 CHECK(channel='sms' OR legacy_sms_outbox_id IS NULL)
);
CREATE INDEX notification_deliveries_pending ON notification_deliveries(next_attempt_at,id) WHERE status='pending';
CREATE TABLE notification_delivery_attempts (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 delivery_id bigint NOT NULL REFERENCES notification_deliveries(id),
 attempt integer NOT NULL CHECK(attempt BETWEEN 1 AND 5),
 lease_token text NOT NULL UNIQUE,
 status text NOT NULL CHECK(status IN ('sending','accepted','failed','unknown')),
 code text NOT NULL DEFAULT '',
 provider_message_id text NOT NULL DEFAULT '',
 started_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz,
 UNIQUE(delivery_id,attempt)
);
INSERT INTO notification_message_templates(id,event_kind,channel,subject,body) VALUES
 ('invoice-paid-telegram','invoice.paid','telegram','付款成功','账单 {{invoice_no}} 已付款，金额 {{amount}}。'),
 ('invoice-paid-inbox','invoice.paid','inbox','付款成功','账单 {{invoice_no}} 已付款，金额 {{amount}}。');
INSERT INTO notification_routes(id,event_kind,channel,template_id) VALUES
 ('invoice-paid-telegram','invoice.paid','telegram','invoice-paid-telegram'),
 ('invoice-paid-inbox','invoice.paid','inbox','invoice-paid-inbox');
