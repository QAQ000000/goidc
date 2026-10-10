-- Financial and billing settings used by the administration settings pages.
-- Monetary values remain integer cents; exchange rates are metadata only until
-- multi-currency pricing is explicitly enabled by the commerce domain.
CREATE TABLE currencies (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  code text NOT NULL UNIQUE CHECK (code ~ '^[A-Z]{3}$'),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
  prefix text NOT NULL DEFAULT '' CHECK (char_length(prefix) <= 12),
  suffix text NOT NULL DEFAULT '' CHECK (char_length(suffix) <= 12),
  format text NOT NULL DEFAULT 'prefix_amount' CHECK (format IN ('prefix_amount','amount_suffix','code_amount')),
  exchange_rate numeric(20,8) NOT NULL DEFAULT 1 CHECK (exchange_rate > 0),
  active boolean NOT NULL DEFAULT true,
  is_default boolean NOT NULL DEFAULT false,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX currencies_one_default ON currencies(is_default) WHERE is_default;
INSERT INTO currencies(code,name,prefix,suffix,format,exchange_rate,active,is_default)
VALUES ('CNY','人民币','¥','元','prefix_amount',1,true,true)
ON CONFLICT (code) DO NOTHING;

CREATE TABLE finance_settings (
  id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1),
  topup_enabled boolean NOT NULL DEFAULT true,
  min_topup_cents bigint NOT NULL DEFAULT 100 CHECK (min_topup_cents >= 0),
  max_topup_cents bigint NOT NULL DEFAULT 100000000 CHECK (max_topup_cents >= 0),
  max_balance_cents bigint NOT NULL DEFAULT 0 CHECK (max_balance_cents >= 0),
  require_active_order boolean NOT NULL DEFAULT false,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (max_topup_cents >= min_topup_cents)
);
INSERT INTO finance_settings(id) VALUES (1) ON CONFLICT DO NOTHING;

CREATE TABLE invoice_settings (
  id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1),
  enabled boolean NOT NULL DEFAULT false,
  rate_bps integer NOT NULL DEFAULT 0 CHECK (rate_bps BETWEEN 0 AND 10000),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO invoice_settings(id) VALUES (1) ON CONFLICT DO NOTHING;

CREATE TABLE invoice_shipping_methods (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  price_cents bigint NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
  active boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE contract_settings (
  id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1),
  enabled boolean NOT NULL DEFAULT false,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO contract_settings(id) VALUES (1) ON CONFLICT DO NOTHING;

CREATE TABLE admin_setting_operations (
  actor_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  operation text NOT NULL CHECK (char_length(operation) BETWEEN 1 AND 80),
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
  request_hash text NOT NULL CHECK (char_length(request_hash)=64),
  result jsonb NOT NULL CHECK (jsonb_typeof(result)='object'),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(actor_id,operation,idempotency_key)
);
