CREATE TABLE legacy_import_issues (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  run_id bigint NOT NULL REFERENCES legacy_import_runs(id) ON DELETE CASCADE,
  entity text NOT NULL,
  legacy_id bigint NOT NULL,
  code text NOT NULL,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(detail) = 'object'),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX legacy_import_issues_run ON legacy_import_issues(run_id, entity, legacy_id);

-- Renewal and balance invoices need not belong to a purchase order.
ALTER TABLE invoices ALTER COLUMN order_id DROP NOT NULL;
ALTER TABLE invoices ADD COLUMN import_review_required boolean NOT NULL DEFAULT false;
ALTER TABLE services ADD COLUMN import_review_required boolean NOT NULL DEFAULT false;
ALTER TABLE order_items ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE order_items ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX order_items_legacy_identity ON order_items(legacy_source,legacy_id)
  WHERE legacy_source <> '' AND legacy_id IS NOT NULL;
CREATE TABLE invoice_items (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  invoice_id bigint NOT NULL REFERENCES invoices(id),
  description text NOT NULL,
  item_type text NOT NULL DEFAULT 'service',
  amount_cents bigint NOT NULL,
  legacy_source text NOT NULL DEFAULT '',
  legacy_id bigint,
  UNIQUE(legacy_source,legacy_id)
);

-- Curated source records contain no passwords, payment credentials or tokens.
-- Keep the source state alongside mapped rows; never "repair" historical money.
CREATE TABLE legacy_source_records (
  source text NOT NULL,
  entity text NOT NULL,
  legacy_id bigint NOT NULL,
  run_id bigint NOT NULL REFERENCES legacy_import_runs(id),
  payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
  checksum text NOT NULL,
  PRIMARY KEY(source,entity,legacy_id)
);
