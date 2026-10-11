-- Sales assignment and commission configuration.  Amounts remain integer cents
-- and rates are basis points (10000 = 100%).
CREATE TABLE sales_settings (
  id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1),
  order_assignment text NOT NULL DEFAULT 'none' CHECK (order_assignment IN ('none','customer_choice','random','round_robin')),
  registration_assignment text NOT NULL DEFAULT 'none' CHECK (registration_assignment IN ('none','customer_choice','random','round_robin')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO sales_settings(id) VALUES (1) ON CONFLICT DO NOTHING;

CREATE TABLE sales_commission_groups (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 120),
  purchase_rate_bps integer NOT NULL DEFAULT 0 CHECK (purchase_rate_bps BETWEEN 0 AND 10000),
  renewal_rate_bps integer NOT NULL DEFAULT 0 CHECK (renewal_rate_bps BETWEEN 0 AND 10000),
  upgrade_rate_bps integer NOT NULL DEFAULT 0 CHECK (upgrade_rate_bps BETWEEN 0 AND 10000),
  include_renewal boolean NOT NULL DEFAULT true,
  include_upgrade boolean NOT NULL DEFAULT true,
  active boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sales_commission_groups_active ON sales_commission_groups(active,id);

CREATE TABLE sales_commission_tiers (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  threshold_cents bigint NOT NULL CHECK (threshold_cents >= 0),
  extra_rate_bps integer NOT NULL DEFAULT 0 CHECK (extra_rate_bps BETWEEN 0 AND 10000),
  active boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(threshold_cents)
);
CREATE INDEX sales_commission_tiers_active ON sales_commission_tiers(active,threshold_cents,id);

ALTER TABLE users ADD COLUMN sales_commission_group_id bigint REFERENCES sales_commission_groups(id) ON DELETE SET NULL;
CREATE INDEX users_sales_commission_group ON users(sales_commission_group_id) WHERE sales_commission_group_id IS NOT NULL;

-- Super administrators are also listed as sales staff in the admin console.
-- Keep the existing invariant while allowing that role to be assigned sales scope.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_sales_enabled_staff;
ALTER TABLE users ADD CONSTRAINT users_sales_enabled_staff CHECK (NOT sales_enabled OR role IN ('admin','super_admin'));
