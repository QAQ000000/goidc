CREATE TABLE payment_reconciliation_checks (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  payment_id bigint NOT NULL REFERENCES payment_transactions(id) ON DELETE RESTRICT,
  actor_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  status text NOT NULL DEFAULT 'checking' CHECK(status IN ('checking','pending','succeeded','error')),
  result_code text NOT NULL DEFAULT '',
  started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  lease_until timestamptz NOT NULL DEFAULT clock_timestamp()+interval '1 minute',
  completed_at timestamptz,
  CHECK ((status='checking')=(completed_at IS NULL))
);
CREATE INDEX payment_reconciliation_checks_payment ON payment_reconciliation_checks(payment_id,id DESC);
CREATE UNIQUE INDEX payment_reconciliation_checks_active ON payment_reconciliation_checks(payment_id) WHERE status='checking';
