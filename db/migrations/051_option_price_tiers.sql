CREATE TABLE product_option_price_tiers (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  option_id bigint NOT NULL REFERENCES product_options(id) ON DELETE CASCADE,
  billing_cycle text NOT NULL CHECK (billing_cycle IN ('hourly','daily','monthly','quarterly','semiannually','annually','biennially','triennially','once')),
  qty_minimum bigint NOT NULL CHECK (qty_minimum >= 1),
  qty_maximum bigint NOT NULL CHECK (qty_maximum >= qty_minimum),
  price_cents bigint NOT NULL CHECK (price_cents >= 0),
  setup_fee_cents bigint NOT NULL CHECK (setup_fee_cents >= 0),
  active boolean NOT NULL DEFAULT true,
  legacy_source text NOT NULL DEFAULT '',
  legacy_id bigint,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(option_id, billing_cycle, qty_minimum, qty_maximum)
);

CREATE INDEX product_option_price_tiers_lookup
  ON product_option_price_tiers(option_id, billing_cycle, active, qty_minimum, qty_maximum);
