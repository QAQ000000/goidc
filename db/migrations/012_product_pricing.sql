CREATE TABLE product_prices (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  product_id bigint NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  billing_cycle text NOT NULL CHECK (billing_cycle IN ('monthly','quarterly','annually','once')),
  price_cents bigint NOT NULL CHECK (price_cents >= 0 AND price_cents <= 100000000),
  setup_fee_cents bigint NOT NULL DEFAULT 0 CHECK (setup_fee_cents >= 0 AND setup_fee_cents <= 100000000),
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(product_id, billing_cycle)
);
INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents)
SELECT id,billing_cycle,price_cents,setup_fee_cents FROM products
ON CONFLICT(product_id,billing_cycle) DO NOTHING;

ALTER TABLE cart_items ADD COLUMN billing_cycle text;
UPDATE cart_items ci SET billing_cycle=p.billing_cycle FROM products p WHERE p.id=ci.product_id;
ALTER TABLE cart_items ALTER COLUMN billing_cycle SET NOT NULL;
ALTER TABLE cart_items DROP CONSTRAINT cart_items_cart_id_product_id_key;
ALTER TABLE cart_items ADD CONSTRAINT cart_items_billing_cycle_check CHECK (billing_cycle IN ('monthly','quarterly','annually','once'));
ALTER TABLE cart_items ADD CONSTRAINT cart_items_cart_product_cycle_key UNIQUE(cart_id,product_id,billing_cycle);

CREATE OR REPLACE FUNCTION seed_product_price() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents)
  VALUES(NEW.id,NEW.billing_cycle,NEW.price_cents,NEW.setup_fee_cents)
  ON CONFLICT(product_id,billing_cycle) DO NOTHING;
  RETURN NEW;
END $$;
CREATE TRIGGER products_seed_price AFTER INSERT ON products FOR EACH ROW EXECUTE FUNCTION seed_product_price();
CREATE INDEX product_prices_catalog ON product_prices(product_id,active,billing_cycle);
