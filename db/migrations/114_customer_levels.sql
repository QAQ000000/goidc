CREATE TABLE customer_levels (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 120),
  min_income_cents bigint NOT NULL DEFAULT 0 CHECK (min_income_cents >= 0),
  min_purchase_count integer NOT NULL DEFAULT 0 CHECK (min_purchase_count >= 0),
  min_login_count integer NOT NULL DEFAULT 0 CHECK (min_login_count >= 0),
  min_recent_login_count integer NOT NULL DEFAULT 0 CHECK (min_recent_login_count >= 0),
  min_renewal_count integer NOT NULL DEFAULT 0 CHECK (min_renewal_count >= 0),
  min_recent_renewal_count integer NOT NULL DEFAULT 0 CHECK (min_recent_renewal_count >= 0),
  sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN -100000 AND 100000),
  active boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX customer_levels_listing ON customer_levels(active, sort_order, id);
