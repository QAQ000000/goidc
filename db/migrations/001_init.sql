CREATE TABLE schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now());

CREATE TABLE users (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  username text NOT NULL UNIQUE,
  email text NOT NULL UNIQUE,
  password_hash text NOT NULL,
  display_name text NOT NULL,
  role text NOT NULL DEFAULT 'customer' CHECK (role IN ('customer','admin','super_admin')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
  balance_cents bigint NOT NULL DEFAULT 0 CHECK (balance_cents >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE sessions (
  token_hash text PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user ON sessions(user_id);
CREATE INDEX sessions_expiry ON sessions(expires_at);

CREATE TABLE product_categories (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL,
  slug text NOT NULL UNIQUE,
  sort_order int NOT NULL DEFAULT 0,
  active boolean NOT NULL DEFAULT true
);
CREATE TABLE products (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  category_id bigint REFERENCES product_categories(id),
  name text NOT NULL,
  slug text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  product_type text NOT NULL DEFAULT 'service',
  billing_cycle text NOT NULL DEFAULT 'monthly' CHECK (billing_cycle IN ('monthly','quarterly','annually','once')),
  price_cents bigint NOT NULL CHECK (price_cents >= 0 AND price_cents <= 100000000),
  setup_fee_cents bigint NOT NULL DEFAULT 0 CHECK (setup_fee_cents >= 0 AND setup_fee_cents <= 100000000),
  stock int CHECK (stock >= 0),
  active boolean NOT NULL DEFAULT true,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
  revision bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX products_catalog ON products(active,category_id,id);

CREATE TABLE carts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id bigint NOT NULL UNIQUE REFERENCES users(id),
  version bigint NOT NULL DEFAULT 1,
  currency text NOT NULL DEFAULT 'CNY' CHECK (currency = 'CNY'),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE cart_items (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  cart_id uuid NOT NULL REFERENCES carts(id) ON DELETE CASCADE,
  product_id bigint NOT NULL REFERENCES products(id),
  quantity int NOT NULL CHECK (quantity BETWEEN 1 AND 100),
  product_revision bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(cart_id,product_id)
);

CREATE TABLE orders (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_no text NOT NULL UNIQUE,
  user_id bigint NOT NULL REFERENCES users(id),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paid','active','cancelled','refunded')),
  total_cents bigint NOT NULL CHECK (total_cents >= 0),
  currency text NOT NULL DEFAULT 'CNY' CHECK (currency = 'CNY'),
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(user_id,idempotency_key)
);
CREATE INDEX orders_user ON orders(user_id,id DESC);
CREATE TABLE order_items (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id bigint NOT NULL REFERENCES orders(id),
  product_id bigint NOT NULL REFERENCES products(id),
  product_name text NOT NULL,
  billing_cycle text NOT NULL,
  quantity int NOT NULL CHECK (quantity > 0),
  price_cents bigint NOT NULL CHECK (price_cents >= 0),
  setup_fee_cents bigint NOT NULL CHECK (setup_fee_cents >= 0),
  total_cents bigint NOT NULL CHECK (total_cents >= 0)
);
CREATE TABLE invoices (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  invoice_no text NOT NULL UNIQUE,
  user_id bigint NOT NULL REFERENCES users(id),
  order_id bigint NOT NULL UNIQUE REFERENCES orders(id),
  status text NOT NULL DEFAULT 'unpaid' CHECK (status IN ('unpaid','paid','cancelled','refunded')),
  amount_cents bigint NOT NULL CHECK (amount_cents >= 0),
  due_at timestamptz NOT NULL DEFAULT now() + interval '7 days',
  paid_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX invoices_user ON invoices(user_id,id DESC);

CREATE TABLE tickets (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_no text NOT NULL UNIQUE,
  user_id bigint NOT NULL REFERENCES users(id),
  subject text NOT NULL,
  body text NOT NULL,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','pending','resolved','closed')),
  priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low','normal','high','urgent')),
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tickets_user ON tickets(user_id,updated_at DESC,id DESC);
CREATE TABLE ticket_replies (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id bigint NOT NULL REFERENCES tickets(id),
  author_id bigint NOT NULL REFERENCES users(id),
  body text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_replies_thread ON ticket_replies(ticket_id,id);
CREATE TABLE audit_events (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  actor_id bigint REFERENCES users(id),
  action text NOT NULL,
  entity_type text NOT NULL,
  entity_id text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);
