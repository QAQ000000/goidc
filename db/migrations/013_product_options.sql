CREATE TABLE product_option_groups (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  product_id bigint NOT NULL REFERENCES products(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  slug text NOT NULL CHECK (slug ~ '^[a-z][a-z0-9-]{0,119}$'),
  required boolean NOT NULL DEFAULT false,
  sort_order int NOT NULL DEFAULT 0,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(product_id, slug)
);
CREATE TABLE product_options (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  group_id bigint NOT NULL REFERENCES product_option_groups(id) ON DELETE CASCADE,
  label text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 120),
  value text NOT NULL CHECK (char_length(value) BETWEEN 1 AND 120),
  price_delta_cents bigint NOT NULL DEFAULT 0 CHECK (price_delta_cents BETWEEN -100000000 AND 100000000),
  setup_delta_cents bigint NOT NULL DEFAULT 0 CHECK (setup_delta_cents BETWEEN -100000000 AND 100000000),
  sort_order int NOT NULL DEFAULT 0,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(group_id, value)
);
CREATE INDEX product_option_groups_product ON product_option_groups(product_id,active,sort_order,id);
CREATE INDEX product_options_group ON product_options(group_id,active,sort_order,id);

ALTER TABLE cart_items ADD COLUMN option_ids jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(option_ids) = 'array');
ALTER TABLE order_items ADD COLUMN config_options jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(config_options) = 'array');
