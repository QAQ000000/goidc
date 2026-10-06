ALTER TABLE site_settings
 ADD COLUMN auth_captcha_enabled boolean NOT NULL DEFAULT false;

CREATE TABLE auth_captcha_challenges (
 id text PRIMARY KEY CHECK (length(id)=64),
 action text NOT NULL CHECK (action IN ('login','register','forgot-password')),
 answer_hash text NOT NULL CHECK (length(answer_hash)=64),
 nonce_hash text NOT NULL CHECK (length(nonce_hash)=64),
 issued_ip text NOT NULL CHECK (length(issued_ip)<=128),
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz
);
CREATE INDEX auth_captcha_challenges_expiry_idx ON auth_captcha_challenges(expires_at);
