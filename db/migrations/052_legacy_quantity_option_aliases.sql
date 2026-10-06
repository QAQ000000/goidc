CREATE TABLE legacy_product_config_option_aliases (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  option_id bigint NOT NULL REFERENCES product_options(id) ON DELETE CASCADE,
  legacy_source text NOT NULL,
  legacy_id bigint NOT NULL,
  qty_minimum bigint NOT NULL DEFAULT 1 CHECK (qty_minimum >= 1),
  qty_maximum bigint NOT NULL DEFAULT 1000000 CHECK (qty_maximum >= qty_minimum),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(legacy_source, legacy_id)
);

CREATE INDEX legacy_product_config_option_aliases_option
  ON legacy_product_config_option_aliases(option_id);
