CREATE TABLE payment_close_verification_requests (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 close_operation_id bigint NOT NULL REFERENCES payment_close_operations(id) ON DELETE RESTRICT,
 payment_id bigint NOT NULL REFERENCES payment_transactions(id) ON DELETE RESTRICT,
 invoice_id bigint NOT NULL REFERENCES invoices(id) ON DELETE RESTRICT,
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 actor_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 provider text NOT NULL, channel_instance_id text NOT NULL, provider_payment_id text NOT NULL,
 operation_id text NOT NULL, amount_cents bigint NOT NULL CHECK(amount_cents>0),
 credential_sha256 text NOT NULL CHECK(credential_sha256 ~ '^[0-9a-f]{64}$'),
 expected_version bigint NOT NULL CHECK(expected_version>0),
 idempotency_key text NOT NULL CHECK(char_length(idempotency_key) BETWEEN 8 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 lease_until timestamptz NOT NULL DEFAULT clock_timestamp()+interval '2 minutes',
 UNIQUE(actor_id,idempotency_key)
);
CREATE INDEX payment_close_verification_requests_payment ON payment_close_verification_requests(payment_id,id DESC);
CREATE TABLE payment_close_verification_results (
 request_id bigint PRIMARY KEY REFERENCES payment_close_verification_requests(id) ON DELETE RESTRICT,
 status text NOT NULL CHECK(status IN ('closed','succeeded','unknown','unsupported','error')),
 result_code text NOT NULL,
 external_transaction_id text NOT NULL DEFAULT '' CHECK(char_length(external_transaction_id)<=160),
 provider_evidence_id text NOT NULL DEFAULT '' CHECK(char_length(provider_evidence_id)<=160),
 result jsonb NOT NULL CHECK(jsonb_typeof(result)='object'),
 result_sha256 text NOT NULL CHECK(result_sha256 ~ '^[0-9a-f]{64}$'),
 received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(status<>'succeeded' OR char_length(trim(external_transaction_id))>0)
);
CREATE TABLE payment_close_verification_decisions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 request_id bigint NOT NULL UNIQUE REFERENCES payment_close_verification_results(request_id) ON DELETE RESTRICT,
 close_operation_id bigint NOT NULL REFERENCES payment_close_operations(id) ON DELETE RESTRICT,
 actor_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 status text NOT NULL CHECK(status IN ('applied_closed','applied_paid','already_paid','superseded_paid','rejected')),
 expected_version bigint NOT NULL CHECK(expected_version>0),
 invoice_version bigint NOT NULL CHECK(invoice_version>0),
 idempotency_key text NOT NULL CHECK(char_length(idempotency_key) BETWEEN 8 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 note text NOT NULL CHECK(char_length(trim(note)) BETWEEN 3 AND 1000),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(actor_id,idempotency_key)
);
CREATE UNIQUE INDEX payment_close_verification_applied ON payment_close_verification_decisions(close_operation_id) WHERE status<>'rejected';
CREATE FUNCTION protect_payment_close_verification_fact() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'payment close verification facts are immutable' USING ERRCODE='23514'; END $$;
CREATE TRIGGER payment_close_verification_requests_immutable BEFORE UPDATE OR DELETE ON payment_close_verification_requests FOR EACH ROW EXECUTE FUNCTION protect_payment_close_verification_fact();
CREATE TRIGGER payment_close_verification_results_immutable BEFORE UPDATE OR DELETE ON payment_close_verification_results FOR EACH ROW EXECUTE FUNCTION protect_payment_close_verification_fact();
CREATE TRIGGER payment_close_verification_decisions_immutable BEFORE UPDATE OR DELETE ON payment_close_verification_decisions FOR EACH ROW EXECUTE FUNCTION protect_payment_close_verification_fact();
CREATE FUNCTION validate_payment_close_verification_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM payment_close_operations c JOIN payment_transactions p ON p.id=c.payment_id
 WHERE c.id=NEW.close_operation_id AND c.payment_id=NEW.payment_id AND c.invoice_id=NEW.invoice_id AND c.user_id=NEW.user_id
 AND c.provider=NEW.provider AND c.channel_instance_id=NEW.channel_instance_id AND c.provider_payment_id=NEW.provider_payment_id
 AND c.operation_id=NEW.operation_id AND c.amount_cents=NEW.amount_cents AND c.credential_sha256=NEW.credential_sha256
 AND (c.status IN ('unknown','checking') OR (c.status='succeeded' AND c.result_code='provider_paid')) AND (c.status<>'checking' OR c.lease_until<=clock_timestamp())
 AND p.invoice_id=c.invoice_id AND p.user_id=c.user_id AND p.provider=c.provider AND p.provider_payment_id=c.provider_payment_id
 AND p.amount_cents=c.amount_cents AND COALESCE(p.channel_instance_id,'')=c.channel_instance_id
 AND encode(sha256(p.credential_snapshot),'hex')=c.credential_sha256 AND p.status IN ('pending','expired')) THEN
 RAISE EXCEPTION 'payment close verification association mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER payment_close_verification_request_association BEFORE INSERT ON payment_close_verification_requests FOR EACH ROW EXECUTE FUNCTION validate_payment_close_verification_request();
CREATE FUNCTION validate_payment_close_verification_result() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM payment_close_verification_requests r WHERE r.id=NEW.request_id AND r.lease_until>clock_timestamp()) THEN
 RAISE EXCEPTION 'payment close verification lease expired' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER payment_close_verification_result_association BEFORE INSERT ON payment_close_verification_results FOR EACH ROW EXECUTE FUNCTION validate_payment_close_verification_result();
CREATE FUNCTION validate_payment_close_verification_decision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM payment_close_verification_requests r JOIN payment_close_verification_results v ON v.request_id=r.id
 JOIN payment_close_operations c ON c.id=r.close_operation_id JOIN payment_transactions p ON p.id=r.payment_id
 WHERE r.id=NEW.request_id AND r.close_operation_id=NEW.close_operation_id AND NEW.actor_id<>r.actor_id AND NEW.actor_id<>c.actor_id
 AND r.payment_id=c.payment_id AND r.invoice_id=c.invoice_id AND r.user_id=c.user_id AND r.provider=c.provider
 AND r.provider_payment_id=c.provider_payment_id AND r.operation_id=c.operation_id AND r.amount_cents=c.amount_cents
 AND r.credential_sha256=c.credential_sha256 AND r.channel_instance_id=c.channel_instance_id
 AND p.invoice_id=r.invoice_id AND p.user_id=r.user_id AND p.provider=r.provider AND p.provider_payment_id=r.provider_payment_id
 AND p.amount_cents=r.amount_cents AND COALESCE(p.channel_instance_id,'')=r.channel_instance_id
 AND encode(sha256(p.credential_snapshot),'hex')=r.credential_sha256
 AND (NEW.status='rejected' OR (NEW.status='applied_closed' AND v.status='closed' AND p.status='closed')
 OR (NEW.status='superseded_paid' AND v.status='closed' AND p.status='succeeded' AND char_length(trim(p.external_transaction_id))>0)
 OR (NEW.status IN ('applied_paid','already_paid') AND v.status='succeeded' AND p.status='succeeded' AND p.external_transaction_id=v.external_transaction_id))) THEN
 RAISE EXCEPTION 'payment close verification decision mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER payment_close_verification_decision_association BEFORE INSERT ON payment_close_verification_decisions FOR EACH ROW EXECUTE FUNCTION validate_payment_close_verification_decision();
CREATE FUNCTION payment_close_operation_unresolved(operation_id bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM payment_close_operations c JOIN payment_transactions p ON p.id=c.payment_id
 WHERE c.id=$1 AND (c.status IN ('checking','unknown') OR (c.status='succeeded' AND c.result_code='provider_paid')) AND p.status IN ('pending','expired')
 AND NOT EXISTS(SELECT 1 FROM payment_close_verification_decisions d JOIN payment_close_verification_requests r ON r.id=d.request_id
 JOIN payment_close_verification_results v ON v.request_id=r.id
 WHERE d.close_operation_id=c.id AND d.status IN ('applied_closed','applied_paid','already_paid','superseded_paid')
 AND r.close_operation_id=c.id AND r.payment_id=c.payment_id AND r.invoice_id=c.invoice_id AND r.user_id=c.user_id
 AND r.provider=c.provider AND r.provider_payment_id=c.provider_payment_id AND r.operation_id=c.operation_id
 AND r.amount_cents=c.amount_cents AND r.channel_instance_id=c.channel_instance_id AND r.credential_sha256=c.credential_sha256));
$$;
CREATE OR REPLACE FUNCTION protect_payment_close_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.status<>'checking' OR OLD.lease_until<=clock_timestamp() THEN
  RAISE EXCEPTION 'payment close evidence is immutable' USING ERRCODE='23514'; END IF;
 IF (to_jsonb(NEW)-ARRAY['status','result_code','external_transaction_id','result','completed_at','updated_at','lease_until']) <>
    (to_jsonb(OLD)-ARRAY['status','result_code','external_transaction_id','result','completed_at','updated_at','lease_until']) THEN
  RAISE EXCEPTION 'payment close evidence is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION require_trusted_payment_close() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='closed' AND NOT EXISTS(SELECT 1 FROM payment_close_operations c
 WHERE c.payment_id=NEW.id AND c.invoice_id=NEW.invoice_id AND c.user_id=NEW.user_id AND c.provider=NEW.provider
 AND c.provider_payment_id=NEW.provider_payment_id AND c.amount_cents=NEW.amount_cents
 AND c.channel_instance_id=COALESCE(NEW.channel_instance_id,'') AND c.credential_sha256=encode(sha256(NEW.credential_snapshot),'hex')
 AND (c.status='closed' OR EXISTS(SELECT 1 FROM payment_close_verification_decisions d
 JOIN payment_close_verification_requests r ON r.id=d.request_id JOIN payment_close_verification_results v ON v.request_id=r.id
 WHERE d.close_operation_id=c.id AND d.status='applied_closed' AND v.status='closed'
 AND r.close_operation_id=c.id AND r.payment_id=c.payment_id AND r.invoice_id=c.invoice_id AND r.user_id=c.user_id
 AND r.provider=c.provider AND r.provider_payment_id=c.provider_payment_id AND r.operation_id=c.operation_id
 AND r.amount_cents=c.amount_cents AND r.channel_instance_id=c.channel_instance_id AND r.credential_sha256=c.credential_sha256))) THEN
 RAISE EXCEPTION 'payment close requires verified operation' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
