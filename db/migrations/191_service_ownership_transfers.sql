-- Ownership history is a business fact. Keep services' row shape and every
-- original purchase/payment snapshot intact; request receipts remain unified.
CREATE TABLE service_ownership_transfers (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 service_id bigint NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
 generation bigint NOT NULL CHECK(generation>0),
 source_customer_id bigint NOT NULL REFERENCES users(id),
 target_customer_id bigint NOT NULL REFERENCES users(id),
 original_customer_id bigint NOT NULL REFERENCES users(id),
 actor_id bigint NOT NULL REFERENCES account_principals(id),
 service_version_before bigint NOT NULL CHECK(service_version_before>0),
 service_version_after bigint NOT NULL CHECK(service_version_after=service_version_before+1),
 audit_floor_id bigint NOT NULL CHECK(audit_floor_id>=0),
 job_floor_id bigint NOT NULL CHECK(job_floor_id>=0),
 change_floor_id bigint NOT NULL CHECK(change_floor_id>=0),
 cancellation_floor_id bigint NOT NULL CHECK(cancellation_floor_id>=0),
 reason text NOT NULL CHECK(char_length(btrim(reason)) BETWEEN 1 AND 500),
 handover_evidence text NOT NULL CHECK(char_length(btrim(handover_evidence)) BETWEEN 1 AND 500),
 idempotency_key text NOT NULL CHECK(char_length(idempotency_key) BETWEEN 8 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 service_before_snapshot jsonb NOT NULL CHECK(jsonb_typeof(service_before_snapshot)='object'),
 service_after_snapshot jsonb NOT NULL CHECK(jsonb_typeof(service_after_snapshot)='object'),
 transaction_id bigint NOT NULL DEFAULT txid_current(),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(source_customer_id<>target_customer_id),
 UNIQUE(service_id,generation), UNIQUE(service_id,service_version_after)
);
CREATE INDEX service_ownership_transfer_source ON service_ownership_transfers(source_customer_id,id DESC);
CREATE INDEX service_ownership_transfer_target ON service_ownership_transfers(target_customer_id,id DESC);
CREATE FUNCTION service_ownership_generation(service bigint) RETURNS bigint LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT generation FROM service_ownership_transfers WHERE service_id=service ORDER BY generation DESC LIMIT 1),0)
$$;
CREATE FUNCTION service_original_customer_id(service bigint) RETURNS bigint LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT original_customer_id FROM service_ownership_transfers WHERE service_id=service ORDER BY generation DESC LIMIT 1),(SELECT user_id FROM services WHERE id=service))
$$;
-- Keep generation-zero financial configuration byte-for-byte compatible.
-- Returning to the same customer must never make an earlier renewal reversible
-- against the entitlement delivered in a later ownership period.
CREATE OR REPLACE FUNCTION local_renewal_configuration(s services) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT (external_refund_order_snapshot(s)-ARRAY['next_due_at','renewal_discounts_used','version','updated_at','auto_renew','auto_renew_error','renewal_generation','status','last_error'])
  ||coalesce((SELECT jsonb_build_object('_admin_renewal_fixed_cents',p.fixed_amount_cents)
    FROM service_renewal_pricing p WHERE p.service_id=s.id),'{}'::jsonb)
  ||CASE WHEN service_ownership_generation(s.id)>0
    THEN jsonb_build_object('_ownership_generation',service_ownership_generation(s.id)) ELSE '{}'::jsonb END;
$$;
CREATE FUNCTION protect_service_ownership_transfer() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'service ownership facts are immutable' USING ERRCODE='23514';
END $$;
CREATE TRIGGER service_ownership_transfer_immutable BEFORE UPDATE OR DELETE ON service_ownership_transfers
 FOR EACH ROW EXECUTE FUNCTION protect_service_ownership_transfer();

CREATE FUNCTION guard_service_ownership_transfer() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE f service_ownership_transfers; p service_ownership_transfers; s services;
BEGIN
 IF TG_TABLE_NAME='services' THEN
  SELECT * INTO f FROM service_ownership_transfers WHERE service_id=NEW.id AND service_version_after=NEW.version AND transaction_id=txid_current();
  IF f.id IS NULL OR f.service_before_snapshot<>external_refund_order_snapshot(OLD) OR f.service_after_snapshot<>external_refund_order_snapshot(NEW) THEN
   RAISE EXCEPTION 'service owner change requires matching ownership fact' USING ERRCODE='23514';
  END IF;
 ELSE
  f:=NEW;
 END IF;
 SELECT * INTO s FROM services WHERE id=f.service_id;
 SELECT * INTO p FROM service_ownership_transfers WHERE service_id=f.service_id AND generation<f.generation ORDER BY generation DESC LIMIT 1;
 IF current_setting('transaction_isolation')<>'read committed' OR f.transaction_id<>txid_current()
 OR f.generation<>coalesce(p.generation,0)+1 OR (p.id IS NOT NULL AND (p.target_customer_id<>f.source_customer_id OR p.original_customer_id<>f.original_customer_id))
 OR (p.id IS NULL AND f.original_customer_id<>f.source_customer_id)
 OR s.user_id<>f.target_customer_id OR s.version<>f.service_version_after OR s.auto_renew OR s.auto_renew_error<>''
 OR f.service_after_snapshot<>external_refund_order_snapshot(s)
 OR f.service_before_snapshot->>'user_id'<>f.source_customer_id::text
 OR f.service_before_snapshot->>'version'<>f.service_version_before::text
 OR (f.service_before_snapshot->>'renewal_generation')::bigint+1<>s.renewal_generation
 OR f.service_before_snapshot-ARRAY['user_id','version','renewal_generation','auto_renew','auto_renew_error','updated_at']
   <>f.service_after_snapshot-ARRAY['user_id','version','renewal_generation','auto_renew','auto_renew_error','updated_at']
 OR NOT EXISTS(SELECT 1 FROM orders o WHERE o.id=s.order_id AND o.user_id=f.original_customer_id)
 OR NOT EXISTS(SELECT 1 FROM administrators a WHERE a.id=f.actor_id AND a.status='active')
 OR NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.actor_id=f.actor_id AND a.entity_type='service' AND a.entity_id=f.service_id::text AND a.action='service.ownership.transferred'
   AND a.xmin::text::bigint=txid_current()%4294967296
   AND a.payload->>'transfer_id'=f.id::text AND a.payload->>'source_customer_id'=f.source_customer_id::text AND a.payload->>'target_customer_id'=f.target_customer_id::text AND a.payload->>'owner_generation'=f.generation::text AND a.payload->>'request_hash'=f.request_hash)
 OR NOT EXISTS(SELECT 1 FROM mutation_operations m WHERE m.actor_id=f.actor_id AND m.scope='admin:service.transfer' AND m.idempotency_key=f.idempotency_key AND m.request_hash=f.request_hash
   AND m.xmin::text::bigint=txid_current()%4294967296
   AND m.result->>'service_id'=f.service_id::text AND m.result->>'source_customer_id'=f.source_customer_id::text AND m.result->>'target_customer_id'=f.target_customer_id::text AND m.result->>'owner_generation'=f.generation::text AND m.result->>'version'=f.service_version_after::text AND m.result->>'auto_renew'='false') THEN
  RAISE EXCEPTION 'service ownership evidence incomplete' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER service_ownership_transfer_pair AFTER INSERT ON service_ownership_transfers
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION guard_service_ownership_transfer();
CREATE CONSTRAINT TRIGGER service_ownership_change AFTER UPDATE ON services
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (OLD.user_id IS DISTINCT FROM NEW.user_id)
 EXECUTE FUNCTION guard_service_ownership_transfer();

-- Each ownership period can receive its own reminders without modifying or
-- reviving the previous customer's delivery facts. Existing history stays 0.
ALTER TABLE notification_delivery_events ADD COLUMN ownership_generation bigint NOT NULL DEFAULT 0 CHECK(ownership_generation>=0);
CREATE FUNCTION stamp_legacy_notification_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.ownership_generation:=coalesce(service_ownership_generation(NEW.service_id),0);
 RETURN NEW;
END $$;
CREATE TRIGGER legacy_notification_ownership BEFORE INSERT ON notification_delivery_events
 FOR EACH ROW EXECUTE FUNCTION stamp_legacy_notification_ownership();
DROP INDEX notification_delivery_events_service_legacy_date;
CREATE UNIQUE INDEX notification_delivery_events_service_legacy_date ON notification_delivery_events(service_id,channel,kind,stage,due_date,ownership_generation)
 WHERE service_id IS NOT NULL AND resource_due_at IS NULL;
DROP INDEX notification_delivery_events_service_exact_due;
CREATE UNIQUE INDEX notification_delivery_events_service_exact_due ON notification_delivery_events(service_id,channel,kind,stage,resource_due_at,ownership_generation)
 WHERE service_id IS NOT NULL AND resource_due_at IS NOT NULL;
DROP INDEX notification_delivery_events_provision;
CREATE UNIQUE INDEX notification_delivery_events_provision ON notification_delivery_events(service_id,channel,kind,ownership_generation)
 WHERE invoice_id IS NULL AND kind='service_provision_success';
CREATE FUNCTION protect_legacy_notification_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.ownership_generation<>OLD.ownership_generation THEN
  RAISE EXCEPTION 'notification ownership generation is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER legacy_notification_ownership_immutable BEFORE UPDATE ON notification_delivery_events
 FOR EACH ROW EXECUTE FUNCTION protect_legacy_notification_ownership();

CREATE INDEX notification_deliveries_inbox_message ON notification_deliveries(inbox_message_id)
 WHERE inbox_message_id IS NOT NULL;
