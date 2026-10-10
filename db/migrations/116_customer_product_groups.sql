-- Product groups used by customer-specific pricing rules. These are distinct
-- from the public catalog sections/categories.
CREATE TABLE customer_product_groups (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 120),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE customer_product_group_products (
  group_id bigint NOT NULL REFERENCES customer_product_groups(id) ON DELETE CASCADE,
  product_id bigint NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, product_id)
);
CREATE INDEX customer_product_group_products_product ON customer_product_group_products(product_id, group_id);

CREATE TABLE customer_product_group_discounts (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  customer_group_id bigint NOT NULL REFERENCES customer_groups(id) ON DELETE CASCADE,
  product_group_id bigint NOT NULL REFERENCES customer_product_groups(id) ON DELETE CASCADE,
  discount_type text NOT NULL CHECK (discount_type IN ('percent','fixed')),
  discount_percent integer NOT NULL DEFAULT 0 CHECK (discount_percent BETWEEN 0 AND 100),
  fixed_discount_cents bigint NOT NULL DEFAULT 0 CHECK (fixed_discount_cents >= 0),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (customer_group_id, product_group_id),
  CHECK ((discount_type = 'percent' AND discount_percent > 0 AND fixed_discount_cents = 0)
      OR (discount_type = 'fixed' AND fixed_discount_cents > 0 AND discount_percent = 0))
);
CREATE INDEX customer_product_group_discounts_page ON customer_product_group_discounts(product_group_id, customer_group_id);
