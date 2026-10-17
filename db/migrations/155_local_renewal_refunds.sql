-- A local renewal is an indivisible invoice settlement and immutable entitlement
-- effect. No historical backfill: subtraction cannot recover calendar arithmetic.
ALTER TABLE services ADD COLUMN renewal_generation bigint NOT NULL DEFAULT 0 CHECK(renewal_generation>=0);
CREATE TABLE service_renewal_effects (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 invoice_id bigint NOT NULL UNIQUE REFERENCES invoices(id), item_id bigint NOT NULL UNIQUE REFERENCES invoice_items(id),
 service_id bigint NOT NULL REFERENCES services(id), user_id bigint NOT NULL REFERENCES users(id),
 billing_cycle text NOT NULL CHECK(billing_cycle IN ('monthly','quarterly','annually')),
 before_due timestamptz, period_start timestamptz NOT NULL, after_due timestamptz NOT NULL,
 before_used bigint NOT NULL CHECK(before_used>=0), after_used bigint NOT NULL CHECK(after_used>=before_used AND after_used-before_used<=1),
 effective_at timestamptz NOT NULL, configuration jsonb NOT NULL, item_snapshot jsonb NOT NULL,
 CHECK(after_due>period_start), CHECK(before_due IS NULL OR period_start>=before_due)
);
CREATE INDEX service_renewal_effect_service ON service_renewal_effects(service_id,id);
CREATE TABLE service_renewal_reversals (
 effect_id bigint PRIMARY KEY REFERENCES service_renewal_effects(id), invoice_id bigint NOT NULL UNIQUE REFERENCES invoices(id),
 service_id bigint NOT NULL REFERENCES services(id), refund_id bigint UNIQUE REFERENCES external_refunds(id),
 generation_before bigint NOT NULL, generation_after bigint NOT NULL CHECK(generation_after=generation_before+1),
 applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE external_refund_renewal_holds (
 refund_id bigint PRIMARY KEY REFERENCES external_refunds(id), effect_id bigint NOT NULL REFERENCES service_renewal_effects(id),
 invoice_id bigint NOT NULL REFERENCES invoices(id), payment_id bigint NOT NULL REFERENCES payment_transactions(id),
 service_id bigint NOT NULL REFERENCES services(id), user_id bigint NOT NULL REFERENCES users(id),
 invoice_version bigint NOT NULL, service_version bigint NOT NULL,
 invoice_snapshot jsonb NOT NULL, service_snapshot jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','applying','released','applied')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), applied_at timestamptz, released_at timestamptz
);
CREATE UNIQUE INDEX external_refund_renewal_effect_live ON external_refund_renewal_holds(effect_id) WHERE status<>'released';
CREATE UNIQUE INDEX external_refund_renewal_service_live ON external_refund_renewal_holds(service_id) WHERE status IN ('pending','applying');
CREATE FUNCTION local_renewal_configuration(s services) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT external_refund_order_snapshot(s)-ARRAY['next_due_at','renewal_discounts_used','version','updated_at','auto_renew','auto_renew_error','renewal_generation','status','last_error'];
$$;
CREATE FUNCTION protect_local_renewal_fact() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP<>'INSERT' OR pg_trigger_depth()<2 THEN RAISE EXCEPTION 'renewal_effect_immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER local_renewal_effect_immutable BEFORE INSERT OR UPDATE OR DELETE ON service_renewal_effects FOR EACH ROW EXECUTE FUNCTION protect_local_renewal_fact();
CREATE TRIGGER local_renewal_reversal_immutable BEFORE INSERT OR UPDATE OR DELETE ON service_renewal_reversals FOR EACH ROW EXECUTE FUNCTION protect_local_renewal_fact();

-- Same payment transaction for all three tenders. This AFTER trigger sees the
-- actual paid transition; a subsequent Go settlement call detects the effect.
CREATE FUNCTION settle_local_renewal_effect() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ii invoice_items; s services; used_after bigint; start_at timestamptz; finish_at timestamptz; settled_at timestamptz;
BEGIN
 IF NEW.status<>'paid' OR OLD.status<>'unpaid' OR NEW.order_id IS NOT NULL OR NEW.invoice_kind<>'automatic' THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM invoice_items WHERE invoice_id=NEW.id AND item_type='renew') THEN RETURN NEW; END IF;
 IF (SELECT count(*) FROM invoice_items WHERE invoice_id=NEW.id)<>1 THEN RAISE EXCEPTION 'renewal_effect_changed' USING ERRCODE='23514'; END IF;
 SELECT * INTO ii FROM invoice_items WHERE invoice_id=NEW.id AND item_type='renew';
 BEGIN SELECT * INTO s FROM services WHERE id=ii.service_id FOR UPDATE NOWAIT;
 EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514'; END;
 IF s.provision_mode='supplier' THEN RETURN NEW; END IF;
 IF s.id IS NULL OR s.user_id<>NEW.user_id OR ii.amount_cents<>NEW.amount_cents OR NEW.amount_cents<0 OR NEW.paid_at IS NULL OR NEW.import_review_required OR s.import_review_required OR s.status NOT IN ('active','suspended') OR s.billing_cycle NOT IN ('monthly','quarterly','annually') THEN RAISE EXCEPTION 'renewal_effect_changed' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM service_configuration_changes WHERE service_id=s.id AND status IN ('unpaid','queued','waiting_manual')) THEN RAISE EXCEPTION 'configuration_change_pending' USING ERRCODE='23514'; END IF;
 used_after:=s.renewal_discounts_used;
 IF ii.renewal_discount_ordinal>0 THEN
  IF ii.renewal_discount_ordinal<>s.renewal_discounts_used+1 OR ii.renewal_discount_percent<>s.renewal_discount_percent OR ii.renewal_fixed_discount_cents<>s.renewal_fixed_discount_cents OR ii.renewal_discount_limit<>s.renewal_discount_limit OR ii.renewal_promo_code<>s.renewal_promo_code OR (s.renewal_discount_limit>0 AND ii.renewal_discount_ordinal>s.renewal_discount_limit) THEN RAISE EXCEPTION 'renewal_benefit_changed' USING ERRCODE='23514'; END IF;
  used_after:=used_after+1;
 END IF;
 settled_at:=clock_timestamp();
 start_at:=GREATEST(coalesce(s.next_due_at,settled_at),settled_at);
 finish_at:=start_at+CASE s.billing_cycle WHEN 'quarterly' THEN interval '3 months' WHEN 'annually' THEN interval '1 year' ELSE interval '1 month' END;
 UPDATE services SET next_due_at=finish_at,renewal_discounts_used=used_after,auto_renew_error='',version=version+1,updated_at=settled_at WHERE id=s.id;
 INSERT INTO service_renewal_effects(invoice_id,item_id,service_id,user_id,billing_cycle,before_due,period_start,after_due,before_used,after_used,effective_at,configuration,item_snapshot)
 VALUES(NEW.id,ii.id,s.id,s.user_id,s.billing_cycle,s.next_due_at,start_at,finish_at,s.renewal_discounts_used,used_after,settled_at,local_renewal_configuration(s),external_refund_order_snapshot(ii));
 RETURN NEW;
END $$;
CREATE TRIGGER zz_local_renewal_settle AFTER UPDATE OF status ON invoices FOR EACH ROW EXECUTE FUNCTION settle_local_renewal_effect();

CREATE FUNCTION local_renewal_refund_reason(target_invoice bigint, own_refund bigint DEFAULT NULL, external_money boolean DEFAULT false) RETURNS text LANGUAGE plpgsql VOLATILE AS $$
DECLARE e service_renewal_effects; s services; i invoices;
BEGIN
 SELECT * INTO i FROM invoices WHERE id=target_invoice;
 SELECT * INTO e FROM service_renewal_effects WHERE invoice_id=target_invoice;
 IF e.id IS NULL THEN
  IF EXISTS(SELECT 1 FROM invoice_items ii JOIN services x ON x.id=ii.service_id WHERE ii.invoice_id=target_invoice AND ii.item_type='renew' AND x.provision_mode='supplier') THEN RETURN 'renewal_external_entitlement'; END IF;
  RETURN 'renewal_effect_missing';
 END IF;
 SELECT * INTO s FROM services WHERE id=e.service_id;
 IF EXISTS(SELECT 1 FROM service_renewal_reversals WHERE effect_id=e.id) THEN RETURN 'renewal_already_reversed'; END IF;
 IF EXISTS(SELECT 1 FROM external_refund_renewal_holds WHERE service_id=e.service_id AND status IN ('pending','applying') AND refund_id IS DISTINCT FROM own_refund) THEN RETURN 'renewal_refund_hold'; END IF;
 IF i.amount_cents<>(e.item_snapshot->>'amount_cents')::bigint THEN RETURN 'renewal_effect_changed'; END IF;
 IF i.status<>'paid' OR i.order_id IS NOT NULL OR i.invoice_kind<>'automatic' OR s.user_id<>i.user_id OR s.user_id<>e.user_id OR s.import_review_required OR i.import_review_required OR s.status NOT IN ('active','suspended') THEN RETURN 'renewal_service_state'; END IF;
 IF s.provision_mode='supplier' THEN RETURN 'renewal_external_entitlement'; END IF;
 IF e.before_due IS NULL THEN RETURN 'renewal_original_due_unproven'; END IF;
 IF e.before_due<=clock_timestamp() OR e.period_start<=clock_timestamp() THEN RETURN 'renewal_period_consumed'; END IF;
 IF EXISTS(SELECT 1 FROM service_renewal_effects z WHERE z.service_id=e.service_id AND z.id>e.id AND NOT EXISTS(SELECT 1 FROM service_renewal_reversals r WHERE r.effect_id=z.id)) THEN RETURN 'renewal_successor_exists'; END IF;
 IF s.next_due_at IS DISTINCT FROM e.after_due OR s.renewal_discounts_used<>e.after_used OR local_renewal_configuration(s)<>e.configuration OR (SELECT count(*) FROM invoice_items WHERE invoice_id=i.id)<>1 OR NOT EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.id=e.item_id AND external_refund_order_snapshot(ii)=e.item_snapshot) THEN RETURN 'renewal_effect_changed'; END IF;
 IF EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices x ON x.id=ii.invoice_id WHERE ii.service_id=s.id AND x.id<>i.id AND x.status='unpaid' AND ii.item_type IN ('renew','upgrade','traffic')) OR EXISTS(SELECT 1 FROM traffic_orders t JOIN invoices x ON x.id=t.invoice_id WHERE t.service_id=s.id AND x.status='unpaid') THEN RETURN 'renewal_dependent_invoice'; END IF;
 IF EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=s.id AND status IN ('pending','running','waiting_manual','waiting_verification')) OR EXISTS(SELECT 1 FROM service_configuration_changes WHERE service_id=s.id AND status IN ('unpaid','queued','waiting_manual')) OR EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=s.id AND status='pending') THEN RETURN 'renewal_operation_pending'; END IF;
 IF EXISTS(SELECT 1 FROM invoice_application_items x JOIN invoice_applications a ON a.id=x.application_id WHERE x.source_invoice_id=i.id AND a.status IN ('pending','issued')) OR EXISTS(SELECT 1 FROM affiliate_commission_entries WHERE invoice_id=i.id) OR EXISTS(SELECT 1 FROM affiliate_turnover_entries WHERE invoice_id=i.id) THEN RETURN 'renewal_financial_association'; END IF;
 IF EXISTS(SELECT 1 FROM payment_transactions p WHERE p.invoice_id=i.id AND p.status IN ('pending','expired') AND p.provider<>'mock') OR EXISTS(SELECT 1 FROM payment_close_operations c WHERE c.invoice_id=i.id AND payment_close_operation_unresolved(c.id)) OR EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) THEN RETURN 'renewal_payment_unresolved'; END IF;
 IF external_money AND ((SELECT count(*) FROM payment_transactions WHERE invoice_id=i.id AND status='succeeded')<>1 OR NOT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=i.id AND status='succeeded' AND user_id=i.user_id AND amount_cents=i.amount_cents) OR EXISTS(SELECT 1 FROM credit_invoice_payments WHERE invoice_id=i.id) OR EXISTS(SELECT 1 FROM balance_ledger WHERE reference_type='invoice' AND reference_id=i.id AND entry_type IN ('payment','refund'))) THEN RETURN 'renewal_financial_association'; END IF;
 RETURN '';
END $$;

-- Common inverse for external, balance, credit and zero-price invoices. The
-- invoice transition itself creates the reversal, so application-only callers
-- cannot omit it. No session flag or mutable effect status is trusted.
CREATE FUNCTION reverse_local_renewal_effect() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e service_renewal_effects; s services; h external_refund_renewal_holds; reason text;
BEGIN
 IF OLD.status<>'paid' OR NEW.status<>'refunded' OR NEW.order_id IS NOT NULL OR NOT EXISTS(SELECT 1 FROM invoice_items WHERE invoice_id=OLD.id AND item_type='renew') THEN RETURN NEW; END IF;
 SELECT * INTO e FROM service_renewal_effects WHERE invoice_id=OLD.id;
 IF e.id IS NULL THEN
  IF EXISTS(SELECT 1 FROM invoice_items ii JOIN services x ON x.id=ii.service_id WHERE ii.invoice_id=OLD.id AND x.provision_mode='supplier') THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'renewal_effect_missing' USING ERRCODE='23514';
 END IF;
 BEGIN SELECT * INTO s FROM services WHERE id=e.service_id FOR UPDATE NOWAIT; EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514'; END;
 SELECT * INTO h FROM external_refund_renewal_holds WHERE invoice_id=OLD.id AND status IN ('pending','applying') FOR UPDATE;
 IF h.refund_id IS NOT NULL AND h.status<>'applying' THEN RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514'; END IF;
 reason:=local_renewal_refund_reason(OLD.id,h.refund_id,h.refund_id IS NOT NULL);
 IF reason<>'' THEN RAISE EXCEPTION '%',reason USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=OLD.id AND status='succeeded') AND h.refund_id IS NULL THEN RAISE EXCEPTION 'renewal_financial_association' USING ERRCODE='23514'; END IF;
 INSERT INTO service_renewal_reversals(effect_id,invoice_id,service_id,refund_id,generation_before,generation_after) VALUES(e.id,e.invoice_id,e.service_id,h.refund_id,s.renewal_generation,s.renewal_generation+1);
 UPDATE services SET next_due_at=e.before_due,renewal_discounts_used=e.before_used,auto_renew=false,auto_renew_error='续费已冲正，自动续费已关闭',renewal_generation=renewal_generation+1,version=version+1,updated_at=clock_timestamp() WHERE id=s.id;
 RETURN NEW;
END $$;
CREATE TRIGGER zy_local_renewal_reverse BEFORE UPDATE OF status ON invoices FOR EACH ROW EXECUTE FUNCTION reverse_local_renewal_effect();

CREATE FUNCTION local_renewal_service_blocked(sid bigint) RETURNS boolean LANGUAGE sql VOLATILE AS $$ SELECT EXISTS(SELECT 1 FROM external_refund_renewal_holds WHERE service_id=sid AND status IN ('pending','applying')) $$;
CREATE FUNCTION guard_local_renewal_service() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE h external_refund_renewal_holds; e service_renewal_effects;
BEGIN
 SELECT * INTO h FROM external_refund_renewal_holds WHERE service_id=OLD.id AND status IN ('pending','applying');
 IF h.refund_id IS NULL THEN RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END; END IF;
 SELECT * INTO e FROM service_renewal_effects WHERE id=h.effect_id;
 IF TG_OP='DELETE' OR h.status<>'applying' OR NEW.next_due_at IS DISTINCT FROM e.before_due OR NEW.renewal_discounts_used<>e.before_used OR NEW.auto_renew OR NEW.renewal_generation<>OLD.renewal_generation+1 OR NEW.auto_renew_error<>'续费已冲正，自动续费已关闭' OR
 (external_refund_order_snapshot(NEW)-ARRAY['next_due_at','renewal_discounts_used','auto_renew','auto_renew_error','renewal_generation','version','updated_at'])<>(external_refund_order_snapshot(OLD)-ARRAY['next_due_at','renewal_discounts_used','auto_renew','auto_renew_error','renewal_generation','version','updated_at']) OR NOT EXISTS(SELECT 1 FROM service_renewal_reversals WHERE effect_id=e.id AND refund_id=h.refund_id) THEN RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER zz_local_renewal_service_guard BEFORE UPDATE OR DELETE ON services FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_service();

CREATE FUNCTION guard_local_renewal_association() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; iid bigint; sid bigint; x record; h external_refund_renewal_holds;
BEGIN
 -- Existing commission identity already disqualifies this invoice. Preserve
 -- withdrawal account -> bucket locking for pure bucket/status movements.
 IF TG_TABLE_NAME='affiliate_commission_entries' AND TG_OP='UPDATE' AND
  (external_refund_order_snapshot(NEW)-ARRAY['status','pending_cents','available_cents','reserved_cents','paid_cents','reversed_cents','reversal_at'])=
  (external_refund_order_snapshot(OLD)-ARRAY['status','pending_cents','available_cents','reserved_cents','paid_cents','reversed_cents','reversal_at']) THEN RETURN NEW; END IF;
 FOR v IN SELECT value FROM jsonb_array_elements(CASE TG_OP WHEN 'INSERT' THEN jsonb_build_array(to_jsonb(NEW)) WHEN 'DELETE' THEN jsonb_build_array(to_jsonb(OLD)) ELSE jsonb_build_array(to_jsonb(OLD),to_jsonb(NEW)) END) LOOP
  iid:=CASE WHEN TG_TABLE_NAME='invoices' THEN (v->>'id')::bigint WHEN TG_TABLE_NAME='balance_ledger' AND v->>'reference_type'='invoice' THEN (v->>'reference_id')::bigint ELSE coalesce((v->>'invoice_id')::bigint,(v->>'source_invoice_id')::bigint) END;
  sid:=(v->>'service_id')::bigint;
  -- Callback/payment writers own a payment child before the invoice. Never
  -- acquire its service first and then wait for the invoice in the caller.
  IF iid IS NOT NULL AND TG_TABLE_NAME<>'invoices' THEN
   BEGIN
    IF TG_OP='INSERT' AND TG_TABLE_NAME IN ('affiliate_commission_entries','affiliate_turnover_entries') THEN PERFORM id FROM invoices WHERE id=iid FOR SHARE NOWAIT;
    ELSE PERFORM id FROM invoices WHERE id=iid FOR UPDATE NOWAIT; END IF;
   EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514'; END;
  END IF;
  IF TG_TABLE_NAME='invoice_applications' THEN
   BEGIN PERFORM i.id FROM invoices i JOIN invoice_application_items ai ON ai.source_invoice_id=i.id WHERE ai.application_id=(v->>'id')::bigint ORDER BY i.id FOR UPDATE OF i NOWAIT;
   EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514'; END;
  END IF;
  -- Existing child writers must not block in reverse invoice/service order.
  FOR x IN SELECT DISTINCT z.id FROM services z WHERE z.id=sid OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=iid AND ii.service_id=z.id) OR (TG_TABLE_NAME='invoice_applications' AND EXISTS(SELECT 1 FROM invoice_application_items ai JOIN invoice_items ii ON ii.invoice_id=ai.source_invoice_id WHERE ai.application_id=(v->>'id')::bigint AND ii.service_id=z.id)) ORDER BY z.id LOOP
   BEGIN PERFORM id FROM services WHERE id=x.id FOR UPDATE NOWAIT;
   EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514'; END;
   SELECT * INTO h FROM external_refund_renewal_holds WHERE service_id=x.id AND status IN ('pending','applying');
   IF h.refund_id IS NOT NULL THEN
    -- A late paid-after-close receipt is evidence, not a new entitlement.
    -- Preserve the existing anomaly intake while serializing it against the
    -- refund. Confirmation rechecks the unresolved anomaly; rejection works.
    IF TG_TABLE_NAME='payment_close_anomalies' AND TG_OP='INSERT' THEN CONTINUE; END IF;
    IF TG_TABLE_NAME='invoices' THEN
    IF TG_OP='UPDATE' AND h.status='applying' AND (v->>'id')::bigint=h.invoice_id AND NEW.status='refunded' AND
     (external_refund_order_snapshot(NEW)-ARRAY['status','version','updated_at'])=(external_refund_order_snapshot(OLD)-ARRAY['status','version','updated_at']) THEN CONTINUE; END IF;
    END IF;
    RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514';
   END IF;
  END LOOP;
 END LOOP;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE TRIGGER zz_local_renewal_invoice_guard BEFORE INSERT OR UPDATE OR DELETE ON invoices FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_item_guard BEFORE INSERT OR UPDATE OR DELETE ON invoice_items FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_job_guard BEFORE INSERT OR UPDATE OR DELETE ON provision_jobs FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_config_guard BEFORE INSERT OR UPDATE OR DELETE ON service_configuration_changes FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_traffic_guard BEFORE INSERT OR UPDATE OR DELETE ON traffic_orders FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_cancel_guard BEFORE INSERT OR UPDATE OR DELETE ON service_cancellation_requests FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_payment_guard BEFORE INSERT OR UPDATE OR DELETE ON payment_transactions FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_credit_guard BEFORE INSERT OR UPDATE OR DELETE ON credit_invoice_payments FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_balance_guard BEFORE INSERT OR UPDATE OR DELETE ON balance_ledger FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_application_guard BEFORE INSERT OR UPDATE OR DELETE ON invoice_application_items FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_application_parent_guard BEFORE UPDATE OR DELETE ON invoice_applications FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_close_guard BEFORE INSERT OR UPDATE OR DELETE ON payment_close_operations FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_anomaly_guard BEFORE INSERT OR UPDATE OR DELETE ON payment_close_anomalies FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_commission_guard BEFORE INSERT OR UPDATE OR DELETE ON affiliate_commission_entries FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();
CREATE TRIGGER zz_local_renewal_turnover_guard BEFORE INSERT OR UPDATE OR DELETE ON affiliate_turnover_entries FOR EACH ROW EXECUTE FUNCTION guard_local_renewal_association();

CREATE FUNCTION protect_local_renewal_hold() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e service_renewal_effects; s services; i invoices; r external_refunds; reason text;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' THEN
  IF OLD.status NOT IN ('pending','applying') OR (OLD.status='pending' AND NEW.status NOT IN ('applying','released')) OR (OLD.status='applying' AND NEW.status<>'applied') OR
   (to_jsonb(NEW)-ARRAY['status','applied_at','released_at'])<>(to_jsonb(OLD)-ARRAY['status','applied_at','released_at']) THEN RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 SELECT * INTO e FROM service_renewal_effects WHERE id=NEW.effect_id;
 BEGIN SELECT * INTO s FROM services WHERE id=e.service_id FOR UPDATE NOWAIT;
 EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514'; END;
 SELECT * INTO i FROM invoices WHERE id=NEW.invoice_id;
 SELECT * INTO r FROM external_refunds WHERE id=NEW.refund_id;
 reason:=local_renewal_refund_reason(i.id,NULL,true);
 IF reason<>'' THEN RAISE EXCEPTION '%',reason USING ERRCODE='23514'; END IF;
 IF NEW.status<>'pending' OR NEW.applied_at IS NOT NULL OR NEW.released_at IS NOT NULL OR e.id IS NULL OR e.invoice_id<>i.id OR e.service_id<>NEW.service_id OR e.user_id<>NEW.user_id OR r.invoice_id<>i.id OR r.payment_id<>NEW.payment_id OR r.user_id<>NEW.user_id OR r.status<>'pending_review' OR NEW.invoice_version<>i.version OR NEW.service_version<>s.version OR NEW.invoice_snapshot<>external_refund_order_snapshot(i) OR NEW.service_snapshot<>external_refund_order_snapshot(s) THEN RAISE EXCEPTION 'renewal_refund_hold' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER local_renewal_hold_immutable BEFORE INSERT OR UPDATE OR DELETE ON external_refund_renewal_holds FOR EACH ROW EXECUTE FUNCTION protect_local_renewal_hold();

CREATE FUNCTION check_local_renewal_complete() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e service_renewal_effects; r service_renewal_reversals; i invoices; h external_refund_renewal_holds; f external_refunds; valid boolean;
BEGIN
 IF TG_TABLE_NAME='external_refunds' THEN
  SELECT * INTO e FROM service_renewal_effects WHERE invoice_id=NEW.invoice_id;
  IF e.id IS NULL THEN
   IF EXISTS(SELECT 1 FROM invoice_items WHERE invoice_id=NEW.invoice_id AND item_type='renew') THEN RAISE EXCEPTION 'renewal_effect_missing' USING ERRCODE='23514'; END IF;
   RETURN NEW;
  END IF;
  SELECT * INTO h FROM external_refund_renewal_holds WHERE refund_id=NEW.id;
  IF h.refund_id IS NULL THEN RAISE EXCEPTION 'renewal_refund_incomplete' USING ERRCODE='23514'; END IF;
 ELSIF TG_TABLE_NAME='external_refund_renewal_holds' THEN
  SELECT * INTO h FROM external_refund_renewal_holds WHERE refund_id=NEW.refund_id;
  SELECT * INTO e FROM service_renewal_effects WHERE id=h.effect_id;
 ELSIF TG_TABLE_NAME='service_renewal_effects' THEN SELECT * INTO e FROM service_renewal_effects WHERE id=NEW.id;
 ELSE SELECT * INTO e FROM service_renewal_effects WHERE id=NEW.effect_id; END IF;
 SELECT * INTO i FROM invoices WHERE id=e.invoice_id;
 SELECT * INTO r FROM service_renewal_reversals WHERE effect_id=e.id;
 -- Payment proof is checked at transaction end: the balance ledger is written
 -- after the paid transition, whereas gateway and credit proofs precede it.
 valid:=EXISTS(SELECT 1 FROM payment_transactions p WHERE p.invoice_id=i.id AND p.user_id=e.user_id AND p.status='succeeded' AND p.amount_cents=i.amount_cents)
  OR EXISTS(SELECT 1 FROM balance_ledger l WHERE l.reference_type='invoice' AND l.reference_id=i.id AND l.user_id=e.user_id AND l.entry_type='payment' AND l.delta_cents=-i.amount_cents)
  OR EXISTS(SELECT 1 FROM credit_invoice_payments c WHERE c.invoice_id=i.id AND c.user_id=e.user_id AND c.amount_cents=i.amount_cents);
 IF NOT valid OR e.user_id<>i.user_id OR e.item_snapshot IS DISTINCT FROM (SELECT external_refund_order_snapshot(ii) FROM invoice_items ii WHERE ii.id=e.item_id) OR
   (r.effect_id IS NULL AND i.status<>'paid') OR (r.effect_id IS NOT NULL AND i.status<>'refunded') THEN RAISE EXCEPTION 'renewal_effect_incomplete' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='service_renewal_reversals' AND NOT EXISTS(SELECT 1 FROM services s WHERE s.id=e.service_id AND s.next_due_at IS NOT DISTINCT FROM e.before_due AND s.renewal_discounts_used=e.before_used AND NOT s.auto_renew AND s.renewal_generation=r.generation_after AND local_renewal_configuration(s)=e.configuration) THEN RAISE EXCEPTION 'renewal_refund_incomplete' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='service_renewal_effects' AND r.effect_id IS NULL AND NOT EXISTS(SELECT 1 FROM services s WHERE s.id=e.service_id AND s.next_due_at=e.after_due AND s.renewal_discounts_used=e.after_used AND local_renewal_configuration(s)=e.configuration) THEN RAISE EXCEPTION 'renewal_effect_incomplete' USING ERRCODE='23514'; END IF;
 IF r.effect_id IS NOT NULL THEN
  IF r.refund_id IS NOT NULL THEN
   IF NOT EXISTS(SELECT 1 FROM external_refunds ff JOIN external_refund_renewal_holds hh ON hh.refund_id=ff.id WHERE ff.id=r.refund_id AND ff.status='confirmed' AND hh.status='applied' AND hh.effect_id=e.id) THEN RAISE EXCEPTION 'renewal_refund_incomplete' USING ERRCODE='23514'; END IF;
  ELSIF NOT EXISTS(SELECT 1 FROM balance_ledger l WHERE l.reference_type='invoice' AND l.reference_id=i.id AND l.user_id=e.user_id AND l.entry_type='refund' AND l.delta_cents=i.amount_cents) AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments c WHERE c.invoice_id=i.id AND c.status='refunded' AND c.remaining_cents=0) THEN RAISE EXCEPTION 'renewal_refund_incomplete' USING ERRCODE='23514'; END IF;
 END IF;
 IF h.refund_id IS NOT NULL THEN
  SELECT * INTO f FROM external_refunds WHERE id=h.refund_id;
  IF h.invoice_id<>e.invoice_id OR h.service_id<>e.service_id OR h.user_id<>e.user_id OR f.invoice_id<>i.id OR f.payment_id<>h.payment_id OR f.user_id<>e.user_id OR f.amount_cents<>i.amount_cents OR NOT EXISTS(SELECT 1 FROM payment_transactions p WHERE p.id=h.payment_id AND p.invoice_id=i.id AND p.user_id=e.user_id AND p.status='succeeded' AND p.amount_cents=i.amount_cents) THEN RAISE EXCEPTION 'renewal_refund_incomplete' USING ERRCODE='23514'; END IF;
  IF h.status='pending' THEN
   IF f.status<>'pending_review' OR h.applied_at IS NOT NULL OR h.released_at IS NOT NULL OR h.invoice_snapshot<>external_refund_order_snapshot(i) OR NOT EXISTS(SELECT 1 FROM services s WHERE s.id=h.service_id AND h.service_snapshot=external_refund_order_snapshot(s)) THEN RAISE EXCEPTION 'renewal_refund_incomplete' USING ERRCODE='23514'; END IF;
  ELSIF h.status='released' THEN
   IF f.status<>'rejected' OR h.released_at IS NULL OR h.applied_at IS NOT NULL THEN RAISE EXCEPTION 'renewal_refund_incomplete' USING ERRCODE='23514'; END IF;
  ELSIF h.status='applied' THEN
   IF f.status<>'confirmed' OR r.refund_id IS DISTINCT FROM h.refund_id OR h.applied_at IS NULL OR h.released_at IS NOT NULL OR
    (external_refund_order_snapshot(i)-ARRAY['status','version','updated_at'])<>(h.invoice_snapshot-ARRAY['status','version','updated_at']) OR
    NOT EXISTS(SELECT 1 FROM audit_events WHERE action='external_refund.renewal_reversed' AND entity_type='external_refund' AND entity_id=f.id::text AND actor_id=f.reviewed_by)
   THEN RAISE EXCEPTION 'renewal_refund_incomplete' USING ERRCODE='23514'; END IF;
  ELSE RAISE EXCEPTION 'renewal_refund_incomplete' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER local_renewal_effect_complete AFTER INSERT ON service_renewal_effects DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_local_renewal_complete();
CREATE CONSTRAINT TRIGGER local_renewal_reversal_complete AFTER INSERT ON service_renewal_reversals DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_local_renewal_complete();
CREATE CONSTRAINT TRIGGER local_renewal_hold_complete AFTER INSERT OR UPDATE ON external_refund_renewal_holds DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_local_renewal_complete();
CREATE CONSTRAINT TRIGGER local_renewal_refund_complete AFTER INSERT OR UPDATE ON external_refunds DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_local_renewal_complete();
CREATE FUNCTION external_refund_renewal_reversal_json(target_refund bigint) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT jsonb_build_object('status',CASE h.status WHEN 'applying' THEN 'pending' ELSE h.status END,'effect_id',e.id,'service_id',e.service_id,'invoice_version',h.invoice_version,'service_version',h.service_version,'before_due',e.before_due,'after_due',e.after_due,'before_used',e.before_used,'after_used',e.after_used,'review_before',e.before_due,'created_at',h.created_at,'applied_at',h.applied_at,'released_at',h.released_at)
 FROM external_refund_renewal_holds h JOIN service_renewal_effects e ON e.id=h.effect_id WHERE h.refund_id=target_refund;
$$;
