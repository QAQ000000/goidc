CREATE TABLE product_purchase_fields (
  product_id bigint NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
  field_key text NOT NULL CHECK (field_key ~ '^[a-z][a-z0-9_]{0,39}$'),
  label text NOT NULL CHECK (length(label) BETWEEN 1 AND 120),
  field_type text NOT NULL CHECK (field_type IN ('text','textarea','select','checkbox','url')),
  description text NOT NULL DEFAULT '',
  required boolean NOT NULL DEFAULT false,
  active boolean NOT NULL DEFAULT true,
  show_invoice boolean NOT NULL DEFAULT false,
  max_length integer NOT NULL DEFAULT 255 CHECK (max_length BETWEEN 1 AND 2048),
  validation_pattern text NOT NULL DEFAULT '',
  choices text[] NOT NULL DEFAULT '{}',
  sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 1000000),
  PRIMARY KEY(product_id,field_key)
);
ALTER TABLE cart_items ADD COLUMN purchase_fields jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(purchase_fields)='object');
ALTER TABLE order_items ADD COLUMN purchase_fields jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(purchase_fields)='array');
