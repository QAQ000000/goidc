ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_action_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_action_check
  CHECK(action IN ('create','suspend','resume','terminate','sync','upgrade',
    'power_on','power_off','reboot','force_off','force_reboot','reset_password'));
ALTER TABLE provision_jobs ADD COLUMN request_credentials_encrypted bytea;
ALTER TABLE provision_jobs ADD COLUMN request_fingerprint text NOT NULL DEFAULT ''
  CHECK(request_fingerprint='' OR request_fingerprint ~ '^[0-9a-f]{64}$');
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_password_request_check CHECK(
  (action='reset_password' AND request_fingerprint<>'' AND
    ((status IN ('pending','running','waiting_verification') AND request_credentials_encrypted IS NOT NULL)
      OR (status IN ('succeeded','failed','cancelled') AND request_credentials_encrypted IS NULL)))
  OR (action<>'reset_password' AND request_fingerprint='' AND request_credentials_encrypted IS NULL));

CREATE FUNCTION preserve_provision_password_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.action='reset_password' AND (
    NEW.action IS DISTINCT FROM OLD.action OR NEW.service_id IS DISTINCT FROM OLD.service_id OR
    NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR
    NEW.request_fingerprint IS DISTINCT FROM OLD.request_fingerprint OR
    (NEW.request_credentials_encrypted IS DISTINCT FROM OLD.request_credentials_encrypted AND
     NEW.status NOT IN ('succeeded','failed','cancelled'))) THEN
  RAISE EXCEPTION 'password reset request is immutable';
 END IF;
 IF NEW.action='reset_password' AND NEW.status IN ('succeeded','failed','cancelled') THEN
  NEW.request_credentials_encrypted := NULL;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER provision_password_request_immutable BEFORE UPDATE ON provision_jobs
 FOR EACH ROW EXECUTE FUNCTION preserve_provision_password_request();
