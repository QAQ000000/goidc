-- Keep legacy conditional configuration rules available for later execution.
-- The current checkout intentionally does not evaluate these rules.
CREATE TABLE legacy_product_config_option_links (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  config_option_group_id bigint NOT NULL REFERENCES product_option_groups(id) ON DELETE CASCADE,
  sub_ids text NOT NULL DEFAULT '',
  relation text NOT NULL DEFAULT '',
  link_type text NOT NULL DEFAULT '',
  relation_legacy_id bigint NOT NULL DEFAULT 0,
  upstream_id bigint NOT NULL DEFAULT 0,
  legacy_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  legacy_source text NOT NULL,
  legacy_id bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(legacy_source, legacy_id)
);
CREATE INDEX legacy_product_config_option_links_group
  ON legacy_product_config_option_links(config_option_group_id);
