-- Gateway bodies may contain phone numbers or verification codes. They are
-- encrypted with the provision key and never included in notification DTOs.
CREATE TABLE sms_delivery_evidence (
 outbox_id bigint NOT NULL REFERENCES sms_outbox(id),
 attempts integer NOT NULL CHECK(attempts>0),
 locked_at timestamptz NOT NULL,
 provider text NOT NULL,
 http_status integer NOT NULL CHECK(http_status BETWEEN 0 AND 599),
 outcome text NOT NULL,
 response_encrypted bytea NOT NULL,
 response_truncated boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '7 days',
 PRIMARY KEY(outbox_id,attempts,locked_at)
);
CREATE INDEX sms_delivery_evidence_expiry ON sms_delivery_evidence(expires_at);
