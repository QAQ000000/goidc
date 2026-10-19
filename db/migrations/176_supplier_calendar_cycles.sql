-- Calendar-based supplier cycles; immutable source-cycle guards stay in force.
ALTER TABLE products DROP CONSTRAINT products_billing_cycle_check;
ALTER TABLE products ADD CONSTRAINT products_billing_cycle_check CHECK (billing_cycle IN ('monthly','quarterly','semiannually','annually','biennially','triennially','fourly','fively','sixly','sevenly','eightly','ninely','tenly','once'));
ALTER TABLE product_prices DROP CONSTRAINT product_prices_billing_cycle_check;
ALTER TABLE product_prices ADD CONSTRAINT product_prices_billing_cycle_check CHECK (billing_cycle IN ('monthly','quarterly','semiannually','annually','biennially','triennially','fourly','fively','sixly','sevenly','eightly','ninely','tenly','once'));
ALTER TABLE cart_items DROP CONSTRAINT cart_items_billing_cycle_check;
ALTER TABLE cart_items ADD CONSTRAINT cart_items_billing_cycle_check CHECK (billing_cycle IN ('monthly','quarterly','semiannually','annually','biennially','triennially','fourly','fively','sixly','sevenly','eightly','ninely','tenly','once'));
ALTER TABLE supplier_product_bindings DROP CONSTRAINT supplier_product_bindings_billing_cycle_check;
ALTER TABLE supplier_product_bindings ADD CONSTRAINT supplier_product_bindings_billing_cycle_check CHECK (billing_cycle IN ('monthly','quarterly','semiannually','annually','biennially','triennially','fourly','fively','sixly','sevenly','eightly','ninely','tenly','once'));
ALTER TABLE product_option_prices DROP CONSTRAINT product_option_prices_billing_cycle_check;
ALTER TABLE product_option_prices ADD CONSTRAINT product_option_prices_billing_cycle_check CHECK (billing_cycle IN ('hourly','daily','monthly','quarterly','semiannually','annually','biennially','triennially','fourly','fively','sixly','sevenly','eightly','ninely','tenly','once'));
ALTER TABLE product_option_price_tiers DROP CONSTRAINT product_option_price_tiers_billing_cycle_check;
ALTER TABLE product_option_price_tiers ADD CONSTRAINT product_option_price_tiers_billing_cycle_check CHECK (billing_cycle IN ('hourly','daily','monthly','quarterly','semiannually','annually','biennially','triennially','fourly','fively','sixly','sevenly','eightly','ninely','tenly','once'));

-- Volume quantity prices charge every unit at the selected range's rate;
-- historical native/progressive rows keep their existing formula.
ALTER TABLE product_option_price_tiers ADD COLUMN pricing_mode text NOT NULL DEFAULT 'graduated'
 CHECK (pricing_mode IN ('graduated','volume'));
