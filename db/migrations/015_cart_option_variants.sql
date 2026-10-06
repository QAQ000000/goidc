ALTER TABLE cart_items ADD COLUMN option_key text NOT NULL DEFAULT '';
UPDATE cart_items SET option_key=md5(option_ids::text);
ALTER TABLE cart_items DROP CONSTRAINT cart_items_cart_product_cycle_key;
ALTER TABLE cart_items ADD CONSTRAINT cart_items_cart_product_cycle_option_key UNIQUE(cart_id,product_id,billing_cycle,option_key);
CREATE INDEX cart_items_option_key ON cart_items(cart_id,product_id,billing_cycle,option_key);
