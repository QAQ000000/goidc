-- Approval closes renewal admission. Existing online receipts must settle
-- before approval; never discard an external receipt to implement cancellation.
ALTER TABLE service_cancellation_requests ADD COLUMN approval_generation bigint NOT NULL DEFAULT 0 CHECK (approval_generation>=0);
ALTER TABLE provision_jobs ADD COLUMN cancellation_request_id bigint REFERENCES service_cancellation_requests(id);
CREATE INDEX provision_jobs_cancellation_request ON provision_jobs(cancellation_request_id) WHERE cancellation_request_id IS NOT NULL;
UPDATE provision_jobs j SET cancellation_request_id=c.id
FROM service_cancellation_requests c
WHERE j.service_id=c.service_id AND j.action='terminate' AND j.idempotency_key='service-cancellation-'||c.id::text;

CREATE FUNCTION cancellation_renewal_payment_unresolved(target_service bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM invoices i WHERE EXISTS(
  SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=target_service AND ii.item_type='renew') AND (
  EXISTS(SELECT 1 FROM payment_transactions p WHERE p.invoice_id=i.id AND p.provider<>'mock' AND p.status IN ('pending','expired')) OR
  EXISTS(SELECT 1 FROM payment_close_operations o WHERE o.invoice_id=i.id AND payment_close_operation_unresolved(o.id)) OR
  EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id))));
$$;

-- Only untouched pending jobs can be reopened automatically. Preserve the old
-- task, its idempotency key and every payment fact; a later approval gets a new
-- generation. Ambiguous/dispatched history requires reconciliation before upgrade.
DO $$
DECLARE r record;
BEGIN
 FOR r IN
  SELECT c.id,c.service_id,c.resolved_at,j.id AS job_id,j.status AS job_status,j.external_started_at,
   to_jsonb(c) AS previous_request,to_jsonb(j) AS previous_job
  FROM service_cancellation_requests c
  LEFT JOIN provision_jobs j ON j.cancellation_request_id=c.id AND j.action='terminate'
  WHERE c.status='approved' AND (
   cancellation_renewal_payment_unresolved(c.service_id) OR
   EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id
    LEFT JOIN service_renewal_effects e ON e.invoice_id=i.id
    WHERE ii.service_id=c.service_id AND ii.item_type='renew' AND i.status='paid'
     AND (i.paid_at>c.resolved_at OR e.effective_at>c.resolved_at)))
  ORDER BY c.service_id,c.id,j.id
 LOOP
  IF r.job_id IS NULL OR r.job_status<>'pending' OR r.external_started_at IS NOT NULL OR
   EXISTS(SELECT 1 FROM service_cancellation_requests x WHERE x.service_id=r.service_id AND x.id<>r.id AND x.status IN ('pending','approved')) THEN
   RAISE EXCEPTION 'cancellation_renewal_requires_reconciliation' USING ERRCODE='23514',
    DETAIL=format('service_id=%s request_id=%s job_id=%s; reconcile resources and payments before retrying migration',r.service_id,r.id,r.job_id);
  END IF;
  INSERT INTO audit_events(action,entity_type,entity_id,payload)
  VALUES('service.cancellation.reopened_for_renewal','service',r.service_id::text,
   jsonb_build_object('request_id',r.id,'job_id',r.job_id,'reason','historical_renewal_conflict','previous_request',r.previous_request,'previous_job',r.previous_job));
  UPDATE provision_jobs SET status='cancelled',locked_at=NULL,completed_at=clock_timestamp(),updated_at=clock_timestamp(),
   last_error='历史取消与续费冲突，已停止原终止任务，等待重新审核' WHERE id=r.job_id;
  UPDATE service_cancellation_requests SET status='pending',resolved_at=NULL,resolved_by=NULL,
   resolution_note='历史取消与续费冲突，原任务已停止，请核实收款和最新权益后重新审核' WHERE id=r.id;
 END LOOP;
END $$;

CREATE FUNCTION assert_renewal_cancellation_open(target_service bigint) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 BEGIN
  PERFORM id FROM services WHERE id=target_service FOR UPDATE NOWAIT;
 EXCEPTION WHEN lock_not_available THEN
  RAISE EXCEPTION 'service_action_pending' USING ERRCODE='23514';
 END;
 IF EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=target_service AND status='approved') THEN
  RAISE EXCEPTION 'service_cancellation_approved' USING ERRCODE='23514';
 END IF;
END $$;

CREATE FUNCTION guard_cancelled_renewal_invoice() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_service bigint;
BEGIN
 IF OLD.status='unpaid' AND NEW.status='paid' THEN
  FOR target_service IN SELECT DISTINCT service_id FROM invoice_items WHERE invoice_id=NEW.id AND item_type='renew' AND service_id IS NOT NULL ORDER BY service_id LOOP
   PERFORM assert_renewal_cancellation_open(target_service);
  END LOOP;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER cancellation_renewal_invoice_guard BEFORE UPDATE OF status ON invoices FOR EACH ROW EXECUTE FUNCTION guard_cancelled_renewal_invoice();

CREATE FUNCTION guard_cancelled_renewal_intent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_service bigint;
BEGIN
 IF NEW.status IN ('pending','expired') THEN
  FOR target_service IN SELECT DISTINCT service_id FROM invoice_items WHERE invoice_id=NEW.invoice_id AND item_type='renew' AND service_id IS NOT NULL ORDER BY service_id LOOP
   PERFORM assert_renewal_cancellation_open(target_service);
  END LOOP;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER cancellation_renewal_intent_guard BEFORE INSERT ON payment_transactions FOR EACH ROW EXECUTE FUNCTION guard_cancelled_renewal_intent();

CREATE FUNCTION guard_cancellation_renewal_approval() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='approved' AND (TG_OP='INSERT' OR OLD.status IS DISTINCT FROM 'approved') THEN
  PERFORM assert_renewal_cancellation_open(NEW.service_id);
  IF cancellation_renewal_payment_unresolved(NEW.service_id) THEN
   RAISE EXCEPTION 'invoice_payment_pending' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER cancellation_renewal_approval_guard BEFORE INSERT OR UPDATE OF status ON service_cancellation_requests FOR EACH ROW EXECUTE FUNCTION guard_cancellation_renewal_approval();
