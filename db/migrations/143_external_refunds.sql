-- Confirmed rows are the independent outgoing-money facts; original receipts remain immutable.
CREATE TABLE external_refunds (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 invoice_id bigint NOT NULL REFERENCES invoices(id), payment_id bigint NOT NULL REFERENCES payment_transactions(id),
 user_id bigint NOT NULL REFERENCES users(id), amount_cents bigint NOT NULL CHECK(amount_cents>0),
 status text NOT NULL DEFAULT 'pending_review' CHECK(status IN ('pending_review','confirmed','rejected')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 method text NOT NULL CHECK(method IN ('original_route','bank_transfer','other')),
 provider text NOT NULL, channel_instance_id text NOT NULL, provider_payment_id text NOT NULL,
 provider_refund_id text NOT NULL CHECK(char_length(trim(provider_refund_id)) BETWEEN 1 AND 200),
 received_at timestamptz NOT NULL, evidence_ref text NOT NULL CHECK(char_length(trim(evidence_ref)) BETWEEN 1 AND 500),
 evidence_sha256 text NOT NULL CHECK(evidence_sha256 ~ '^[0-9a-f]{64}$'), note text NOT NULL,
 requested_by bigint NOT NULL REFERENCES users(id), reviewed_by bigint REFERENCES users(id),
 review_note text NOT NULL DEFAULT '', confirmed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(reviewed_by IS NULL OR reviewed_by<>requested_by),
 CHECK((status='pending_review' AND reviewed_by IS NULL AND confirmed_at IS NULL) OR
 (status='confirmed' AND reviewed_by IS NOT NULL AND confirmed_at IS NOT NULL) OR
 (status='rejected' AND reviewed_by IS NOT NULL AND confirmed_at IS NULL)),
 UNIQUE(provider,channel_instance_id,provider_refund_id)
);
CREATE UNIQUE INDEX external_refunds_one_live_invoice ON external_refunds(invoice_id) WHERE status<>'rejected';
CREATE UNIQUE INDEX external_refunds_one_live_payment ON external_refunds(payment_id) WHERE status<>'rejected';
CREATE INDEX external_refunds_invoice ON external_refunds(invoice_id,id DESC);
CREATE TABLE external_refund_operations (
 actor_id bigint NOT NULL REFERENCES users(id), idempotency_key text NOT NULL CHECK(char_length(idempotency_key) BETWEEN 8 AND 128),
 invoice_id bigint NOT NULL REFERENCES invoices(id), refund_id bigint NOT NULL REFERENCES external_refunds(id),
 request_hash text NOT NULL, result jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(actor_id,idempotency_key)
);
CREATE FUNCTION protect_external_refund_fact() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.status<>'pending_review' THEN RAISE EXCEPTION 'external refund fact is immutable'; END IF;
 IF (to_jsonb(NEW)-ARRAY['status','version','reviewed_by','review_note','confirmed_at','updated_at']) <>
 (to_jsonb(OLD)-ARRAY['status','version','reviewed_by','review_note','confirmed_at','updated_at']) THEN
 RAISE EXCEPTION 'external refund evidence is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER external_refund_immutable BEFORE UPDATE OR DELETE ON external_refunds FOR EACH ROW EXECUTE FUNCTION protect_external_refund_fact();
