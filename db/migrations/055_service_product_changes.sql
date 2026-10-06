CREATE TABLE product_upgrade_targets (
  product_id bigint NOT NULL REFERENCES products(id),
  target_product_id bigint NOT NULL REFERENCES products(id),
  allow_downgrade boolean NOT NULL DEFAULT false,
  PRIMARY KEY(product_id,target_product_id),
  CHECK(product_id<>target_product_id)
);
ALTER TABLE service_configuration_changes ADD COLUMN change_type text NOT NULL DEFAULT 'configuration'
 CHECK(change_type IN ('configuration','product'));
ALTER TABLE service_configuration_changes ADD COLUMN source_product_id bigint REFERENCES products(id);
ALTER TABLE service_configuration_changes ADD COLUMN target_product_id bigint REFERENCES products(id);
ALTER TABLE service_configuration_changes ADD COLUMN target_product_revision bigint;
ALTER TABLE service_configuration_changes ADD COLUMN target_product_name text;
ALTER TABLE service_configuration_changes ADD COLUMN target_stock_reserved boolean NOT NULL DEFAULT false;
ALTER TABLE service_configuration_changes ADD CONSTRAINT service_change_product_fields CHECK (
 (change_type='configuration' AND source_product_id IS NULL AND target_product_id IS NULL AND NOT target_stock_reserved)
 OR (change_type='product' AND source_product_id IS NOT NULL AND target_product_id IS NOT NULL
 AND source_product_id<>target_product_id AND target_product_revision IS NOT NULL AND target_product_revision>0 AND target_product_name IS NOT NULL)
);
