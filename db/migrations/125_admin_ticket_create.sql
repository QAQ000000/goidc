ALTER TABLE tickets
 ADD COLUMN created_by bigint REFERENCES users(id),
 ADD COLUMN cc jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(cc)='array'),
 ADD COLUMN send_initial_email boolean NOT NULL DEFAULT false,
 ADD COLUMN initial_email_status text NOT NULL DEFAULT 'not_requested'
   CHECK (initial_email_status IN ('not_requested','queued','skipped_unverified','skipped_channel','skipped_template'));

CREATE TABLE admin_ticket_creation_receipts (
 actor_id bigint NOT NULL REFERENCES users(id),
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
 customer_id bigint NOT NULL REFERENCES users(id),
 ticket_id bigint NOT NULL REFERENCES tickets(id),
 request_hash text NOT NULL,
 result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (actor_id,idempotency_key)
);

CREATE FUNCTION admin_ticket_customer_allowed(actor bigint, owner bigint) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN EXISTS(SELECT 1 FROM users WHERE id=owner AND role='customer')
   THEN admin_customer_allowed(actor,owner)
   ELSE admin_customer_owner_allowed(actor,NULL)
 END
$$;

CREATE FUNCTION admin_ticket_access_allowed(actor bigint, owner bigint, department bigint) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT admin_ticket_department_allowed(actor,department) AND admin_ticket_customer_allowed(actor,owner)
$$;

INSERT INTO notification_templates(channel,kind,name,subject,body)
VALUES('email','ticket_created','工单创建通知','工单 {{ticket_no}} 已创建','您的工单 {{ticket_no}} 已创建。\n主题：{{subject}}\n{{body}}')
ON CONFLICT(channel,kind) DO NOTHING;
