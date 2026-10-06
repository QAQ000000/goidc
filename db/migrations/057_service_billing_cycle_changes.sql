ALTER TABLE service_configuration_changes DROP CONSTRAINT service_change_product_fields;
ALTER TABLE service_configuration_changes DROP CONSTRAINT service_configuration_changes_change_type_check;
ALTER TABLE service_configuration_changes ADD COLUMN new_billing_cycle text NOT NULL DEFAULT 'monthly';
UPDATE service_configuration_changes SET new_billing_cycle=billing_cycle;
ALTER TABLE service_configuration_changes ADD CONSTRAINT service_configuration_changes_new_cycle_check
 CHECK (new_billing_cycle IN ('monthly','quarterly','annually','once'));
ALTER TABLE service_configuration_changes ADD CONSTRAINT service_configuration_changes_change_type_check
 CHECK(change_type IN ('configuration','product','billing_cycle'));
ALTER TABLE service_configuration_changes ADD CONSTRAINT service_change_product_fields CHECK (
 (change_type='configuration' AND source_product_id IS NULL AND target_product_id IS NULL AND NOT target_stock_reserved AND new_billing_cycle=billing_cycle)
 OR (change_type='billing_cycle' AND source_product_id IS NULL AND target_product_id IS NULL AND NOT target_stock_reserved AND new_billing_cycle<>billing_cycle)
 OR (change_type='product' AND source_product_id IS NOT NULL AND target_product_id IS NOT NULL
 AND source_product_id<>target_product_id AND target_product_revision IS NOT NULL AND target_product_revision>0 AND target_product_name IS NOT NULL
 AND new_billing_cycle=billing_cycle)
);
