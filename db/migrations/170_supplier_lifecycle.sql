-- Preserve historical requests; new lifecycle payloads carry no credentials.
-- Historical successful suspensions lack proven origin and are not backfilled.
DO $$
DECLARE existing_check text;
BEGIN
 SELECT pg_get_expr(conbin,conrelid) INTO STRICT existing_check FROM pg_constraint
 WHERE conrelid='provision_jobs'::regclass AND conname='provision_jobs_password_request_check';
 ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_password_request_check;
 EXECUTE format('ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_password_request_check CHECK ((%s) OR (%s))', existing_check, $check$
 action IN ('suspend','resume','terminate') AND request_credentials_encrypted IS NULL AND request_fingerprint=''
 AND jsonb_typeof(request_payload)='object' AND COALESCE((
   request_payload='{"manual_lifecycle":true}'::jsonb
   OR (action IN ('suspend','resume')
     AND (NOT request_payload ? 'manual_lifecycle' OR request_payload->'manual_lifecycle'='true'::jsonb)
     AND CASE WHEN jsonb_typeof(request_payload->'billing_invoice_id')='number'
       AND request_payload->>'billing_invoice_id' ~ '^[1-9][0-9]{0,18}$'
       THEN (request_payload->>'billing_invoice_id')::numeric <= 9223372036854775807 ELSE false END
     AND (
       (action='suspend' AND request_payload->'system_overdue_suspension'='true'::jsonb
        AND request_payload-ARRAY['manual_lifecycle','system_overdue_suspension','billing_invoice_id']='{}'::jsonb)
       OR (action='resume'
        AND request_payload-ARRAY['manual_lifecycle','billing_suspension_job_id','billing_invoice_id']='{}'::jsonb
        AND CASE WHEN jsonb_typeof(request_payload->'billing_suspension_job_id')='number'
          AND request_payload->>'billing_suspension_job_id' ~ '^[1-9][0-9]{0,18}$'
          THEN (request_payload->>'billing_suspension_job_id')::numeric <= 9223372036854775807 ELSE false END)
     ))),false)
 $check$);
END $$;

CREATE FUNCTION preserve_supplier_lifecycle_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.action IN ('suspend','resume','terminate') OR NEW.action IN ('suspend','resume','terminate') THEN
  IF NEW.action IS DISTINCT FROM OLD.action OR NEW.service_id IS DISTINCT FROM OLD.service_id OR
   NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR
   (NEW.request_payload IS DISTINCT FROM OLD.request_payload AND NOT (
    OLD.status='running' AND OLD.external_started_at IS NULL AND NEW.status='waiting_manual'
    AND NEW.request_payload=OLD.request_payload||'{"manual_lifecycle":true}'::jsonb)) THEN
   RAISE EXCEPTION 'supplier lifecycle request is immutable';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER supplier_lifecycle_request_immutable BEFORE UPDATE ON provision_jobs
 FOR EACH ROW EXECUTE FUNCTION preserve_supplier_lifecycle_request();
