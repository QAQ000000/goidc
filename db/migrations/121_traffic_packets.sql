-- Native flow-package catalog and purchase snapshots.  These records are
-- intentionally separate from services: a package consumes traffic on an
-- existing service and must not create a second provisioned service.
CREATE TABLE traffic_products (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
  capacity_gb bigint NOT NULL CHECK (capacity_gb > 0 AND capacity_gb <= 1000000000),
  price_cents bigint NOT NULL CHECK (price_cents > 0 AND price_cents <= 100000000000),
  active boolean NOT NULL DEFAULT true,
  stock bigint CHECK (stock IS NULL OR stock >= 0),
  sold_count bigint NOT NULL DEFAULT 0 CHECK (sold_count >= 0),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX traffic_products_listing ON traffic_products(active, id DESC);

CREATE TABLE traffic_product_products (
  traffic_product_id bigint NOT NULL REFERENCES traffic_products(id) ON DELETE CASCADE,
  product_id bigint NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
  PRIMARY KEY (traffic_product_id, product_id)
);
CREATE INDEX traffic_product_products_product ON traffic_product_products(product_id, traffic_product_id);

CREATE TABLE traffic_orders (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_no text NOT NULL UNIQUE,
  user_id bigint NOT NULL REFERENCES users(id),
  traffic_product_id bigint NOT NULL REFERENCES traffic_products(id) ON DELETE RESTRICT,
  service_id bigint NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
  invoice_id bigint NOT NULL UNIQUE REFERENCES invoices(id) ON DELETE RESTRICT,
  product_name text NOT NULL,
  capacity_gb bigint NOT NULL CHECK (capacity_gb > 0),
  amount_cents bigint NOT NULL CHECK (amount_cents >= 0),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paid','cancelled','refunded')),
  fulfillment_status text NOT NULL DEFAULT 'pending_payment' CHECK (fulfillment_status IN ('pending_payment','waiting_manual','fulfilled','cancelled')),
  fulfillment_note text NOT NULL DEFAULT '',
  fulfilled_at timestamptz,
  payment_method text NOT NULL DEFAULT '',
  idempotency_key text NOT NULL DEFAULT '',
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX traffic_orders_user_idempotency ON traffic_orders(user_id,idempotency_key)
  WHERE idempotency_key <> '';
CREATE INDEX traffic_orders_listing ON traffic_orders(created_at DESC, id DESC);
CREATE INDEX traffic_orders_user ON traffic_orders(user_id, id DESC);
