CREATE TABLE contract_templates (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 name text NOT NULL,
 content text NOT NULL,
 enabled boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE contracts (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 customer_id bigint NOT NULL REFERENCES users(id),
 service_id bigint NOT NULL REFERENCES services(id),
 template_id bigint NOT NULL REFERENCES contract_templates(id),
 template_name text NOT NULL,
 customer_name text NOT NULL,
 customer_email text NOT NULL,
 product_name text NOT NULL,
 service_name text NOT NULL,
 domain text NOT NULL,
 paid_at timestamptz NOT NULL,
 content text NOT NULL,
 party_snapshot jsonb NOT NULL,
 service_snapshot jsonb NOT NULL,
 status text NOT NULL DEFAULT 'draft' CHECK(status IN('draft','pending','approved','rejected','shipped','void')),
 signer_name text NOT NULL DEFAULT '',
 review_note text NOT NULL DEFAULT '',
 carrier text NOT NULL DEFAULT '',
 tracking_number text NOT NULL DEFAULT '',
 recipient text NOT NULL DEFAULT '',
 address text NOT NULL DEFAULT '',
 phone text NOT NULL DEFAULT '',
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX contracts_service_live ON contracts(service_id) WHERE status<>'void';
CREATE INDEX contracts_customer ON contracts(customer_id,id DESC);
CREATE TABLE contract_history (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 contract_id bigint NOT NULL REFERENCES contracts(id),
 actor_id bigint NOT NULL REFERENCES users(id),
 action text NOT NULL,
 before_status text NOT NULL,
 after_status text NOT NULL,
 note text NOT NULL DEFAULT '',
 version bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE contract_documents (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 contract_id bigint NOT NULL REFERENCES contracts(id),
 version bigint NOT NULL,
 content text NOT NULL,
 signer_name text NOT NULL,
 status text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(contract_id,version)
);
CREATE TABLE contract_receipts (
 actor_id bigint NOT NULL REFERENCES users(id),
 operation text NOT NULL,
 idempotency_key text NOT NULL,
 request_hash text NOT NULL,
 result jsonb NOT NULL,
 PRIMARY KEY(actor_id,operation,idempotency_key)
);
