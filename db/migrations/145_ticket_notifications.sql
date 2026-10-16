CREATE TABLE user_ticket_notification_preferences (
 user_id bigint PRIMARY KEY REFERENCES users(id), email_enabled boolean NOT NULL DEFAULT true,
 sms_enabled boolean NOT NULL DEFAULT false, version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE ticket_notification_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, event_key text NOT NULL UNIQUE,
 ticket_id bigint NOT NULL REFERENCES tickets(id), reply_id bigint REFERENCES ticket_replies(id),
 actor_id bigint NOT NULL REFERENCES users(id), kind text NOT NULL CHECK(kind IN ('customer_created','customer_replied','staff_replied')),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE ticket_notification_deliveries (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, event_id bigint NOT NULL REFERENCES ticket_notification_events(id),
 recipient_kind text NOT NULL CHECK(recipient_kind IN ('user','contact','cc')), recipient_id bigint NOT NULL,
 user_id bigint REFERENCES users(id), contact_id bigint REFERENCES customer_contacts(id), customer_id bigint NOT NULL REFERENCES users(id),
 channel text NOT NULL CHECK(channel IN ('email','sms')), address text NOT NULL, normalized_address text NOT NULL,
 preference_enabled boolean NOT NULL, preference_version bigint NOT NULL DEFAULT 0, address_verified boolean NOT NULL,
 template_kind text NOT NULL, template_version bigint, channel_instance_id text,
 rendered_subject text NOT NULL DEFAULT '', rendered_body text NOT NULL DEFAULT '',
 outcome text NOT NULL CHECK(outcome IN ('queued','skipped')), reason text NOT NULL DEFAULT '',
 duplicate_of bigint REFERENCES ticket_notification_deliveries(id), email_outbox_id bigint REFERENCES email_outbox(id), sms_outbox_id bigint REFERENCES sms_outbox(id),
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(event_id,recipient_kind,recipient_id,channel)
);
CREATE UNIQUE INDEX ticket_notification_address ON ticket_notification_deliveries(event_id,channel,normalized_address) WHERE outcome='queued';
CREATE INDEX ticket_notification_ticket ON ticket_notification_events(ticket_id,id);
INSERT INTO notification_templates(channel,kind,name,subject,body,active) SELECT channel,kind,name,'工单 {{ticket_no}}：{{subject}}','{{author_name}}\n{{body}}\n{{ticket_url}}',false FROM (VALUES
 ('email','ticket_customer_created','客户新建工单'),('email','ticket_customer_replied','客户回复工单'),('email','ticket_staff_replied','客服回复工单'),
 ('sms','ticket_customer_created','客户新建工单'),('sms','ticket_customer_replied','客户回复工单'),('sms','ticket_staff_replied','客服回复工单')) AS v(channel,kind,name);
CREATE TABLE ticket_mutation_receipts (
 actor_id bigint NOT NULL REFERENCES users(id), idempotency_key text NOT NULL, operation text NOT NULL,
 ticket_id bigint NOT NULL REFERENCES tickets(id), request_hash text NOT NULL, result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(actor_id,operation,idempotency_key)
);
