-- Late receipts are liabilities outside ordinary sales. Never rewrite their events.
CREATE VIEW payment_close_anomaly_groups AS
 SELECT min(a.id) AS anchor_anomaly_id,max(a.id) AS group_version,a.payment_id,a.invoice_id,a.user_id,
 a.provider,a.external_transaction_id,count(*)::bigint AS event_count,min(a.created_at) AS first_seen,max(a.created_at) AS last_seen,
 p.amount_cents,bool_and(a.amount_cents=p.amount_cents) AS amount_consistent,
 encode(sha256(p.credential_snapshot),'hex') AS credential_sha256
 FROM payment_close_anomalies a JOIN payment_transactions p ON p.id=a.payment_id
 GROUP BY a.payment_id,a.invoice_id,a.user_id,a.provider,a.external_transaction_id,p.amount_cents,p.credential_snapshot;

-- A one-time non-secret merchant identity attestation from the original encrypted snapshot.
CREATE TABLE payment_close_anomaly_merchant_bindings (
 payment_id bigint NOT NULL REFERENCES payment_transactions(id) ON DELETE RESTRICT,
 credential_sha256 text NOT NULL CHECK(credential_sha256 ~ '^[0-9a-f]{64}$'),
 merchant_scope text NOT NULL CHECK(merchant_scope='mock:test-only' OR merchant_scope ~ '^epay:[0-9a-f]{64}$'),
 provider text NOT NULL,provider_payment_id text NOT NULL,channel_instance_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(payment_id,credential_sha256)
);
CREATE FUNCTION protect_anomaly_merchant_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'merchant binding immutable' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM payment_transactions p WHERE p.id=NEW.payment_id AND p.status='closed' AND p.provider=NEW.provider
 AND p.provider_payment_id=NEW.provider_payment_id AND coalesce(p.channel_instance_id,'')=NEW.channel_instance_id
 AND encode(sha256(p.credential_snapshot),'hex')=NEW.credential_sha256)
 OR (NEW.provider='mock' AND NEW.merchant_scope<>'mock:test-only') OR (NEW.provider<>'mock' AND NEW.merchant_scope='mock:test-only') THEN
 RAISE EXCEPTION 'merchant binding association mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER anomaly_merchant_binding_immutable BEFORE INSERT OR UPDATE OR DELETE ON payment_close_anomaly_merchant_bindings FOR EACH ROW EXECUTE FUNCTION protect_anomaly_merchant_binding();

CREATE TABLE payment_close_anomaly_refunds (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 anchor_anomaly_id bigint NOT NULL REFERENCES payment_close_anomalies(id) ON DELETE RESTRICT,
 payment_id bigint NOT NULL REFERENCES payment_transactions(id) ON DELETE RESTRICT,
 invoice_id bigint NOT NULL REFERENCES invoices(id) ON DELETE RESTRICT,
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 provider text NOT NULL,channel_instance_id text NOT NULL,provider_payment_id text NOT NULL,
 merchant_scope text NOT NULL CHECK(merchant_scope='mock:test-only' OR merchant_scope ~ '^epay:[0-9a-f]{64}$'),
 credential_sha256 text NOT NULL CHECK(credential_sha256 ~ '^[0-9a-f]{64}$'),
 external_transaction_id text NOT NULL CHECK(char_length(external_transaction_id) BETWEEN 1 AND 200 AND external_transaction_id=btrim(external_transaction_id)),
 amount_cents bigint NOT NULL CHECK(amount_cents>0),expected_group_version bigint NOT NULL CHECK(expected_group_version>0),
 status text NOT NULL DEFAULT 'pending_review' CHECK(status IN ('pending_review','confirmed','rejected')),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 method text NOT NULL CHECK(method IN ('original_route','bank_transfer','other')),
 provider_refund_id text NOT NULL CHECK(char_length(btrim(provider_refund_id)) BETWEEN 1 AND 200 AND provider_refund_id=btrim(provider_refund_id)),
 received_at timestamptz NOT NULL,evidence_ref text NOT NULL CHECK(char_length(btrim(evidence_ref)) BETWEEN 1 AND 500),
 evidence_sha256 text NOT NULL CHECK(evidence_sha256 ~ '^[0-9a-f]{64}$'),note text NOT NULL,
 requested_by bigint NOT NULL REFERENCES users(id),reviewed_by bigint REFERENCES users(id),review_note text NOT NULL DEFAULT '',
 reviewed_group_version bigint,confirmed_at timestamptz,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(reviewed_by IS NULL OR reviewed_by<>requested_by),
 CHECK((status='pending_review' AND reviewed_by IS NULL AND confirmed_at IS NULL AND reviewed_group_version IS NULL) OR
 (status='confirmed' AND reviewed_by IS NOT NULL AND confirmed_at IS NOT NULL AND reviewed_group_version IS NOT NULL AND reviewed_group_version>0) OR
 (status='rejected' AND reviewed_by IS NOT NULL AND confirmed_at IS NULL AND reviewed_group_version IS NOT NULL AND reviewed_group_version>0))
);
CREATE UNIQUE INDEX payment_close_anomaly_refunds_live_group ON payment_close_anomaly_refunds(payment_id,merchant_scope,external_transaction_id) WHERE status<>'rejected';
CREATE INDEX payment_close_anomaly_refunds_invoice ON payment_close_anomaly_refunds(invoice_id,id DESC);
CREATE FUNCTION protect_payment_close_anomaly_refund() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.status<>'pending_review' THEN RAISE EXCEPTION 'anomaly refund is immutable' USING ERRCODE='23514'; END IF;
 IF (to_jsonb(NEW)-ARRAY['status','version','reviewed_by','review_note','reviewed_group_version','confirmed_at','updated_at']) <>
 (to_jsonb(OLD)-ARRAY['status','version','reviewed_by','review_note','reviewed_group_version','confirmed_at','updated_at']) THEN
 RAISE EXCEPTION 'anomaly refund evidence is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER payment_close_anomaly_refund_immutable BEFORE UPDATE OR DELETE ON payment_close_anomaly_refunds FOR EACH ROW EXECUTE FUNCTION protect_payment_close_anomaly_refund();
CREATE FUNCTION validate_payment_close_anomaly_refund() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM payment_close_anomaly_groups g JOIN payment_transactions p ON p.id=g.payment_id
 WHERE g.anchor_anomaly_id=NEW.anchor_anomaly_id AND g.payment_id=NEW.payment_id AND g.invoice_id=NEW.invoice_id AND g.user_id=NEW.user_id
 AND g.provider=NEW.provider AND p.provider_payment_id=NEW.provider_payment_id AND coalesce(p.channel_instance_id,'')=NEW.channel_instance_id
 AND p.status='closed' AND g.external_transaction_id=NEW.external_transaction_id AND g.amount_cents=NEW.amount_cents AND g.amount_consistent
 AND (NEW.status='rejected' OR g.credential_sha256=NEW.credential_sha256) AND NEW.received_at>=p.created_at
 AND EXISTS(SELECT 1 FROM payment_close_anomaly_merchant_bindings b WHERE b.payment_id=NEW.payment_id AND b.credential_sha256=NEW.credential_sha256 AND b.merchant_scope=NEW.merchant_scope AND b.provider=NEW.provider AND b.provider_payment_id=NEW.provider_payment_id AND b.channel_instance_id=NEW.channel_instance_id)
 AND (TG_OP<>'INSERT' OR g.group_version=NEW.expected_group_version)
 AND (NEW.status='pending_review' OR g.group_version=NEW.reviewed_group_version)) THEN
 RAISE EXCEPTION 'anomaly refund association mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER payment_close_anomaly_refund_association BEFORE INSERT OR UPDATE ON payment_close_anomaly_refunds FOR EACH ROW EXECUTE FUNCTION validate_payment_close_anomaly_refund();
CREATE TABLE payment_close_anomaly_refund_operations (
 actor_id bigint NOT NULL REFERENCES users(id),idempotency_key text NOT NULL CHECK(char_length(idempotency_key) BETWEEN 8 AND 128),
 invoice_id bigint NOT NULL REFERENCES invoices(id),refund_id bigint NOT NULL REFERENCES payment_close_anomaly_refunds(id),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),result jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(actor_id,idempotency_key)
);

-- Shared database-enforced refund identity, including the pre-existing workflow.
CREATE TABLE external_refund_payout_claims (
 source_kind text NOT NULL CHECK(source_kind IN ('external_refund','payment_anomaly_refund')),source_id bigint NOT NULL,
 merchant_scope text NOT NULL,provider_refund_id text NOT NULL,
 status text NOT NULL CHECK(status IN ('reserved','confirmed','released')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(source_kind,source_id)
);
CREATE UNIQUE INDEX external_refund_payout_claims_live_identity ON external_refund_payout_claims(merchant_scope,provider_refund_id) WHERE status<>'released';
INSERT INTO external_refund_payout_claims(source_kind,source_id,merchant_scope,provider_refund_id,status)
 SELECT 'external_refund',id,merchant_scope,provider_refund_id,CASE status WHEN 'pending_review' THEN 'reserved' WHEN 'confirmed' THEN 'confirmed' ELSE 'released' END FROM external_refunds;
CREATE FUNCTION sync_external_refund_payout_claim() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE kind text;
BEGIN
 kind:=CASE TG_TABLE_NAME WHEN 'external_refunds' THEN 'external_refund' ELSE 'payment_anomaly_refund' END;
 INSERT INTO external_refund_payout_claims(source_kind,source_id,merchant_scope,provider_refund_id,status)
 VALUES(kind,NEW.id,NEW.merchant_scope,NEW.provider_refund_id,CASE NEW.status WHEN 'pending_review' THEN 'reserved' WHEN 'confirmed' THEN 'confirmed' ELSE 'released' END)
 ON CONFLICT(source_kind,source_id) DO UPDATE SET status=excluded.status,updated_at=clock_timestamp();
 RETURN NEW;
END $$;
CREATE TRIGGER external_refund_global_payout AFTER INSERT OR UPDATE ON external_refunds FOR EACH ROW EXECUTE FUNCTION sync_external_refund_payout_claim();
CREATE TRIGGER anomaly_refund_global_payout AFTER INSERT OR UPDATE ON payment_close_anomaly_refunds FOR EACH ROW EXECUTE FUNCTION sync_external_refund_payout_claim();
CREATE FUNCTION protect_external_refund_payout_claim() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE valid boolean;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'payout claim immutable' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' AND (OLD.status<>'reserved' OR (to_jsonb(NEW)-ARRAY['status','updated_at'])<>(to_jsonb(OLD)-ARRAY['status','updated_at'])) THEN
 RAISE EXCEPTION 'payout claim immutable' USING ERRCODE='23514'; END IF;
 IF NEW.source_kind='external_refund' THEN
 SELECT EXISTS(SELECT 1 FROM external_refunds r WHERE r.id=NEW.source_id AND r.merchant_scope=NEW.merchant_scope AND r.provider_refund_id=NEW.provider_refund_id AND NEW.status=CASE r.status WHEN 'pending_review' THEN 'reserved' WHEN 'confirmed' THEN 'confirmed' ELSE 'released' END) INTO valid;
 ELSE
 SELECT EXISTS(SELECT 1 FROM payment_close_anomaly_refunds r WHERE r.id=NEW.source_id AND r.merchant_scope=NEW.merchant_scope AND r.provider_refund_id=NEW.provider_refund_id AND NEW.status=CASE r.status WHEN 'pending_review' THEN 'reserved' WHEN 'confirmed' THEN 'confirmed' ELSE 'released' END) INTO valid;
 END IF;
 IF NOT valid THEN RAISE EXCEPTION 'payout claim association mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER external_refund_payout_claim_immutable BEFORE INSERT OR UPDATE OR DELETE ON external_refund_payout_claims FOR EACH ROW EXECUTE FUNCTION protect_external_refund_payout_claim();

CREATE FUNCTION payment_close_anomaly_unresolved(anomaly_id bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM payment_close_anomalies a JOIN payment_transactions p ON p.id=a.payment_id WHERE a.id=anomaly_id
 AND NOT EXISTS(SELECT 1 FROM payment_close_anomaly_refunds r WHERE r.status='confirmed'
 AND r.payment_id=a.payment_id AND r.invoice_id=a.invoice_id AND r.user_id=a.user_id AND r.provider=a.provider
 AND r.external_transaction_id=a.external_transaction_id AND a.external_transaction_id<>'' AND r.amount_cents=a.amount_cents
 AND r.amount_cents=p.amount_cents AND r.credential_sha256=encode(sha256(p.credential_snapshot),'hex')
 AND r.provider_payment_id=p.provider_payment_id AND r.channel_instance_id=coalesce(p.channel_instance_id,'')
 AND EXISTS(SELECT 1 FROM payment_close_anomaly_merchant_bindings b WHERE b.payment_id=r.payment_id AND b.credential_sha256=r.credential_sha256 AND b.merchant_scope=r.merchant_scope AND b.provider=r.provider AND b.provider_payment_id=r.provider_payment_id AND b.channel_instance_id=r.channel_instance_id)))
$$;

-- Serialize provenance claims across both workflows, independently of payout ID.
CREATE UNIQUE INDEX anomaly_refunds_live_original_transaction ON payment_close_anomaly_refunds(merchant_scope,external_transaction_id) WHERE status<>'rejected';
CREATE FUNCTION guard_refund_original_transaction() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE original_tx text;
BEGIN
 IF NEW.status='rejected' THEN RETURN NEW; END IF;
 original_tx:=CASE WHEN TG_TABLE_NAME='external_refunds' THEN to_jsonb(NEW)->>'original_transaction_id' ELSE to_jsonb(NEW)->>'external_transaction_id' END;
 IF original_tx='' THEN RETURN NEW; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('refund-original:'||NEW.merchant_scope||':'||original_tx,0));
 IF TG_TABLE_NAME='payment_close_anomaly_refunds' THEN
  IF EXISTS(SELECT 1 FROM payment_transactions p WHERE p.id<>NEW.payment_id AND p.status='succeeded' AND p.external_transaction_id=original_tx)
   OR EXISTS(SELECT 1 FROM external_refunds r WHERE r.merchant_scope=NEW.merchant_scope AND r.original_transaction_id=original_tx AND r.status<>'rejected') THEN
   RAISE EXCEPTION 'original transaction is already claimed' USING ERRCODE='23514';
  END IF;
 ELSE
  IF EXISTS(SELECT 1 FROM payment_close_anomaly_refunds r WHERE r.merchant_scope=NEW.merchant_scope AND r.external_transaction_id=original_tx AND r.status<>'rejected') THEN
   RAISE EXCEPTION 'original transaction is already claimed' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER anomaly_refund_original_transaction BEFORE INSERT OR UPDATE ON payment_close_anomaly_refunds FOR EACH ROW EXECUTE FUNCTION guard_refund_original_transaction();
CREATE TRIGGER external_refund_original_transaction BEFORE INSERT OR UPDATE ON external_refunds FOR EACH ROW EXECUTE FUNCTION guard_refund_original_transaction();
