ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_action_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_action_check
 CHECK(action IN (
   'create','renew','suspend','resume','terminate','sync','upgrade','power_on','power_off','reboot','force_off','force_reboot','reset_password','reinstall',
   'snapshot_create','snapshot_delete','snapshot_restore','backup_create','backup_delete','backup_restore',
   'nat_acl_create','nat_acl_delete','nat_web_create','nat_web_delete','cdrom_mount','cdrom_unmount','rescue_system','exit_rescue_system'));

ALTER TABLE invoice_items ADD COLUMN supplier_renewal_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb
 CHECK(jsonb_typeof(supplier_renewal_snapshot)='object');

-- Preserve existing password/reinstall/storage checks and add a separate
-- credential-free immutable financial request for renewal.
DO $$
DECLARE existing_check text;
BEGIN
 SELECT pg_get_expr(conbin,conrelid) INTO STRICT existing_check FROM pg_constraint
 WHERE conrelid='provision_jobs'::regclass AND conname='provision_jobs_password_request_check';
 ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_password_request_check;
 EXECUTE 'ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_password_request_check CHECK ((action<>''renew'' AND ('||existing_check||')) OR (action=''renew'' AND request_credentials_encrypted IS NULL AND request_fingerprint='''' AND jsonb_typeof(request_payload)=''object'' AND request_payload ?& ARRAY[''supplier_id'',''account_version'',''local_invoice_id'',''upstream_id'',''product_id'',''cycle'',''currency'',''cost_cents'',''maximum_cost_cents'',''previous_due'']))';
END $$;

CREATE FUNCTION preserve_supplier_renewal_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (OLD.action='renew' OR NEW.action='renew') AND (
 NEW.action IS DISTINCT FROM OLD.action OR NEW.service_id IS DISTINCT FROM OLD.service_id OR
 NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR NEW.request_payload IS DISTINCT FROM OLD.request_payload) THEN
  RAISE EXCEPTION 'supplier renewal request is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER supplier_renewal_request_immutable BEFORE UPDATE ON provision_jobs
 FOR EACH ROW EXECUTE FUNCTION preserve_supplier_renewal_request();

CREATE FUNCTION preserve_supplier_renewal_quote() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.supplier_renewal_snapshot IS DISTINCT FROM OLD.supplier_renewal_snapshot THEN
  RAISE EXCEPTION 'supplier renewal quote is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER supplier_renewal_quote_immutable BEFORE UPDATE ON invoice_items
 FOR EACH ROW EXECUTE FUNCTION preserve_supplier_renewal_quote();
