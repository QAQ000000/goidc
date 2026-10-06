ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_action_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_action_check CHECK(action IN (
 'create','suspend','resume','terminate','sync','upgrade','power_on','power_off','reboot','force_off','force_reboot','reset_password','reinstall',
 'snapshot_create','snapshot_delete','snapshot_restore','backup_create','backup_delete','backup_restore'));
ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_password_request_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_password_request_check CHECK(
 (action='reset_password' AND request_fingerprint<>'' AND request_payload='{}'::jsonb AND
 ((status IN ('pending','running','waiting_verification') AND request_credentials_encrypted IS NOT NULL)
 OR (status IN ('succeeded','failed','cancelled') AND request_credentials_encrypted IS NULL)))
 OR (action='reinstall' AND request_fingerprint<>'' AND request_credentials_encrypted IS NULL
 AND jsonb_typeof(request_payload)='object' AND request_payload ?& ARRAY['id','name','group','login'])
 OR (action IN ('snapshot_create','snapshot_delete','snapshot_restore','backup_create','backup_delete','backup_restore')
 AND request_fingerprint<>'' AND request_credentials_encrypted IS NULL AND jsonb_typeof(request_payload)='object'
 AND request_payload ?& ARRAY['kind','operation','point_id','profile_id','upstream_id','limit'])
 OR (action NOT IN ('reset_password','reinstall','snapshot_create','snapshot_delete','snapshot_restore','backup_create','backup_delete','backup_restore')
 AND request_fingerprint='' AND request_credentials_encrypted IS NULL AND request_payload='{}'::jsonb));
CREATE FUNCTION preserve_provision_storage_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (OLD.action LIKE 'snapshot_%' OR OLD.action LIKE 'backup_%' OR NEW.action LIKE 'snapshot_%' OR NEW.action LIKE 'backup_%') AND (
 NEW.action IS DISTINCT FROM OLD.action OR NEW.service_id IS DISTINCT FROM OLD.service_id OR
 NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR NEW.request_fingerprint IS DISTINCT FROM OLD.request_fingerprint OR
 NEW.request_payload IS DISTINCT FROM OLD.request_payload) THEN
 RAISE EXCEPTION 'storage request is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER provision_storage_request_immutable BEFORE UPDATE ON provision_jobs
 FOR EACH ROW EXECUTE FUNCTION preserve_provision_storage_request();
