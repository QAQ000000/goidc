CREATE TABLE sms_settings (
 id smallint PRIMARY KEY CHECK(id=1), enabled boolean NOT NULL DEFAULT false,
 configured boolean NOT NULL DEFAULT false, provider text NOT NULL DEFAULT '',
 endpoint text NOT NULL DEFAULT '', token_encrypted bytea NOT NULL DEFAULT ''::bytea,
 version bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO sms_settings(id) VALUES(1);
CREATE TABLE sms_outbox (
 id bigserial PRIMARY KEY, recipient text NOT NULL, body_encrypted bytea NOT NULL,
 kind text NOT NULL, challenge_id text NOT NULL DEFAULT '', verification_id text REFERENCES verification_challenges(id), expires_at timestamptz NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sending','sent','expired','failed')),
 attempts integer NOT NULL DEFAULT 0, available_at timestamptz NOT NULL DEFAULT now(),
 locked_at timestamptz, last_error text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), sent_at timestamptz
);
CREATE INDEX sms_outbox_pending ON sms_outbox(available_at,id) WHERE status='pending';
