-- Keep services' row shape stable: immutable refund snapshots contain its full
-- JSON representation. Never backfill an observed upstream deadline from a
-- locally promised deadline, and never infer historical management authority.
CREATE TABLE service_term_management (
 service_id bigint PRIMARY KEY REFERENCES services(id) ON DELETE RESTRICT,
 mode text NOT NULL DEFAULT 'follow_supplier' CHECK(mode IN ('follow_supplier','managed')),
 supplier_next_due_at timestamptz CHECK(supplier_next_due_at IS NULL OR isfinite(supplier_next_due_at)),
 supplier_observed_at timestamptz CHECK(supplier_observed_at IS NULL OR isfinite(supplier_observed_at)),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(supplier_next_due_at IS NULL OR supplier_observed_at IS NOT NULL)
);

CREATE FUNCTION service_term_billing_months(cycle text) RETURNS integer LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE cycle WHEN 'monthly' THEN 1 WHEN 'quarterly' THEN 3 WHEN 'semiannually' THEN 6
 WHEN 'annually' THEN 12 WHEN 'biennially' THEN 24 WHEN 'triennially' THEN 36
 WHEN 'fourly' THEN 48 WHEN 'fively' THEN 60 WHEN 'sixly' THEN 72 WHEN 'sevenly' THEN 84
 WHEN 'eightly' THEN 96 WHEN 'ninely' THEN 108 WHEN 'tenly' THEN 120 ELSE 0 END;
$$;

CREATE FUNCTION service_manual_term_reason(target_service bigint) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT CASE
 WHEN s.import_review_required THEN 'service_review_required'
 WHEN s.status NOT IN ('active','suspended') OR service_term_billing_months(s.billing_cycle)=0
  OR s.next_due_at IS NULL OR s.inventory_released THEN 'service_term_unavailable'
 WHEN external_refund_order_blocked(s.order_id) THEN 'external_refund_order_hold'
 WHEN local_renewal_service_blocked(s.id) THEN 'renewal_refund_hold'
 WHEN EXISTS(SELECT 1 FROM external_refunds r JOIN invoices i ON i.id=r.invoice_id WHERE r.status='pending_review' AND
  (i.order_id=s.order_id OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id))) THEN 'service_refund_pending'
 WHEN EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=s.id AND status IN ('pending','approved')) THEN 'service_cancellation_pending'
 WHEN EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=s.id AND status IN ('pending','running','waiting_manual','waiting_verification')) THEN 'service_action_pending'
 WHEN EXISTS(SELECT 1 FROM service_configuration_changes WHERE service_id=s.id AND status IN ('unpaid','queued','waiting_manual')) THEN 'configuration_change_pending'
 WHEN EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE ii.service_id=s.id AND i.status='unpaid' AND ii.item_type IN ('renew','upgrade','traffic'))
  OR EXISTS(SELECT 1 FROM traffic_orders t JOIN invoices i ON i.id=t.invoice_id WHERE t.service_id=s.id AND i.status='unpaid') THEN 'renewal_invoice_pending'
 WHEN cancellation_renewal_payment_unresolved(s.id) THEN 'invoice_payment_pending'
 WHEN s.provision_mode='supplier' AND EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id
  LEFT JOIN provision_jobs j ON j.idempotency_key='supplier-renew-'||i.id::text
  WHERE ii.service_id=s.id AND ii.item_type='renew' AND i.status='paid' AND coalesce(j.status,'')<>'succeeded') THEN 'supplier_renewal_pending'
 ELSE '' END FROM services s WHERE s.id=target_service;
$$;

CREATE FUNCTION guard_service_term_management() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE reason text;
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'service term management cannot be deleted' USING ERRCODE='23514';
 END IF;
 IF TG_OP='UPDATE' AND NEW.service_id<>OLD.service_id THEN
  RAISE EXCEPTION 'service term identity is immutable' USING ERRCODE='23514';
 END IF;
 PERFORM id FROM services WHERE id=NEW.service_id FOR UPDATE;
 IF TG_OP='UPDATE' AND OLD.mode='managed' AND NEW.mode<>'managed' THEN
  RAISE EXCEPTION 'managed term cannot silently follow supplier' USING ERRCODE='23514';
 END IF;
 IF NEW.mode='managed' AND (TG_OP='INSERT' OR OLD.mode IS DISTINCT FROM NEW.mode) THEN
  reason:=service_manual_term_reason(NEW.service_id);
  IF reason IS NULL OR reason<>'' THEN
   RAISE EXCEPTION 'service term operation pending' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER service_term_management_guard BEFORE INSERT OR UPDATE OR DELETE ON service_term_management
 FOR EACH ROW EXECUTE FUNCTION guard_service_term_management();

-- A confirmed upstream purchase advances a managed customer contract once.
-- This is an entitlement fact, not another financial/idempotency receipt.
CREATE TABLE service_managed_renewal_effects (
 job_id bigint PRIMARY KEY REFERENCES provision_jobs(id) ON DELETE RESTRICT,
 service_id bigint NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
 invoice_id bigint NOT NULL UNIQUE REFERENCES invoices(id) ON DELETE RESTRICT,
 billing_cycle text NOT NULL CHECK(service_term_billing_months(billing_cycle)>0),
 before_due timestamptz NOT NULL CHECK(isfinite(before_due)),
 period_start timestamptz NOT NULL CHECK(isfinite(period_start)),
 after_due timestamptz NOT NULL CHECK(isfinite(after_due)),
 supplier_after_due timestamptz NOT NULL CHECK(isfinite(supplier_after_due)),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(period_start>=before_due AND after_due>period_start)
);
CREATE INDEX service_managed_renewal_effect_service ON service_managed_renewal_effects(service_id,job_id);

CREATE FUNCTION guard_service_managed_renewal_effect() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN
  RAISE EXCEPTION 'managed renewal effect is immutable' USING ERRCODE='23514';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM provision_jobs j JOIN services s ON s.id=j.service_id
  JOIN service_term_management t ON t.service_id=s.id
  JOIN invoices i ON i.id=NEW.invoice_id AND i.user_id=s.user_id
  WHERE j.id=NEW.job_id AND j.service_id=NEW.service_id AND j.action='renew'
   AND j.status IN ('running','waiting_verification') AND j.external_started_at IS NOT NULL
   AND j.idempotency_key='supplier-renew-'||i.id::text
   AND j.request_payload->>'local_invoice_id'=i.id::text
   AND s.provision_mode='supplier' AND s.status IN ('active','suspended') AND t.mode='managed'
   AND s.billing_cycle=NEW.billing_cycle AND s.next_due_at=NEW.before_due
   AND i.status='paid' AND i.invoice_kind='automatic' AND i.order_id IS NULL
   AND NOT i.import_review_required AND NOT s.import_review_required
   AND (SELECT count(*) FROM invoice_items WHERE invoice_id=i.id)=1
   AND EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id AND ii.item_type='renew')
   AND NEW.after_due=(((NEW.period_start AT TIME ZONE 'Asia/Shanghai')+
    make_interval(months=>service_term_billing_months(NEW.billing_cycle))) AT TIME ZONE 'Asia/Shanghai')
 ) THEN
  RAISE EXCEPTION 'managed renewal effect source invalid' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER service_managed_renewal_effect_guard BEFORE INSERT OR UPDATE OR DELETE ON service_managed_renewal_effects
 FOR EACH ROW EXECUTE FUNCTION guard_service_managed_renewal_effect();

CREATE FUNCTION check_service_managed_renewal_complete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM services s WHERE s.id=NEW.service_id AND s.next_due_at=NEW.after_due) THEN
  RAISE EXCEPTION 'managed renewal effect incomplete' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER service_managed_renewal_effect_complete AFTER INSERT ON service_managed_renewal_effects
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_service_managed_renewal_complete();
