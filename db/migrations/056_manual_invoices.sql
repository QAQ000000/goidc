ALTER TABLE invoices ADD COLUMN invoice_kind text NOT NULL DEFAULT 'automatic'
 CHECK (invoice_kind IN ('automatic','manual')),
 ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
 ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
 ADD CONSTRAINT manual_invoice_origin CHECK (invoice_kind<>'manual' OR
 (order_id IS NULL AND legacy_source='' AND legacy_id IS NULL AND NOT import_review_required));

CREATE FUNCTION advance_invoice_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.version := OLD.version+1;
 NEW.updated_at := clock_timestamp();
 RETURN NEW;
END
$$;
CREATE TRIGGER invoice_version BEFORE UPDATE ON invoices
 FOR EACH ROW EXECUTE FUNCTION advance_invoice_version();

ALTER TABLE payment_transactions ADD COLUMN checkout_url text NOT NULL DEFAULT '';

CREATE TABLE manual_invoice_operations (
 actor_id bigint NOT NULL REFERENCES users(id),
 idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
 invoice_id bigint NOT NULL REFERENCES invoices(id),
 action text NOT NULL CHECK (action IN ('create','update','cancel')),
 request_hash text NOT NULL,
 result jsonb NOT NULL CHECK (jsonb_typeof(result)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor_id,idempotency_key)
);
