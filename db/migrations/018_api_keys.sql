CREATE TABLE api_keys (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  prefix text NOT NULL UNIQUE CHECK (prefix ~ '^zjmfgo_[a-f0-9]{16}$'),
  secret_hash text NOT NULL UNIQUE CHECK (secret_hash ~ '^[a-f0-9]{64}$'),
  last_used_at timestamptz,
  expires_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX api_keys_user ON api_keys(user_id,created_at DESC);
CREATE INDEX api_keys_active ON api_keys(secret_hash) WHERE revoked_at IS NULL;
