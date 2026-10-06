CREATE TABLE mfa_recovery_codes (
  code_hash text PRIMARY KEY CHECK (code_hash ~ '^[0-9a-f]{64}$'),
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mfa_recovery_codes_user ON mfa_recovery_codes(user_id,used_at);
