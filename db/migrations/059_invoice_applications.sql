CREATE TABLE invoice_profiles (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  profile_type text NOT NULL CHECK (profile_type IN ('individual','company')),
  title text NOT NULL,
  tax_number text NOT NULL DEFAULT '',
  bank_name text NOT NULL DEFAULT '',
  bank_account text NOT NULL DEFAULT '',
  company_address text NOT NULL DEFAULT '',
  phone text NOT NULL DEFAULT '',
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (char_length(trim(title)) BETWEEN 1 AND 160),
  CHECK (profile_type <> 'company' OR char_length(trim(tax_number)) BETWEEN 1 AND 80)
);
CREATE INDEX invoice_profiles_user ON invoice_profiles(user_id,id DESC);

CREATE TABLE invoice_addresses (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  recipient text NOT NULL,
  phone text NOT NULL,
  province text NOT NULL DEFAULT '',
  city text NOT NULL DEFAULT '',
  region text NOT NULL DEFAULT '',
  address text NOT NULL,
  postcode text NOT NULL DEFAULT '',
  is_default boolean NOT NULL DEFAULT false,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (char_length(trim(recipient)) BETWEEN 1 AND 80),
  CHECK (char_length(trim(phone)) BETWEEN 3 AND 80),
  CHECK (char_length(trim(address)) BETWEEN 1 AND 300)
);
CREATE INDEX invoice_addresses_user ON invoice_addresses(user_id,is_default DESC,id DESC);
CREATE UNIQUE INDEX invoice_addresses_one_default ON invoice_addresses(user_id) WHERE is_default;

CREATE TABLE invoice_applications (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  profile_id bigint NOT NULL REFERENCES invoice_profiles(id) ON DELETE RESTRICT,
  address_id bigint NOT NULL REFERENCES invoice_addresses(id) ON DELETE RESTRICT,
  profile_snapshot jsonb NOT NULL CHECK (jsonb_typeof(profile_snapshot)='object'),
  address_snapshot jsonb NOT NULL CHECK (jsonb_typeof(address_snapshot)='object'),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','rejected','issued')),
  amount_cents bigint NOT NULL CHECK (amount_cents BETWEEN 1 AND 10000000000),
  review_note text NOT NULL DEFAULT '',
  document_number text NOT NULL DEFAULT '',
  carrier text NOT NULL DEFAULT '',
  tracking_number text NOT NULL DEFAULT '',
  reviewer_id bigint REFERENCES users(id) ON DELETE SET NULL,
  reviewed_at timestamptz,
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (status<>'issued' OR char_length(trim(document_number)) BETWEEN 1 AND 120),
  CHECK (status<>'rejected' OR char_length(trim(review_note)) BETWEEN 1 AND 500)
);
CREATE INDEX invoice_applications_user ON invoice_applications(user_id,id DESC);
CREATE INDEX invoice_applications_status ON invoice_applications(status,id DESC);

CREATE TABLE invoice_application_items (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  application_id bigint NOT NULL REFERENCES invoice_applications(id) ON DELETE CASCADE,
  source_invoice_id bigint NOT NULL REFERENCES invoices(id) ON DELETE RESTRICT,
  invoice_no text NOT NULL,
  description text NOT NULL,
  amount_cents bigint NOT NULL CHECK (amount_cents > 0),
  UNIQUE(application_id,source_invoice_id)
);
CREATE INDEX invoice_application_items_source ON invoice_application_items(source_invoice_id);

CREATE TABLE invoice_application_operations (
  actor_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
  application_id bigint NOT NULL REFERENCES invoice_applications(id) ON DELETE CASCADE,
  action text NOT NULL CHECK (action IN ('create','review')),
  request_hash text NOT NULL,
  result jsonb NOT NULL CHECK (jsonb_typeof(result)='object'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(actor_id,idempotency_key)
);
