CREATE TABLE customer_credit_accounts (
  user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  limit_cents bigint NOT NULL DEFAULT 0 CHECK (limit_cents >= 0),
  used_cents bigint NOT NULL DEFAULT 0 CHECK (used_cents >= 0),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (used_cents <= limit_cents)
);

CREATE TABLE customer_credit_events (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  actor_id bigint NOT NULL REFERENCES users(id),
  event_type text NOT NULL CHECK (event_type = 'limit_adjustment'),
  delta_limit_cents bigint NOT NULL,
  limit_after_cents bigint NOT NULL CHECK (limit_after_cents >= 0),
  used_after_cents bigint NOT NULL CHECK (used_after_cents >= 0),
  expected_version bigint NOT NULL CHECK (expected_version > 0),
  version_after bigint NOT NULL CHECK (version_after > 0),
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(actor_id, idempotency_key)
);
CREATE INDEX customer_credit_events_user ON customer_credit_events(user_id, id DESC);

INSERT INTO customer_credit_accounts(user_id, limit_cents)
SELECT u.id, 0
FROM users u WHERE u.role='customer'
ON CONFLICT (user_id) DO NOTHING;

CREATE OR REPLACE FUNCTION create_customer_credit_account() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.role = 'customer' THEN
    INSERT INTO customer_credit_accounts(user_id, limit_cents)
    VALUES (NEW.id, 0)
    ON CONFLICT (user_id) DO NOTHING;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER users_customer_credit_account
AFTER INSERT ON users FOR EACH ROW EXECUTE FUNCTION create_customer_credit_account();
