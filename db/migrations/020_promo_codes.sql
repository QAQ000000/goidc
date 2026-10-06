CREATE TABLE promo_codes (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code text NOT NULL UNIQUE,
  discount_type text NOT NULL CHECK (discount_type IN ('percent', 'fixed', 'free')),
  value bigint NOT NULL DEFAULT 0 CHECK (value >= 0),
  cycles text[] NOT NULL DEFAULT '{}',
  applies_product_ids bigint[] NOT NULL DEFAULT '{}',
  requires_product_ids bigint[] NOT NULL DEFAULT '{}',
  requires_existing boolean NOT NULL DEFAULT false,
  starts_at timestamptz,
  expires_at timestamptz,
  max_uses bigint NOT NULL DEFAULT 0 CHECK (max_uses >= 0),
  used_count bigint NOT NULL DEFAULT 0 CHECK (used_count >= 0),
  one_time boolean NOT NULL DEFAULT false,
  only_new_customer boolean NOT NULL DEFAULT false,
  only_old_customer boolean NOT NULL DEFAULT false,
  once_per_customer boolean NOT NULL DEFAULT false,
  recurring boolean NOT NULL DEFAULT false,
  active boolean NOT NULL DEFAULT true,
  notes text NOT NULL DEFAULT '',
  created_by bigint REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX promo_codes_active_window ON promo_codes(active,starts_at,expires_at,id);

CREATE TABLE cart_promo_codes (
  cart_id uuid PRIMARY KEY REFERENCES carts(id) ON DELETE CASCADE,
  promo_code_id bigint NOT NULL REFERENCES promo_codes(id) ON DELETE RESTRICT,
  code text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE orders ADD COLUMN promo_code_id bigint REFERENCES promo_codes(id);
ALTER TABLE orders ADD COLUMN promo_code text NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN discount_cents bigint NOT NULL DEFAULT 0 CHECK (discount_cents >= 0);

CREATE TABLE promo_code_redemptions (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  promo_code_id bigint NOT NULL REFERENCES promo_codes(id) ON DELETE RESTRICT,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  order_id bigint NOT NULL UNIQUE REFERENCES orders(id) ON DELETE RESTRICT,
  discount_cents bigint NOT NULL CHECK (discount_cents >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(promo_code_id,user_id,order_id)
);
CREATE INDEX promo_code_redemptions_user ON promo_code_redemptions(user_id,promo_code_id,id);
