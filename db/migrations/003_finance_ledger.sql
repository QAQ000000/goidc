CREATE TABLE balance_ledger (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id),
  entry_type text NOT NULL CHECK (entry_type IN ('topup','payment','refund','adjustment')),
  delta_cents bigint NOT NULL CHECK (delta_cents <> 0),
  balance_after bigint NOT NULL CHECK (balance_after >= 0),
  reference_type text NOT NULL DEFAULT '',
  reference_id bigint,
  idempotency_key text,
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(user_id, entry_type, idempotency_key)
);
CREATE INDEX balance_ledger_user ON balance_ledger(user_id, id DESC);
CREATE INDEX balance_ledger_reference ON balance_ledger(reference_type, reference_id);
