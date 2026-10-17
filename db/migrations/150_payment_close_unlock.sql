ALTER TABLE payment_transactions DROP CONSTRAINT payment_transactions_status_check;
ALTER TABLE payment_transactions ADD CONSTRAINT payment_transactions_status_check
 CHECK (status IN ('pending','expired','succeeded','failed','closed'));

CREATE TABLE payment_close_operations (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  payment_id bigint NOT NULL REFERENCES payment_transactions(id) ON DELETE RESTRICT,
  invoice_id bigint NOT NULL REFERENCES invoices(id) ON DELETE RESTRICT,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  actor_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  provider text NOT NULL,
  channel_instance_id text NOT NULL,
  credential_sha256 text NOT NULL CHECK(credential_sha256 ~ '^[0-9a-f]{64}$'),
  provider_payment_id text NOT NULL,
  amount_cents bigint NOT NULL CHECK (amount_cents > 0),
  expected_version bigint NOT NULL CHECK (expected_version > 0),
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
  request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
  operation_id text NOT NULL CHECK (char_length(trim(operation_id)) BETWEEN 16 AND 160),
  status text NOT NULL DEFAULT 'checking' CHECK (status IN ('checking','closed','succeeded','error','unknown')),
  result_code text NOT NULL DEFAULT '',
  external_transaction_id text NOT NULL DEFAULT '',
  result jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result)='object'),
  started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  lease_until timestamptz NOT NULL DEFAULT clock_timestamp()+interval '2 minutes',
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK ((status='checking')=(completed_at IS NULL))
);
CREATE UNIQUE INDEX payment_close_operations_actor_key ON payment_close_operations(actor_id,idempotency_key);
CREATE UNIQUE INDEX payment_close_operations_operation_id ON payment_close_operations(provider,operation_id);
CREATE UNIQUE INDEX payment_close_operations_active ON payment_close_operations(payment_id) WHERE status='checking';
CREATE INDEX payment_close_operations_payment ON payment_close_operations(payment_id,id DESC);

CREATE FUNCTION protect_payment_close_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.status<>'checking' THEN
  RAISE EXCEPTION 'payment close evidence is immutable' USING ERRCODE='23514';
 END IF;
 IF (to_jsonb(NEW)-ARRAY['status','result_code','external_transaction_id','result','completed_at','updated_at','lease_until']) <>
    (to_jsonb(OLD)-ARRAY['status','result_code','external_transaction_id','result','completed_at','updated_at','lease_until']) THEN
  RAISE EXCEPTION 'payment close evidence is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER payment_close_operation_immutable BEFORE UPDATE OR DELETE ON payment_close_operations
 FOR EACH ROW EXECUTE FUNCTION protect_payment_close_operation();

CREATE FUNCTION validate_payment_close_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id AND i.user_id=p.user_id
 WHERE p.id=NEW.payment_id AND p.invoice_id=NEW.invoice_id AND p.user_id=NEW.user_id AND p.provider=NEW.provider
 AND p.provider_payment_id=NEW.provider_payment_id AND p.amount_cents=NEW.amount_cents AND COALESCE(p.channel_instance_id,'')=NEW.channel_instance_id AND encode(sha256(p.credential_snapshot),'hex')=NEW.credential_sha256) THEN
  RAISE EXCEPTION 'payment close association mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER payment_close_operation_association BEFORE INSERT ON payment_close_operations
 FOR EACH ROW EXECUTE FUNCTION validate_payment_close_operation();

CREATE FUNCTION require_trusted_payment_close() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='closed' AND NOT EXISTS(SELECT 1 FROM payment_close_operations c WHERE c.payment_id=NEW.id
 AND c.invoice_id=NEW.invoice_id AND c.user_id=NEW.user_id AND c.provider=NEW.provider AND c.provider_payment_id=NEW.provider_payment_id
 AND c.amount_cents=NEW.amount_cents AND c.status='closed') THEN
  RAISE EXCEPTION 'payment close requires verified operation' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER payment_close_requires_operation AFTER INSERT OR UPDATE ON payment_transactions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION require_trusted_payment_close();

-- A verified success arriving after final close is money requiring review,
-- never an invoice settlement or a silent ignored notification.
CREATE TABLE payment_close_anomalies (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 payment_id bigint NOT NULL REFERENCES payment_transactions(id) ON DELETE RESTRICT,
 invoice_id bigint NOT NULL REFERENCES invoices(id) ON DELETE RESTRICT,
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 provider text NOT NULL,
 event_id text NOT NULL,
 amount_cents bigint NOT NULL CHECK(amount_cents>0),
 external_transaction_id text NOT NULL,
 payload_sha256 text NOT NULL CHECK(payload_sha256 ~ '^[0-9a-f]{64}$'),
 status text NOT NULL DEFAULT 'pending_verification' CHECK(status='pending_verification'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(provider,event_id)
);
CREATE INDEX payment_close_anomalies_invoice ON payment_close_anomalies(invoice_id,id DESC);
CREATE FUNCTION protect_payment_close_anomaly() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'payment close anomaly is immutable' USING ERRCODE='23514';
END $$;
CREATE TRIGGER payment_close_anomaly_immutable BEFORE UPDATE OR DELETE ON payment_close_anomalies
 FOR EACH ROW EXECUTE FUNCTION protect_payment_close_anomaly();

-- Cancelling a fully closed accepted topup keeps its original acceptance
-- timestamp/snapshot for audit, while ending its reserved capacity.
ALTER TABLE balance_topups DROP CONSTRAINT balance_topups_check;
ALTER TABLE balance_topups ADD CONSTRAINT balance_topups_acceptance_state_check CHECK
 ((status='created' AND accepted_at IS NULL) OR status='cancelled' OR
  (status IN ('accepted','credited','pending_liability') AND accepted_at IS NOT NULL));
