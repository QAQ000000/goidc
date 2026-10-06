CREATE TABLE customer_groups (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL UNIQUE,
  color text NOT NULL DEFAULT '#64748b' CHECK (color ~ '^#[0-9a-fA-F]{6}$'),
  discount_percent int NOT NULL DEFAULT 0 CHECK (discount_percent BETWEEN 0 AND 100),
  suspend_delete_exempt boolean NOT NULL DEFAULT false,
  separate_invoices boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE customer_group_members (
  customer_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  group_id bigint NOT NULL REFERENCES customer_groups(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(customer_id, group_id)
);
CREATE INDEX customer_group_members_group ON customer_group_members(group_id, customer_id);

CREATE TABLE customer_contacts (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  customer_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  username text NOT NULL DEFAULT '' CHECK (char_length(username) <= 120),
  sex smallint NOT NULL DEFAULT 0 CHECK (sex IN (0, 1, 2)),
  avatar text NOT NULL DEFAULT '',
  company_name text NOT NULL DEFAULT '',
  email text NOT NULL,
  wechat_id text NOT NULL DEFAULT '',
  country text NOT NULL DEFAULT '',
  province text NOT NULL DEFAULT '',
  city text NOT NULL DEFAULT '',
  region text NOT NULL DEFAULT '',
  address1 text NOT NULL DEFAULT '',
  address2 text NOT NULL DEFAULT '',
  postcode text NOT NULL DEFAULT '',
  phone_number text NOT NULL DEFAULT '',
  permissions text[] NOT NULL DEFAULT '{}',
  general_emails boolean NOT NULL DEFAULT false,
  invoice_emails boolean NOT NULL DEFAULT false,
  product_emails boolean NOT NULL DEFAULT false,
  support_emails boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'inactive' CHECK (status IN ('active', 'inactive', 'disabled')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX customer_contacts_email_lower ON customer_contacts(lower(email));
CREATE INDEX customer_contacts_customer ON customer_contacts(customer_id, id DESC);
