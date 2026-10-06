CREATE TABLE payment_transactions (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  provider text NOT NULL,
  provider_payment_id text NOT NULL,
  invoice_id bigint NOT NULL REFERENCES invoices(id),
  user_id bigint NOT NULL REFERENCES users(id),
  amount_cents bigint NOT NULL CHECK (amount_cents >= 0),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','succeeded','failed')),
  idempotency_key text NOT NULL DEFAULT '',
  expires_at timestamptz NOT NULL DEFAULT now() + interval '30 minutes',
  completed_at timestamptz,
  raw_result jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(raw_result) = 'object'),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(provider, provider_payment_id)
);
CREATE UNIQUE INDEX payment_transactions_user_idempotency
  ON payment_transactions(user_id, idempotency_key)
  WHERE idempotency_key <> '';
CREATE INDEX payment_transactions_invoice ON payment_transactions(invoice_id, id DESC);
CREATE INDEX payment_transactions_pending ON payment_transactions(status, expires_at);

CREATE TABLE payment_webhook_events (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  provider text NOT NULL,
  event_id text NOT NULL,
  payload_hash text NOT NULL,
  status text NOT NULL DEFAULT 'processed' CHECK (status IN ('processed','ignored','failed')),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(provider, event_id)
);
