-- Offline administrator collections are real receipt facts, not simulated
-- gateway callbacks. They remain linked to the one immutable payment row so
-- existing cash reports and external-refund admission keep one cash source.
CREATE TABLE manual_collections (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 payment_id bigint NOT NULL UNIQUE REFERENCES payment_transactions(id) ON DELETE RESTRICT,
 invoice_id bigint NOT NULL UNIQUE REFERENCES invoices(id) ON DELETE RESTRICT,
 service_id bigint NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 actor_id bigint NOT NULL REFERENCES account_principals(id) ON DELETE RESTRICT,
 amount_cents bigint NOT NULL CHECK(amount_cents>0),
 received_at timestamptz NOT NULL CHECK(isfinite(received_at)),
 method text NOT NULL CHECK(method IN ('bank_transfer','cash','other_offline')),
 receiving_account text NOT NULL CHECK(char_length(btrim(receiving_account)) BETWEEN 1 AND 120 AND receiving_account=btrim(receiving_account)),
 transaction_no text NOT NULL CHECK(char_length(btrim(transaction_no)) BETWEEN 1 AND 160 AND transaction_no=btrim(transaction_no)),
 evidence_ref text NOT NULL CHECK(char_length(btrim(evidence_ref)) BETWEEN 1 AND 500),
 evidence_sha256 text NOT NULL CHECK(evidence_sha256 ~ '^[0-9a-f]{64}$'),
 reason text NOT NULL CHECK(char_length(btrim(reason)) BETWEEN 1 AND 500),
 idempotency_key text NOT NULL CHECK(char_length(idempotency_key) BETWEEN 8 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 invoice_version bigint NOT NULL CHECK(invoice_version>0),
 service_version bigint NOT NULL CHECK(service_version>0),
 invoice_snapshot jsonb NOT NULL CHECK(jsonb_typeof(invoice_snapshot)='object'),
 service_snapshot jsonb NOT NULL CHECK(jsonb_typeof(service_snapshot)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(transaction_no)
);
CREATE INDEX manual_collections_customer ON manual_collections(user_id,received_at DESC,id DESC);
CREATE INDEX manual_collections_service ON manual_collections(service_id,id DESC);
CREATE INDEX manual_collection_late_receipt_source ON payment_close_anomalies(external_transaction_id)
 WHERE provider<>'mock' AND external_transaction_id<>'';

CREATE FUNCTION protect_manual_collection_fact() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'manual collection fact is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER manual_collection_immutable BEFORE UPDATE OR DELETE ON manual_collections
 FOR EACH ROW EXECUTE FUNCTION protect_manual_collection_fact();

CREATE FUNCTION guard_manual_collection_pair() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p payment_transactions; i invoices; c manual_collections; canonical_reference text;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'manual collection fact is immutable' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='manual_collections' THEN
  c:=NEW;
  SELECT * INTO p FROM payment_transactions WHERE id=c.payment_id;
  SELECT * INTO i FROM invoices WHERE id=c.invoice_id;
 ELSE
  p:=NEW;
  SELECT * INTO c FROM manual_collections WHERE payment_id=p.id;
  IF c.id IS NULL THEN RAISE EXCEPTION 'manual payment requires collection fact' USING ERRCODE='23514'; END IF;
  SELECT * INTO i FROM invoices WHERE id=p.invoice_id;
 END IF;
 canonical_reference:='manual:'||encode(sha256(convert_to(c.method,'UTF8')||decode('00','hex')||convert_to(c.receiving_account,'UTF8')),'hex')||':'||encode(sha256(convert_to(c.transaction_no,'UTF8')),'hex');
 IF p.id IS NULL OR p.provider<>'manual_offline' OR p.status<>'succeeded' OR p.invoice_id<>c.invoice_id OR p.user_id<>c.user_id OR p.amount_cents<>c.amount_cents OR p.external_transaction_id<>p.provider_payment_id OR p.provider_payment_id<>canonical_reference OR
    octet_length(p.credential_snapshot)<>0 OR p.channel_instance_id IS NOT NULL OR p.checkout_url<>'' OR p.next_reconcile_at IS NOT NULL OR
    p.completed_at IS DISTINCT FROM c.received_at OR i.paid_at IS DISTINCT FROM c.received_at OR c.received_at<i.created_at OR c.received_at>c.created_at OR c.received_at>clock_timestamp() OR
    i.id IS NULL OR i.user_id<>c.user_id OR i.amount_cents<>c.amount_cents OR i.status<>'paid' OR i.invoice_kind<>'automatic' THEN
  RAISE EXCEPTION 'manual collection payment pair invalid' USING ERRCODE='23514';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM services s WHERE s.id=c.service_id AND s.user_id=c.user_id AND EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id AND ii.item_type='renew' AND (SELECT count(*) FROM invoice_items WHERE invoice_id=i.id)=1)) THEN
  RAISE EXCEPTION 'manual collection service pair invalid' USING ERRCODE='23514';
 END IF;
 IF c.invoice_version<>i.version OR c.service_version<>(SELECT version FROM services WHERE id=c.service_id) OR
    c.invoice_snapshot<>external_refund_order_snapshot(i) OR c.service_snapshot<>external_refund_order_snapshot((SELECT s FROM services s WHERE s.id=c.service_id)) OR
    NOT EXISTS(SELECT 1 FROM administrators a WHERE a.id=c.actor_id AND a.status='active' AND a.role IN ('admin','super_admin')) OR
    NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.action='invoice.manual_collection.recorded' AND a.entity_type='invoice' AND a.entity_id=i.id::text AND a.payload->>'collection_id'=c.id::text AND a.actor_id=c.actor_id AND a.payload->>'request_hash'=c.request_hash AND a.payload->>'payment_id'=p.id::text AND a.payload->>'service_id'=c.service_id::text AND a.payload->>'customer_id'=c.user_id::text AND a.payload->>'amount_cents'=c.amount_cents::text AND (a.payload->>'received_at')::timestamptz=c.received_at AND a.payload->>'method'=c.method AND a.payload->>'reason'=c.reason AND a.payload->>'evidence_ref'=c.evidence_ref AND a.payload->>'evidence_sha256'=c.evidence_sha256) OR
    NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.action='service.renewal.manual_collected' AND a.entity_type='service' AND a.entity_id=c.service_id::text AND a.actor_id=c.actor_id AND a.payload->>'collection_id'=c.id::text AND a.payload->>'invoice_id'=i.id::text AND a.payload->>'payment_id'=p.id::text AND a.payload->>'request_hash'=c.request_hash AND a.payload->>'customer_id'=c.user_id::text AND a.payload->>'amount_cents'=c.amount_cents::text AND (a.payload->>'received_at')::timestamptz=c.received_at AND a.payload->>'method'=c.method AND a.payload->>'reason'=c.reason) OR
    NOT EXISTS(SELECT 1 FROM mutation_operations m WHERE m.actor_id=c.actor_id AND m.scope='admin:service.renewal.collect_manual' AND m.idempotency_key=c.idempotency_key AND m.request_hash=c.request_hash AND m.result->>'collection_id'=c.id::text AND m.result->>'payment_id'=p.id::text AND m.result->>'invoice_id'=i.id::text AND m.result->>'service_id'=c.service_id::text AND m.result->>'customer_id'=c.user_id::text AND m.result->>'amount_cents'=c.amount_cents::text AND m.result->>'invoice_version'=c.invoice_version::text AND m.result->>'service_version'=c.service_version::text AND (m.result->>'received_at')::timestamptz=c.received_at AND m.result->>'status'='paid') THEN
  RAISE EXCEPTION 'manual collection evidence incomplete' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER manual_collection_pair AFTER INSERT ON manual_collections
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_manual_collection_pair();
CREATE CONSTRAINT TRIGGER manual_payment_pair AFTER INSERT OR UPDATE ON payment_transactions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.provider='manual_offline') EXECUTE FUNCTION guard_manual_collection_pair();

CREATE FUNCTION guard_manual_payment_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.provider='manual_offline' AND (TG_OP='DELETE' OR NEW IS DISTINCT FROM OLD) THEN
  RAISE EXCEPTION 'manual payment fact is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER manual_payment_immutable BEFORE UPDATE OR DELETE ON payment_transactions
 FOR EACH ROW WHEN (OLD.provider='manual_offline') EXECUTE FUNCTION guard_manual_payment_mutation();

CREATE FUNCTION guard_manual_collection_transaction_collision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF current_setting('transaction_isolation')<>'read committed' THEN
  RAISE EXCEPTION 'manual receipt sources require read committed isolation' USING ERRCODE='23514';
 END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('refund-original:'||NEW.transaction_no,0));
 IF EXISTS(SELECT 1 FROM payment_transactions p WHERE p.id<>NEW.payment_id AND p.provider<>'mock' AND p.external_transaction_id=NEW.transaction_no)
 OR EXISTS(SELECT 1 FROM credit_repayments r WHERE r.receipt=NEW.transaction_no)
 OR EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.provider<>'mock' AND a.external_transaction_id=NEW.transaction_no)
 OR EXISTS(SELECT 1 FROM payment_close_anomaly_refunds r WHERE r.external_transaction_id=NEW.transaction_no AND r.status<>'rejected') THEN
  RAISE EXCEPTION 'manual collection transaction already occupied' USING ERRCODE='23505';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER manual_collection_transaction_collision BEFORE INSERT ON manual_collections
 FOR EACH ROW EXECUTE FUNCTION guard_manual_collection_transaction_collision();

-- Take the same raw-source lock in every direction. A namespace or account
-- alias cannot make one physical receipt both an online/offline payment or a
-- credit repayment. Keep original facts after refunds; their source stays used.
-- Re-read after lock waits must see the preceding claim's commit. Reject
-- snapshot isolation on source writes instead of silently accepting stale data.
CREATE FUNCTION guard_manual_collection_other_source() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE raw_source text;
BEGIN
 IF TG_TABLE_NAME='payment_transactions' THEN
  IF NEW.provider='mock' OR NEW.external_transaction_id='' THEN RETURN NEW; END IF;
  raw_source:=NEW.external_transaction_id;
 ELSIF TG_TABLE_NAME='credit_repayments' THEN
  raw_source:=NEW.receipt;
 ELSE
  IF NEW.status='rejected' THEN RETURN NEW; END IF;
  raw_source:=NEW.external_transaction_id;
 END IF;
 IF current_setting('transaction_isolation')<>'read committed' THEN
  RAISE EXCEPTION 'manual receipt sources require read committed isolation' USING ERRCODE='23514';
 END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('refund-original:'||raw_source,0));
 IF EXISTS(SELECT 1 FROM manual_collections c WHERE c.transaction_no=raw_source) THEN
  RAISE EXCEPTION 'source already belongs to manual collection' USING ERRCODE='23505';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER manual_collection_payment_source BEFORE INSERT OR UPDATE OF external_transaction_id,provider ON payment_transactions
 FOR EACH ROW EXECUTE FUNCTION guard_manual_collection_other_source();
CREATE TRIGGER manual_collection_credit_source BEFORE INSERT OR UPDATE OF receipt ON credit_repayments
 FOR EACH ROW EXECUTE FUNCTION guard_manual_collection_other_source();
CREATE TRIGGER manual_collection_anomaly_source BEFORE INSERT OR UPDATE ON payment_close_anomaly_refunds
 FOR EACH ROW EXECUTE FUNCTION guard_manual_collection_other_source();

-- A trusted late callback is evidence even if a manual claim already exists.
-- Keep it, while serializing arrival against new manual collections. Reports
-- use the manual raw source to keep ambiguous duplicates outside actual cash.
CREATE FUNCTION lock_manual_collection_late_receipt_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.provider='mock' OR NEW.external_transaction_id='' THEN RETURN NEW; END IF;
 IF current_setting('transaction_isolation')<>'read committed' THEN
  RAISE EXCEPTION 'manual receipt sources require read committed isolation' USING ERRCODE='23514';
 END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('refund-original:'||NEW.external_transaction_id,0));
 RETURN NEW;
END $$;
CREATE TRIGGER manual_collection_late_receipt_source BEFORE INSERT ON payment_close_anomalies
 FOR EACH ROW EXECUTE FUNCTION lock_manual_collection_late_receipt_source();
