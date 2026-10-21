-- Keep administrator overrides outside services: existing financial evidence
-- snapshots contain its complete row and must remain byte-for-byte compatible.
CREATE TABLE service_renewal_pricing (
 service_id bigint PRIMARY KEY REFERENCES services(id),
 fixed_amount_cents bigint NOT NULL CHECK(fixed_amount_cents>=0)
);

CREATE FUNCTION service_manual_pricing_reason(target_service bigint) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT CASE
 WHEN s.import_review_required THEN 'service_review_required'
 WHEN s.status NOT IN ('active','suspended') OR s.inventory_released OR
  (s.billing_cycle NOT IN ('monthly','quarterly','annually') AND NOT(s.provision_mode='supplier' AND s.billing_cycle IN ('semiannually','biennially','triennially','fourly','fively','sixly','sevenly','eightly','ninely','tenly'))) THEN 'invalid_renewal_state'
 WHEN external_refund_order_blocked(s.order_id) THEN 'external_refund_order_hold'
 WHEN local_renewal_service_blocked(s.id) THEN 'renewal_refund_hold'
 WHEN EXISTS(SELECT 1 FROM external_refunds r JOIN invoices i ON i.id=r.invoice_id WHERE r.status='pending_review' AND
  (i.order_id=s.order_id OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id))) THEN 'service_refund_pending'
 WHEN EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=s.id AND status IN ('pending','approved')) THEN 'service_cancellation_pending'
 WHEN EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=s.id AND status IN ('pending','running','waiting_manual','waiting_verification')) THEN 'service_action_pending'
 WHEN s.provision_mode='supplier' AND EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id
  WHERE ii.service_id=s.id AND ii.item_type='renew' AND i.status='paid' AND NOT EXISTS(
   SELECT 1 FROM provision_jobs j WHERE j.service_id=s.id AND j.action='renew' AND j.idempotency_key='supplier-renew-'||i.id::text AND j.status='succeeded')) THEN 'supplier_renewal_pending'
 WHEN EXISTS(SELECT 1 FROM service_configuration_changes WHERE service_id=s.id AND status IN ('unpaid','queued','waiting_manual')) THEN 'configuration_change_pending'
 WHEN EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE ii.service_id=s.id AND i.status='unpaid' AND ii.item_type IN ('renew','upgrade','traffic'))
  OR EXISTS(SELECT 1 FROM traffic_orders t JOIN invoices i ON i.id=t.invoice_id WHERE t.service_id=s.id AND i.status='unpaid') THEN 'renewal_invoice_pending'
 WHEN cancellation_renewal_payment_unresolved(s.id) THEN 'invoice_payment_pending'
 ELSE '' END FROM services s WHERE s.id=target_service;
$$;

-- Also fence writers outside the HTTP command. The service is the serialization
-- parent for quotations, payments, refunds, upgrades and automatic renewal.
CREATE FUNCTION guard_service_renewal_pricing() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_service bigint; reason text;
BEGIN
 IF TG_OP='UPDATE' AND NEW.service_id<>OLD.service_id THEN
  RAISE EXCEPTION 'service pricing identity is immutable' USING ERRCODE='23514';
 END IF;
 IF TG_OP='DELETE' THEN target_service:=OLD.service_id; ELSE target_service:=NEW.service_id; END IF;
 PERFORM id FROM services WHERE id=target_service FOR UPDATE NOWAIT;
 reason:=service_manual_pricing_reason(target_service);
 IF reason IS NULL THEN RAISE EXCEPTION 'service missing' USING ERRCODE='23514'; END IF;
 IF reason<>'' THEN RAISE EXCEPTION '%',reason USING ERRCODE='23514'; END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER service_renewal_pricing_guard BEFORE INSERT OR UPDATE OR DELETE ON service_renewal_pricing
 FOR EACH ROW EXECUTE FUNCTION guard_service_renewal_pricing();

-- AFTER, rather than BEFORE INSERT: an UPSERT may take the conflict path and
-- must advance the service generation once, never twice or on a no-op.
CREATE FUNCTION advance_service_renewal_pricing() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_service bigint;
BEGIN
 IF TG_OP='DELETE' THEN target_service:=OLD.service_id; ELSE target_service:=NEW.service_id; END IF;
 IF TG_OP<>'UPDATE' OR NEW.fixed_amount_cents IS DISTINCT FROM OLD.fixed_amount_cents THEN
  UPDATE services SET version=version+1,renewal_generation=renewal_generation+1,
   auto_renew_error='',updated_at=clock_timestamp() WHERE id=target_service;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER service_renewal_pricing_advance AFTER INSERT OR UPDATE OR DELETE ON service_renewal_pricing
 FOR EACH ROW EXECUTE FUNCTION advance_service_renewal_pricing();

-- Contract-mode services retain the exact old configuration JSON. A fixed
-- override is significant for a later renewal refund, including zero cents.
CREATE OR REPLACE FUNCTION local_renewal_configuration(s services) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT (external_refund_order_snapshot(s)-ARRAY['next_due_at','renewal_discounts_used','version','updated_at','auto_renew','auto_renew_error','renewal_generation','status','last_error'])
  ||coalesce((SELECT jsonb_build_object('_admin_renewal_fixed_cents',p.fixed_amount_cents)
    FROM service_renewal_pricing p WHERE p.service_id=s.id),'{}'::jsonb);
$$;
