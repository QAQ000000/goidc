ALTER TABLE invoices ADD COLUMN import_review_version bigint NOT NULL DEFAULT 1 CHECK (import_review_version > 0);
ALTER TABLE services ADD COLUMN import_review_version bigint NOT NULL DEFAULT 1 CHECK (import_review_version > 0);
ALTER TABLE services ADD COLUMN recurring_price_cents bigint CHECK (recurring_price_cents >= 0);

CREATE TABLE legacy_review_decisions (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  entity text NOT NULL CHECK (entity IN ('invoices','services')),
  target_id bigint NOT NULL,
  source text NOT NULL,
  legacy_id bigint NOT NULL,
  version bigint NOT NULL,
  decision text NOT NULL CHECK (decision IN ('archive','release')),
  actor_id bigint NOT NULL REFERENCES users(id),
  evidence text NOT NULL CHECK (length(evidence) BETWEEN 3 AND 2000),
  source_checksum text NOT NULL,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  result jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(entity,target_id,version),
  UNIQUE(actor_id,idempotency_key)
);
CREATE INDEX legacy_review_decisions_target ON legacy_review_decisions(entity,target_id,version DESC);
