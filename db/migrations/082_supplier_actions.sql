ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_action_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_action_check
 CHECK(action IN (
   'create','suspend','resume','terminate','sync','upgrade','power_on','power_off','reboot','force_off','force_reboot','reset_password','reinstall',
   'snapshot_create','snapshot_delete','snapshot_restore','backup_create','backup_delete','backup_restore',
   'nat_acl_create','nat_acl_delete','nat_web_create','nat_web_delete','cdrom_mount','cdrom_unmount','rescue_system'));

ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_password_request_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_password_request_check CHECK(
 (action='reset_password' AND request_fingerprint<>'' AND request_payload='{}'::jsonb
   AND ((status IN ('pending','running','waiting_verification') AND request_credentials_encrypted IS NOT NULL)
    OR (status IN ('succeeded','failed','cancelled') AND request_credentials_encrypted IS NULL)))
 OR (action='rescue_system' AND request_fingerprint<>'' AND jsonb_typeof(request_payload)='object'
   AND jsonb_typeof(request_payload->'system')='number' AND request_payload->>'system' IN ('1','2')
   AND ((status IN ('pending','running','waiting_verification') AND request_credentials_encrypted IS NOT NULL)
    OR (status IN ('succeeded','failed','cancelled') AND request_credentials_encrypted IS NULL)))
 OR (action='reinstall' AND request_fingerprint<>'' AND request_credentials_encrypted IS NULL
     AND jsonb_typeof(request_payload)='object' AND request_payload ?& ARRAY['id','name','group','login'])
 OR (action IN ('snapshot_create','snapshot_delete','snapshot_restore','backup_create','backup_delete','backup_restore')
 AND request_fingerprint<>'' AND request_credentials_encrypted IS NULL AND jsonb_typeof(request_payload)='object'
 AND request_payload ?& ARRAY['kind','operation','point_id','profile_id','upstream_id','limit'])
 OR (action IN ('nat_acl_create','nat_acl_delete','nat_web_create','nat_web_delete')
 AND request_fingerprint<>'' AND request_credentials_encrypted IS NULL AND jsonb_typeof(request_payload)='object'
 AND request_payload ?& ARRAY['kind','operation','point_id','profile_id','upstream_id','limit']
 AND action='nat_'||(request_payload->>'kind')||'_'||(request_payload->>'operation')
 AND (request_payload->>'kind') IN ('acl','web') AND (request_payload->>'operation') IN ('create','delete')
 AND jsonb_typeof(request_payload->'point_id')='string'
 AND jsonb_typeof(request_payload->'profile_id')='string'
 AND jsonb_typeof(request_payload->'upstream_id')='string'
 AND jsonb_typeof(request_payload->'limit')='number'
 AND (request_payload->>'limit')::bigint BETWEEN 1 AND 100000000
 AND ((request_payload->>'operation'='create' AND request_payload->>'point_id'='' AND NOT request_payload ? 'point'
 AND jsonb_typeof(request_payload->'interior_port')='number')
 OR (request_payload->>'operation'='delete' AND request_payload->>'point_id'<>'' AND jsonb_typeof(request_payload->'point')='object')))
 OR (action IN ('cdrom_mount','cdrom_unmount') AND request_fingerprint<>'' AND request_credentials_encrypted IS NULL
 AND jsonb_typeof(request_payload)='object' AND request_payload ?& ARRAY['operation','image_id','profile_id','upstream_id','image','mounted_id']
 AND action='cdrom_'||(request_payload->>'operation')
 AND (request_payload->>'operation') IN ('mount','unmount')
 AND jsonb_typeof(request_payload->'image_id')='string'
 AND jsonb_typeof(request_payload->'profile_id')='string'
 AND jsonb_typeof(request_payload->'upstream_id')='string'
 AND jsonb_typeof(request_payload->'image')='object'
 AND request_payload->'image'->>'id'=request_payload->>'image_id'
 AND jsonb_typeof(request_payload->'mounted_id')='string')
 OR (action NOT IN ('reset_password','rescue_system','reinstall','snapshot_create','snapshot_delete','snapshot_restore','backup_create','backup_delete','backup_restore',
 'nat_acl_create','nat_acl_delete','nat_web_create','nat_web_delete','cdrom_mount','cdrom_unmount')
 AND request_fingerprint='' AND request_credentials_encrypted IS NULL AND request_payload='{}'::jsonb));

CREATE OR REPLACE FUNCTION preserve_provision_password_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.action IN ('reset_password','rescue_system') AND (
    NEW.action IS DISTINCT FROM OLD.action OR NEW.service_id IS DISTINCT FROM OLD.service_id OR
    NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR NEW.request_fingerprint IS DISTINCT FROM OLD.request_fingerprint OR
    NEW.request_payload IS DISTINCT FROM OLD.request_payload OR
    (NEW.request_credentials_encrypted IS DISTINCT FROM OLD.request_credentials_encrypted AND
     NEW.status NOT IN ('succeeded','failed','cancelled'))) THEN
  RAISE EXCEPTION 'sensitive supplier request is immutable';
 END IF;
 IF NEW.action IN ('reset_password','rescue_system') AND NEW.status IN ('succeeded','failed','cancelled') THEN
  NEW.request_credentials_encrypted := NULL;
 END IF;
 RETURN NEW;
END $$;
