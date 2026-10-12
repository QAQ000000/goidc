CREATE TABLE order_settings (
 id smallint PRIMARY KEY CHECK(id=1),
 require_verified_phone boolean NOT NULL DEFAULT false,
 require_certification boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO order_settings(id) VALUES(1);
ALTER TABLE products ADD COLUMN require_verified_phone boolean NOT NULL DEFAULT false,
 ADD COLUMN require_certification boolean NOT NULL DEFAULT false;
CREATE TABLE order_settings_receipts (
 actor_id bigint NOT NULL REFERENCES users(id), idempotency_key text NOT NULL,
 request_hash text NOT NULL, result jsonb NOT NULL,
 PRIMARY KEY(actor_id,idempotency_key)
);
