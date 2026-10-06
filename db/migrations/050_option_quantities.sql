ALTER TABLE cart_items
  ADD COLUMN option_quantities jsonb NOT NULL DEFAULT '{}'::jsonb
  CHECK (jsonb_typeof(option_quantities) = 'object');

ALTER TABLE order_items
  ADD COLUMN option_quantities jsonb NOT NULL DEFAULT '{}'::jsonb
  CHECK (jsonb_typeof(option_quantities) = 'object');
