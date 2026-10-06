CREATE TABLE password_reset_tokens (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  requested_ip inet,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_reset_tokens_active ON password_reset_tokens(user_id,expires_at) WHERE consumed_at IS NULL;

CREATE TABLE email_outbox (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  recipient text NOT NULL CHECK (char_length(recipient) BETWEEN 3 AND 254),
  subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 240),
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 100000),
  kind text NOT NULL CHECK (kind IN ('password_reset','system')),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sending','sent')),
  attempts int NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  available_at timestamptz NOT NULL DEFAULT now(),
  locked_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz
);
CREATE INDEX email_outbox_pending ON email_outbox(status,available_at,id);
