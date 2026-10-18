-- Extend the immutable, credential-free lifecycle envelope. Existing jobs and
-- financial facts are retained; historical suspensions gain no new authority.
DO $$
DECLARE existing_check text;
BEGIN
 SELECT pg_get_expr(conbin,conrelid) INTO STRICT existing_check FROM pg_constraint
 WHERE conrelid='provision_jobs'::regclass AND conname='provision_jobs_password_request_check';
 ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_password_request_check;
 EXECUTE format('ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_password_request_check CHECK ((%s) OR (%s))', existing_check, $check$
 action='suspend' AND request_credentials_encrypted IS NULL AND request_fingerprint=''
 AND jsonb_typeof(request_payload)='object' AND COALESCE((
  request_payload->'system_expiry_suspension'='true'::jsonb
  AND (NOT request_payload ? 'manual_lifecycle' OR request_payload->'manual_lifecycle'='true'::jsonb)
  AND request_payload-ARRAY['manual_lifecycle','system_expiry_suspension','expiry_due_unix_micro']='{}'::jsonb
  AND CASE WHEN jsonb_typeof(request_payload->'expiry_due_unix_micro')='number'
    AND request_payload->>'expiry_due_unix_micro' ~ '^[1-9][0-9]{0,18}$'
    THEN (request_payload->>'expiry_due_unix_micro')::numeric<=9223372036854775807 ELSE false END
 ),false)
 $check$);
END $$;
