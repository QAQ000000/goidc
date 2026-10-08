CREATE TABLE affiliate_programs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 name text NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 120),
 status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
 attribution_days integer NOT NULL DEFAULT 30 CHECK (attribution_days BETWEEN 1 AND 365),
 hold_days integer NOT NULL DEFAULT 7 CHECK (hold_days BETWEEN 0 AND 365),
 rule_version integer NOT NULL DEFAULT 1 CHECK (rule_version > 0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE affiliate_rule_sets (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 program_id bigint NOT NULL REFERENCES affiliate_programs(id) ON DELETE RESTRICT,
 version integer NOT NULL CHECK (version > 0),
 effective_from timestamptz NOT NULL DEFAULT now(),
 effective_to timestamptz,
 default_rules jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(default_rules)='object'),
 created_by bigint REFERENCES users(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK (effective_to IS NULL OR effective_to > effective_from),
 UNIQUE(program_id,version)
);
CREATE UNIQUE INDEX affiliate_rule_sets_current ON affiliate_rule_sets(program_id) WHERE effective_to IS NULL;

CREATE TABLE affiliate_product_rules (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 rule_set_id bigint NOT NULL REFERENCES affiliate_rule_sets(id) ON DELETE RESTRICT,
 product_id bigint NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
 event_type text NOT NULL CHECK (event_type IN ('first_purchase','renewal','reorder','upgrade')),
 mode text NOT NULL CHECK (mode IN ('disabled','inherit','override')),
 rate_bps integer NOT NULL DEFAULT 0 CHECK (rate_bps BETWEEN 0 AND 10000),
 amount_cents bigint NOT NULL DEFAULT 0 CHECK (amount_cents >= 0),
 max_cents bigint CHECK (max_cents IS NULL OR max_cents > 0),
 CHECK (rate_bps=0 OR amount_cents=0),
 UNIQUE(rule_set_id,product_id,event_type)
);

CREATE TABLE affiliate_tier_rules (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 rule_set_id bigint NOT NULL REFERENCES affiliate_rule_sets(id) ON DELETE RESTRICT,
 lower_cents bigint NOT NULL CHECK (lower_cents >= 0),
 upper_cents bigint CHECK (upper_cents IS NULL OR upper_cents > lower_cents),
 rate_bps integer NOT NULL DEFAULT 0 CHECK (rate_bps BETWEEN 0 AND 10000),
 amount_cents bigint NOT NULL DEFAULT 0 CHECK (amount_cents >= 0),
 CHECK (rate_bps=0 OR amount_cents=0),
 UNIQUE(rule_set_id,lower_cents)
);

CREATE TABLE affiliate_accounts (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id bigint NOT NULL UNIQUE REFERENCES users(id) ON DELETE RESTRICT,
 program_id bigint NOT NULL REFERENCES affiliate_programs(id) ON DELETE RESTRICT,
 account_type text NOT NULL DEFAULT 'referrer' CHECK (account_type IN ('referrer','agent')),
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','suspended','rejected','closed')),
 parent_account_id bigint REFERENCES affiliate_accounts(id) ON DELETE RESTRICT,
 level smallint NOT NULL DEFAULT 1 CHECK (level=1),
 approved_at timestamptz,
 version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (parent_account_id IS NULL)
);
CREATE INDEX affiliate_accounts_program_status ON affiliate_accounts(program_id,status,id);

CREATE TABLE affiliate_links (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account_id bigint NOT NULL REFERENCES affiliate_accounts(id) ON DELETE RESTRICT,
 code text NOT NULL UNIQUE CHECK (code ~ '^[A-Za-z0-9_-]{12,32}$'),
 landing_path text NOT NULL DEFAULT '/' CHECK (landing_path LIKE '/%' AND landing_path NOT LIKE '//%' AND position(E'\\n' in landing_path)=0 AND position(E'\\r' in landing_path)=0),
 product_id bigint REFERENCES products(id) ON DELETE SET NULL,
 note text NOT NULL DEFAULT '' CHECK (char_length(note)<=160),
 status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX affiliate_links_account ON affiliate_links(account_id,id DESC);

CREATE TABLE affiliate_clicks (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 link_id bigint NOT NULL REFERENCES affiliate_links(id) ON DELETE RESTRICT,
 session_hash text NOT NULL CHECK (session_hash ~ '^[0-9a-f]{64}$'),
 referrer text NOT NULL DEFAULT '' CHECK (char_length(referrer)<=2048),
 landing_path text NOT NULL DEFAULT '/' CHECK (char_length(landing_path)<=2048),
 client_version text NOT NULL DEFAULT '' CHECK (char_length(client_version)<=120),
 registered_user_id bigint REFERENCES users(id) ON DELETE SET NULL,
 converted_at timestamptz,
 click_day date NOT NULL DEFAULT CURRENT_DATE,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(link_id,session_hash,click_day),
 CHECK ((registered_user_id IS NULL)=(converted_at IS NULL))
);
CREATE UNIQUE INDEX affiliate_clicks_registered_user ON affiliate_clicks(registered_user_id) WHERE registered_user_id IS NOT NULL;
CREATE INDEX affiliate_clicks_link_created ON affiliate_clicks(link_id,created_at DESC);

CREATE TABLE affiliate_attributions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id bigint NOT NULL UNIQUE REFERENCES users(id) ON DELETE RESTRICT,
 account_id bigint NOT NULL REFERENCES affiliate_accounts(id) ON DELETE RESTRICT,
 link_id bigint NOT NULL REFERENCES affiliate_links(id) ON DELETE RESTRICT,
 click_id bigint NOT NULL UNIQUE REFERENCES affiliate_clicks(id) ON DELETE RESTRICT,
 status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','unassigned','review')),
 first_clicked_at timestamptz NOT NULL,
 registered_at timestamptz NOT NULL DEFAULT now(),
 source_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(source_snapshot)='object'),
 version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX affiliate_attributions_account ON affiliate_attributions(account_id,registered_at DESC,id DESC);

CREATE TABLE affiliate_commission_entries (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account_id bigint NOT NULL REFERENCES affiliate_accounts(id) ON DELETE RESTRICT,
 referred_user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 order_id bigint REFERENCES orders(id) ON DELETE RESTRICT,
 invoice_id bigint NOT NULL REFERENCES invoices(id) ON DELETE RESTRICT,
 order_item_id bigint REFERENCES order_items(id) ON DELETE RESTRICT,
 invoice_item_id bigint REFERENCES invoice_items(id) ON DELETE RESTRICT,
 payment_transaction_id bigint REFERENCES payment_transactions(id) ON DELETE RESTRICT,
 product_id bigint REFERENCES products(id) ON DELETE SET NULL,
 event_type text NOT NULL CHECK (event_type IN ('first_purchase','renewal','reorder','upgrade')),
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','available','reserved','paid','reversed','clawback')),
 amount_cents bigint NOT NULL CHECK (amount_cents > 0),
 basis_cents bigint NOT NULL CHECK (basis_cents > 0 AND amount_cents <= basis_cents),
 rate_bps integer NOT NULL CHECK (rate_bps BETWEEN 0 AND 10000),
 available_at timestamptz NOT NULL,
 rule_snapshot jsonb NOT NULL CHECK (jsonb_typeof(rule_snapshot)='object'),
 source_snapshot jsonb NOT NULL CHECK (jsonb_typeof(source_snapshot)='object'),
 idempotency_key text NOT NULL UNIQUE CHECK (char_length(idempotency_key) BETWEEN 8 AND 240),
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((order_item_id IS NULL) <> (invoice_item_id IS NULL))
);
CREATE INDEX affiliate_commission_account_status ON affiliate_commission_entries(account_id,status,available_at,id);
CREATE INDEX affiliate_commission_invoice ON affiliate_commission_entries(invoice_id,id);
CREATE UNIQUE INDEX affiliate_commission_order_item_event ON affiliate_commission_entries(order_item_id,event_type) WHERE order_item_id IS NOT NULL;
CREATE UNIQUE INDEX affiliate_commission_invoice_item_event ON affiliate_commission_entries(invoice_item_id,event_type) WHERE invoice_item_id IS NOT NULL;

CREATE TABLE affiliate_commission_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 entry_id bigint NOT NULL REFERENCES affiliate_commission_entries(id) ON DELETE RESTRICT,
 event_type text NOT NULL CHECK (event_type IN ('created','released','reversed','reserved','paid','clawback','adjusted')),
 delta_cents bigint NOT NULL,
 before_status text CHECK (before_status IS NULL OR before_status IN ('pending','available','reserved','paid','reversed','clawback')),
 after_status text NOT NULL CHECK (after_status IN ('pending','available','reserved','paid','reversed','clawback')),
 reason text NOT NULL DEFAULT '' CHECK (char_length(reason)<=1000),
 idempotency_key text NOT NULL UNIQUE CHECK (char_length(idempotency_key) BETWEEN 8 AND 240),
 actor_id bigint REFERENCES users(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX affiliate_commission_events_entry ON affiliate_commission_events(entry_id,id);

CREATE TABLE affiliate_audit_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account_id bigint REFERENCES affiliate_accounts(id) ON DELETE SET NULL,
 actor_id bigint REFERENCES users(id) ON DELETE SET NULL,
 event_type text NOT NULL CHECK (char_length(event_type) BETWEEN 1 AND 120),
 object_type text NOT NULL CHECK (char_length(object_type) BETWEEN 1 AND 80),
 object_id text NOT NULL CHECK (char_length(object_id) BETWEEN 1 AND 120),
 payload jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload)='object'),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX affiliate_audit_object ON affiliate_audit_events(object_type,object_id,id DESC);

CREATE TABLE affiliate_operations (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 operation text NOT NULL CHECK (char_length(operation) BETWEEN 1 AND 80),
 idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
 request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
 result jsonb NOT NULL CHECK (jsonb_typeof(result)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,operation,idempotency_key)
);

INSERT INTO affiliate_programs(name,status,attribution_days,hold_days)
VALUES('默认推广计划','active',30,7);
INSERT INTO affiliate_rule_sets(program_id,version,default_rules)
SELECT id,1,'{}'::jsonb FROM affiliate_programs WHERE name='默认推广计划';
