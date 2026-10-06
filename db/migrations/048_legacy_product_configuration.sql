-- Preserve the legacy product-configuration model while the public catalog uses
-- the normalized product_option_groups/product_options tables.
ALTER TABLE product_option_groups
  ADD COLUMN legacy_config_group_id bigint,
  ADD COLUMN option_type integer NOT NULL DEFAULT 0,
  ADD COLUMN qty_minimum bigint NOT NULL DEFAULT 0,
  ADD COLUMN qty_maximum bigint NOT NULL DEFAULT 0,
  ADD COLUMN qty_stage bigint NOT NULL DEFAULT 0,
  ADD COLUMN unit text NOT NULL DEFAULT '',
  ADD COLUMN notes text NOT NULL DEFAULT '',
  ADD COLUMN upgrade_enabled boolean NOT NULL DEFAULT true,
  ADD COLUMN legacy_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN legacy_source text NOT NULL DEFAULT '',
  ADD COLUMN legacy_id bigint;

ALTER TABLE product_options
  ADD COLUMN qty_minimum bigint NOT NULL DEFAULT 0,
  ADD COLUMN qty_maximum bigint NOT NULL DEFAULT 0,
  ADD COLUMN hidden boolean NOT NULL DEFAULT false,
  ADD COLUMN upgrade_enabled boolean NOT NULL DEFAULT true,
  ADD COLUMN legacy_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN legacy_source text NOT NULL DEFAULT '',
  ADD COLUMN legacy_id bigint;

CREATE TABLE legacy_product_config_groups (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  global_group boolean NOT NULL DEFAULT false,
  upstream_id bigint NOT NULL DEFAULT 0,
  legacy_source text NOT NULL,
  legacy_id bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(legacy_source, legacy_id)
);

CREATE TABLE legacy_product_config_links (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  config_group_id bigint NOT NULL REFERENCES legacy_product_config_groups(id) ON DELETE CASCADE,
  product_id bigint NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  legacy_source text NOT NULL,
  legacy_id bigint NOT NULL,
  UNIQUE(legacy_source, legacy_id)
);

ALTER TABLE product_option_groups
  ADD CONSTRAINT product_option_groups_legacy_group_fk
  FOREIGN KEY (legacy_config_group_id) REFERENCES legacy_product_config_groups(id);

CREATE UNIQUE INDEX product_option_groups_legacy_identity
  ON product_option_groups(legacy_source, legacy_id)
  WHERE legacy_source <> '' AND legacy_id IS NOT NULL;
CREATE UNIQUE INDEX product_options_legacy_identity
  ON product_options(legacy_source, legacy_id)
  WHERE legacy_source <> '' AND legacy_id IS NOT NULL;

CREATE TABLE product_option_prices (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  option_id bigint NOT NULL REFERENCES product_options(id) ON DELETE CASCADE,
  billing_cycle text NOT NULL CHECK (billing_cycle IN ('hourly','daily','monthly','quarterly','semiannually','annually','biennially','triennially','once')),
  price_cents bigint NOT NULL CHECK (price_cents >= 0),
  setup_fee_cents bigint NOT NULL CHECK (setup_fee_cents >= 0),
  active boolean NOT NULL DEFAULT true,
  legacy_source text NOT NULL DEFAULT '',
  legacy_id bigint,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(option_id, billing_cycle),
  UNIQUE(legacy_source, legacy_id, billing_cycle)
);
CREATE INDEX product_option_prices_lookup ON product_option_prices(option_id,billing_cycle,active);

CREATE TABLE service_option_selections (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  service_id bigint NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  group_id bigint NOT NULL REFERENCES product_option_groups(id),
  option_id bigint REFERENCES product_options(id),
  quantity bigint NOT NULL DEFAULT 0,
  legacy_source text NOT NULL,
  legacy_id bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(legacy_source, legacy_id),
  UNIQUE(service_id, group_id)
);
CREATE INDEX service_option_selections_service ON service_option_selections(service_id);

CREATE INDEX legacy_product_config_links_product ON legacy_product_config_links(product_id);
