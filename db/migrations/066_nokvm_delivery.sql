ALTER TABLE products DROP CONSTRAINT products_provision_mode_check;
ALTER TABLE products ADD CONSTRAINT products_provision_mode_check CHECK (provision_mode IN ('manual','simulation','nokvm'));
ALTER TABLE services DROP CONSTRAINT services_provision_mode_check;
ALTER TABLE services ADD CONSTRAINT services_provision_mode_check CHECK (provision_mode IN ('manual','simulation','nokvm'));
ALTER TABLE products ADD COLUMN delivery_config jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(delivery_config)='object');
ALTER TABLE services ADD COLUMN delivery_snapshot jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(delivery_snapshot)='object');
ALTER TABLE services ADD COLUMN resource_info jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(resource_info)='object');
ALTER TABLE services ADD COLUMN credentials_encrypted bytea;
CREATE UNIQUE INDEX services_nokvm_resource ON services((delivery_snapshot->>'profile_id'),upstream_id)
 WHERE provision_mode='nokvm' AND upstream_id<>'';
CREATE FUNCTION preserve_service_delivery_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.delivery_snapshot IS DISTINCT FROM OLD.delivery_snapshot THEN
  RAISE EXCEPTION 'service delivery snapshot is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER services_delivery_snapshot_immutable BEFORE UPDATE ON services FOR EACH ROW EXECUTE FUNCTION preserve_service_delivery_snapshot();
ALTER TABLE provision_jobs ADD COLUMN external_started_at timestamptz;
ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_status_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_status_check CHECK (status IN ('pending','running','waiting_manual','waiting_verification','succeeded','failed','cancelled'));
DROP INDEX provision_jobs_one_inflight;
CREATE UNIQUE INDEX provision_jobs_one_inflight ON provision_jobs(service_id) WHERE status IN ('pending','running','waiting_manual','waiting_verification');
