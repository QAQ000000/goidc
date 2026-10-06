ALTER TABLE users ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN legacy_id bigint;
ALTER TABLE users ADD COLUMN password_reset_required boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX users_legacy_identity ON users(legacy_source, legacy_id) WHERE legacy_source <> '' AND legacy_id IS NOT NULL;

ALTER TABLE products ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE products ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX products_legacy_identity ON products(legacy_source, legacy_id) WHERE legacy_source <> '' AND legacy_id IS NOT NULL;

ALTER TABLE orders ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX orders_legacy_identity ON orders(legacy_source, legacy_id) WHERE legacy_source <> '' AND legacy_id IS NOT NULL;

ALTER TABLE invoices ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE invoices ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX invoices_legacy_identity ON invoices(legacy_source, legacy_id) WHERE legacy_source <> '' AND legacy_id IS NOT NULL;

ALTER TABLE services ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX services_legacy_identity ON services(legacy_source, legacy_id) WHERE legacy_source <> '' AND legacy_id IS NOT NULL;

CREATE TABLE legacy_import_runs (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  source text NOT NULL,
  mode text NOT NULL CHECK (mode IN ('dry_run','apply')),
  started_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  status text NOT NULL DEFAULT 'running' CHECK (status IN ('running','succeeded','failed')),
  counts jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(counts) = 'object'),
  error text NOT NULL DEFAULT ''
);

CREATE TABLE legacy_id_map (
  source text NOT NULL,
  entity text NOT NULL,
  legacy_id bigint NOT NULL,
  target_id bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (source, entity, legacy_id)
);
CREATE INDEX legacy_id_map_target ON legacy_id_map(entity, target_id);
