-- Marketing push campaigns. Audience rules are stored as a validated snapshot so
-- a later send cannot silently change the set of customers selected by an admin.
-- Keep compatibility with installations that still have the pre-verification
-- email kind constraint; marketing is a normal, non-secret outbox payload.
ALTER TABLE email_outbox DROP CONSTRAINT IF EXISTS email_outbox_kind_check;
ALTER TABLE email_outbox ADD CONSTRAINT email_outbox_kind_check CHECK (kind ~ '^[a-z][a-z0-9_-]{0,79}$');

CREATE TABLE marketing_campaigns (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 160),
  mode text NOT NULL CHECK (mode IN ('customer','product')),
  channel text NOT NULL CHECK (channel IN ('email','sms','inbox')),
  subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 240),
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 100000),
  audience jsonb NOT NULL CHECK (jsonb_typeof(audience)='object'),
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','queued','completed','cancelled','failed')),
  recipient_count integer NOT NULL DEFAULT 0 CHECK (recipient_count >= 0),
  created_by bigint NOT NULL REFERENCES users(id),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz
);
CREATE INDEX marketing_campaigns_created ON marketing_campaigns(created_at DESC,id DESC);
CREATE INDEX marketing_campaigns_status ON marketing_campaigns(status,created_at DESC,id DESC);

CREATE TABLE marketing_campaign_recipients (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  campaign_id bigint NOT NULL REFERENCES marketing_campaigns(id) ON DELETE CASCADE,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  channel text NOT NULL CHECK (channel IN ('email','sms','inbox')),
  email_outbox_id bigint REFERENCES email_outbox(id) ON DELETE SET NULL,
  sms_outbox_id bigint REFERENCES sms_outbox(id) ON DELETE SET NULL,
  inbox_message_id bigint REFERENCES admin_inbox_messages(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(campaign_id,user_id,channel)
);
CREATE INDEX marketing_campaign_recipients_campaign ON marketing_campaign_recipients(campaign_id,id);

CREATE TABLE marketing_campaign_operations (
  actor_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
  request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
  result jsonb NOT NULL CHECK (jsonb_typeof(result)='object'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(actor_id,idempotency_key)
);
