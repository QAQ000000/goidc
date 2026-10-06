ALTER TABLE services ADD COLUMN installed_os jsonb NOT NULL DEFAULT '{}'
 CHECK(jsonb_typeof(installed_os)='object');
ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_action_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_action_check
 CHECK(action IN ('create','suspend','resume','terminate','sync','upgrade',
 'power_on','power_off','reboot','force_off','force_reboot','reset_password','reinstall'));
ALTER TABLE provision_jobs ADD COLUMN request_payload jsonb NOT NULL DEFAULT '{}';
ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_password_request_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_password_request_check CHECK(
 (action='reset_password' AND request_fingerprint<>'' AND request_payload='{}'::jsonb AND
   ((status IN ('pending','running','waiting_verification') AND request_credentials_encrypted IS NOT NULL)
    OR (status IN ('succeeded','failed','cancelled') AND request_credentials_encrypted IS NULL)))
 OR (action='reinstall' AND request_fingerprint<>'' AND request_credentials_encrypted IS NULL
     AND jsonb_typeof(request_payload)='object' AND request_payload ?& ARRAY['id','name','group','login'])
 OR (action NOT IN ('reset_password','reinstall') AND request_fingerprint='' AND request_credentials_encrypted IS NULL AND request_payload='{}'::jsonb));

CREATE FUNCTION preserve_provision_reinstall_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (OLD.action='reinstall' OR NEW.action='reinstall') AND (
    NEW.action IS DISTINCT FROM OLD.action OR NEW.service_id IS DISTINCT FROM OLD.service_id OR
    NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR
    NEW.request_fingerprint IS DISTINCT FROM OLD.request_fingerprint OR
    NEW.request_payload IS DISTINCT FROM OLD.request_payload) THEN
  RAISE EXCEPTION 'system reinstall request is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER provision_reinstall_request_immutable BEFORE UPDATE ON provision_jobs
 FOR EACH ROW EXECUTE FUNCTION preserve_provision_reinstall_request();
