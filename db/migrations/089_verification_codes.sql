ALTER TABLE site_settings ADD COLUMN email_verification_required boolean NOT NULL DEFAULT true;
ALTER TABLE users ADD COLUMN email_verified boolean NOT NULL DEFAULT false,
 ADD COLUMN phone text NOT NULL DEFAULT '' CHECK(phone='' OR phone ~ '^\+[1-9][0-9]{6,14}$'),
 ADD COLUMN phone_verified boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX users_verified_phone ON users(phone) WHERE phone_verified;

CREATE TABLE verification_challenges (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{64}$'),
 purpose text NOT NULL CHECK(purpose IN ('register','password_reset','bind_email','bind_phone')),
 channel text NOT NULL CHECK(channel IN ('email','sms')),
 recipient text NOT NULL, user_id bigint REFERENCES users(id) ON DELETE CASCADE,
 code_hash text NOT NULL CHECK(code_hash ~ '^[0-9a-f]{64}$'),
 attempts int NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
 expires_at timestamptz NOT NULL, consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX verification_recipient ON verification_challenges(purpose,channel,recipient,created_at);
CREATE INDEX verification_user_active ON verification_challenges(user_id) WHERE consumed_at IS NULL;
ALTER TABLE email_outbox ADD COLUMN body_encrypted bytea NOT NULL DEFAULT ''::bytea,
 ADD COLUMN expires_at timestamptz,
 ADD COLUMN verification_id text REFERENCES verification_challenges(id);
ALTER TABLE email_outbox DROP CONSTRAINT email_outbox_kind_check;
ALTER TABLE email_outbox ADD CONSTRAINT email_outbox_kind_check CHECK(kind ~ '^[a-z][a-z0-9_-]{0,79}$');
ALTER TABLE email_outbox DROP CONSTRAINT email_outbox_body_check;
ALTER TABLE email_outbox ADD CONSTRAINT email_outbox_body_check CHECK (
 (char_length(body) BETWEEN 1 AND 100000 AND octet_length(body_encrypted)=0)
 OR (body='' AND octet_length(body_encrypted)>0));
ALTER TABLE email_outbox DROP CONSTRAINT email_outbox_status_check;
ALTER TABLE email_outbox ADD CONSTRAINT email_outbox_status_check CHECK(status IN ('pending','sending','sent','expired'));
UPDATE password_reset_tokens SET consumed_at=now() WHERE consumed_at IS NULL;
UPDATE email_outbox SET status='expired',body='[expired]' WHERE kind='password_reset' AND status IN ('pending','sending');
UPDATE notification_templates SET body=E'您的验证码为：{{code}}\n有效期 {{expires_minutes}} 分钟。如非本人操作，请忽略。',subject='密码重置验证码' WHERE kind='password_reset';
INSERT INTO notification_templates(kind,name,subject,body) VALUES
 ('verification_code','身份验证验证码','身份验证验证码',E'您的验证码为：{{code}}\n有效期 {{expires_minutes}} 分钟。如非本人操作，请忽略。');

CREATE FUNCTION invalidate_user_verification() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.password_hash IS DISTINCT FROM OLD.password_hash OR NEW.email IS DISTINCT FROM OLD.email
 OR NEW.username IS DISTINCT FROM OLD.username OR NEW.status IS DISTINCT FROM OLD.status
 OR NEW.phone IS DISTINCT FROM OLD.phone THEN
  UPDATE verification_challenges SET consumed_at=clock_timestamp() WHERE user_id=NEW.id AND consumed_at IS NULL;
 END IF;
 IF NEW.email IS DISTINCT FROM OLD.email THEN NEW.email_verified=false; END IF;
 IF NEW.phone IS DISTINCT FROM OLD.phone THEN NEW.phone_verified=false; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER user_verification_invalidation BEFORE UPDATE ON users
 FOR EACH ROW EXECUTE FUNCTION invalidate_user_verification();
