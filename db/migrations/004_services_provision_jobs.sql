CREATE TABLE services (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id),
  order_id bigint NOT NULL REFERENCES orders(id),
  order_item_id bigint NOT NULL UNIQUE REFERENCES order_items(id),
  product_id bigint NOT NULL REFERENCES products(id),
  name text NOT NULL,
  domain text NOT NULL UNIQUE,
  billing_cycle text NOT NULL,
  status text NOT NULL DEFAULT 'pending_payment' CHECK (status IN ('pending_payment','queued','provisioning','active','suspended','cancelled','terminated','failed')),
  next_due_at timestamptz,
  username text NOT NULL DEFAULT '',
  upstream_id text NOT NULL DEFAULT '',
  last_error text NOT NULL DEFAULT '',
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX services_user ON services(user_id, id DESC);
CREATE INDEX services_order ON services(order_id);
CREATE INDEX services_status ON services(status, id);

CREATE TABLE provision_jobs (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  service_id bigint NOT NULL REFERENCES services(id) ON DELETE CASCADE,
  action text NOT NULL CHECK (action IN ('create','suspend','resume','terminate','sync')),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','succeeded','failed','cancelled')),
  idempotency_key text NOT NULL UNIQUE,
  attempts int NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  available_at timestamptz NOT NULL DEFAULT now(),
  locked_at timestamptz,
  completed_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  result jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result) = 'object'),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX provision_jobs_ready ON provision_jobs(status, available_at, id);
CREATE INDEX provision_jobs_service ON provision_jobs(service_id, id DESC);
