-- Existing manual applications, reviews and ciphertext remain untouched.
LOCK TABLE channel_instances IN SHARE ROW EXCLUSIVE MODE;
DO $$ DECLARE chosen_name text := '人工实名认证'; suffix integer := 0; BEGIN
 IF NOT EXISTS(SELECT 1 FROM channel_instances WHERE id='manual-default') THEN
  WHILE EXISTS(SELECT 1 FROM channel_instances WHERE category='certification'
   AND implementation_id='manual' AND display_name=chosen_name) LOOP
   suffix := suffix+1;
   chosen_name := '人工实名认证（默认 '||suffix::text||'）';
  END LOOP;
  INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config,schema_version)
   VALUES('manual-default','certification','manual',chosen_name,true,'{}',1);
 END IF;
END $$;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM channel_instances WHERE id='manual-default'
  AND category='certification' AND implementation_id='manual') THEN
  RAISE EXCEPTION 'manual-default is occupied by a different channel';
 END IF;
END $$;

ALTER TABLE certification_settings
 ADD COLUMN default_instance_id text NOT NULL DEFAULT 'manual-default'
 REFERENCES channel_instances(id) ON DELETE RESTRICT;

ALTER TABLE certification_submissions
 ADD COLUMN channel_instance_id text REFERENCES channel_instances(id) ON DELETE RESTRICT,
 ADD COLUMN channel_instance_version bigint NOT NULL DEFAULT 0 CHECK(channel_instance_version>=0),
 ADD COLUMN channel_schema_version integer NOT NULL DEFAULT 1 CHECK(channel_schema_version>0),
 ADD COLUMN profile_version bigint NOT NULL DEFAULT 0 CHECK(profile_version>=0),
 ADD COLUMN request_id text NOT NULL DEFAULT '' CHECK(char_length(request_id)<=128),
 ADD COLUMN config_snapshot_encrypted bytea NOT NULL DEFAULT ''::bytea,
 ADD COLUMN config_snapshot_aad text NOT NULL DEFAULT '',
 ADD CONSTRAINT certification_submission_binding CHECK(
  (channel_instance_id IS NULL AND channel_instance_version=0 AND profile_version=0
   AND request_id='' AND octet_length(config_snapshot_encrypted)=0 AND config_snapshot_aad='')
  OR (channel_instance_id IS NOT NULL AND channel_instance_version>0 AND profile_version>0
   AND request_id<>'' AND octet_length(config_snapshot_encrypted)>0 AND config_snapshot_aad<>''));
CREATE UNIQUE INDEX certification_submission_request
 ON certification_submissions(channel_instance_id,request_id) WHERE request_id<>'';

CREATE FUNCTION protect_certification_submission_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.profile_id,NEW.user_id,NEW.certification_type,NEW.provider_id,
     NEW.payload_encrypted,NEW.payload_aad,NEW.channel_instance_id,
     NEW.channel_instance_version,NEW.channel_schema_version,NEW.profile_version,
     NEW.request_id,NEW.config_snapshot_encrypted,NEW.config_snapshot_aad)
 IS DISTINCT FROM
    (OLD.profile_id,OLD.user_id,OLD.certification_type,OLD.provider_id,
     OLD.payload_encrypted,OLD.payload_aad,OLD.channel_instance_id,
     OLD.channel_instance_version,OLD.channel_schema_version,OLD.profile_version,
     OLD.request_id,OLD.config_snapshot_encrypted,OLD.config_snapshot_aad)
 THEN RAISE EXCEPTION 'certification application binding is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER certification_submission_binding_immutable BEFORE UPDATE ON certification_submissions
 FOR EACH ROW EXECUTE FUNCTION protect_certification_submission_binding();

CREATE TABLE certification_provider_jobs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 submission_id bigint NOT NULL UNIQUE REFERENCES certification_submissions(id) ON DELETE CASCADE,
 status text NOT NULL DEFAULT 'queued' CHECK(status IN
  ('queued','submitting','querying','waiting','uncertain','needs_review','done','cancelled')),
 submit_attempts integer NOT NULL DEFAULT 0 CHECK(submit_attempts BETWEEN 0 AND 1),
 query_attempts integer NOT NULL DEFAULT 0 CHECK(query_attempts BETWEEN 0 AND 5),
 submit_started_at timestamptz,
 next_run_at timestamptz NOT NULL DEFAULT now(),
 lease_token text,
 lease_until timestamptz,
 code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK((status IN ('submitting','querying') AND lease_token IS NOT NULL AND lease_until IS NOT NULL)
  OR (status NOT IN ('submitting','querying') AND lease_token IS NULL AND lease_until IS NULL)),
 CHECK(status<>'queued' OR (submit_attempts=0 AND submit_started_at IS NULL))
);
CREATE INDEX certification_provider_jobs_ready ON certification_provider_jobs(next_run_at,id)
 WHERE status IN ('queued','waiting','uncertain','submitting','querying');

ALTER TABLE certification_provider_results
 ADD COLUMN applied boolean NOT NULL DEFAULT false,
 ADD COLUMN ignore_reason text NOT NULL DEFAULT '',
 ADD COLUMN job_lease_token text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX certification_provider_result_lease
 ON certification_provider_results(submission_id,job_lease_token) WHERE job_lease_token<>'';
