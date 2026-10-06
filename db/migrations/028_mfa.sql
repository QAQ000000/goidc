ALTER TABLE users
  ADD COLUMN mfa_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN mfa_secret_enc bytea,
  ADD COLUMN mfa_confirmed_at timestamptz;

CREATE TABLE mfa_challenges (
  token_hash text PRIMARY KEY CHECK (token_hash ~ '^[0-9a-f]{64}$'),
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  attempts int NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mfa_challenges_expiry ON mfa_challenges(expires_at);
