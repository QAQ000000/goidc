ALTER TABLE invoices DROP CONSTRAINT invoices_invoice_kind_check;
ALTER TABLE invoices ADD CONSTRAINT invoices_invoice_kind_check CHECK(invoice_kind IN ('automatic','manual','topup'));
ALTER TABLE invoices ADD CONSTRAINT topup_invoice_origin CHECK(invoice_kind<>'topup' OR (order_id IS NULL AND legacy_source='' AND legacy_id IS NULL AND NOT import_review_required));
CREATE TABLE balance_topups (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 invoice_id bigint NOT NULL UNIQUE REFERENCES invoices(id), user_id bigint NOT NULL REFERENCES users(id),
 amount_cents bigint NOT NULL CHECK(amount_cents BETWEEN 1 AND 10000000000),
 status text NOT NULL DEFAULT 'created' CHECK(status IN ('created','accepted','credited','pending_liability','cancelled')),
 settings_version bigint NOT NULL, accepted_settings jsonb, accepted_at timestamptz,
 idempotency_key text NOT NULL CHECK(char_length(idempotency_key) BETWEEN 8 AND 128), request_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(user_id,idempotency_key),
 CHECK((status IN ('created','cancelled') AND accepted_at IS NULL) OR (status IN ('accepted','credited','pending_liability') AND accepted_at IS NOT NULL))
);
CREATE TABLE balance_topup_settlements (
 topup_id bigint PRIMARY KEY REFERENCES balance_topups(id), invoice_id bigint NOT NULL UNIQUE REFERENCES invoices(id),
 payment_id bigint NOT NULL UNIQUE REFERENCES payment_transactions(id), user_id bigint NOT NULL REFERENCES users(id),
 amount_cents bigint NOT NULL CHECK(amount_cents>0), status text NOT NULL CHECK(status IN ('credited','pending_liability')),
 ledger_id bigint UNIQUE REFERENCES balance_ledger(id), failure_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK((status='credited' AND ledger_id IS NOT NULL AND failure_code='') OR (status='pending_liability' AND ledger_id IS NULL AND failure_code<>''))
);
CREATE UNIQUE INDEX balance_ledger_gateway_topup_invoice ON balance_ledger(reference_id) WHERE reference_type='gateway_topup' AND entry_type='topup';
CREATE INDEX balance_topups_reserved ON balance_topups(user_id) WHERE status='accepted';
CREATE FUNCTION immutable_topup_settlement() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'topup settlement fact is immutable'; END $$;
CREATE TRIGGER protect_topup_settlement BEFORE UPDATE OR DELETE ON balance_topup_settlements FOR EACH ROW EXECUTE FUNCTION immutable_topup_settlement();
CREATE TABLE balance_topup_operations (
 user_id bigint NOT NULL REFERENCES users(id), idempotency_key text NOT NULL CHECK(char_length(idempotency_key) BETWEEN 8 AND 128),
 request_hash text NOT NULL,result jsonb NOT NULL,PRIMARY KEY(user_id,idempotency_key)
);
