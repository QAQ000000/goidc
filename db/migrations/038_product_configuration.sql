ALTER TABLE order_items ADD COLUMN config_snapshot jsonb NOT NULL DEFAULT '[]'::jsonb
 CHECK (jsonb_typeof(config_snapshot)='array');
-- Historical rows retain their original IDs and amounts. Current configuration
-- cannot prove which names or deltas were used when an earlier order was placed.
CREATE INDEX cart_items_option_ids ON cart_items USING gin(option_ids);
CREATE INDEX order_items_config_options ON order_items USING gin(config_options);
