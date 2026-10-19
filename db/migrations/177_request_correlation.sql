-- Diagnostic correlation is independent of business request/idempotency IDs.
-- NULL means historical/unknown; no origin is fabricated during this upgrade.
ALTER TABLE audit_events ADD COLUMN correlation_id text
 CHECK(correlation_id IS NULL OR correlation_id ~ '^[0-9a-f]{32}$');
ALTER TABLE admin_api_logs ADD COLUMN correlation_id text
 CHECK(correlation_id IS NULL OR correlation_id ~ '^[0-9a-f]{32}$');
ALTER TABLE provision_jobs ADD COLUMN correlation_id text
 CHECK(correlation_id IS NULL OR correlation_id ~ '^[0-9a-f]{32}$');
ALTER TABLE email_outbox ADD COLUMN correlation_id text
 CHECK(correlation_id IS NULL OR correlation_id ~ '^[0-9a-f]{32}$');
ALTER TABLE sms_outbox ADD COLUMN correlation_id text
 CHECK(correlation_id IS NULL OR correlation_id ~ '^[0-9a-f]{32}$');
ALTER TABLE notification_events ADD COLUMN correlation_id text
 CHECK(correlation_id IS NULL OR correlation_id ~ '^[0-9a-f]{32}$');
ALTER TABLE notification_deliveries ADD COLUMN correlation_id text
 CHECK(correlation_id IS NULL OR correlation_id ~ '^[0-9a-f]{32}$');
ALTER TABLE certification_provider_jobs ADD COLUMN correlation_id text
 CHECK(correlation_id IS NULL OR correlation_id ~ '^[0-9a-f]{32}$');
ALTER TABLE scheduler_runs ADD COLUMN correlation_id text
 CHECK(correlation_id IS NULL OR correlation_id ~ '^[0-9a-f]{32}$');

-- Defaults apply only to new rows, even when migration has a correlation ID.
ALTER TABLE audit_events ALTER COLUMN correlation_id SET DEFAULT NULLIF(current_setting('goidc.correlation_id',true),'');
ALTER TABLE admin_api_logs ALTER COLUMN correlation_id SET DEFAULT NULLIF(current_setting('goidc.correlation_id',true),'');
ALTER TABLE provision_jobs ALTER COLUMN correlation_id SET DEFAULT NULLIF(current_setting('goidc.correlation_id',true),'');
ALTER TABLE email_outbox ALTER COLUMN correlation_id SET DEFAULT NULLIF(current_setting('goidc.correlation_id',true),'');
ALTER TABLE sms_outbox ALTER COLUMN correlation_id SET DEFAULT NULLIF(current_setting('goidc.correlation_id',true),'');
ALTER TABLE notification_events ALTER COLUMN correlation_id SET DEFAULT NULLIF(current_setting('goidc.correlation_id',true),'');
ALTER TABLE certification_provider_jobs ALTER COLUMN correlation_id SET DEFAULT NULLIF(current_setting('goidc.correlation_id',true),'');
ALTER TABLE scheduler_runs ALTER COLUMN correlation_id SET DEFAULT NULLIF(current_setting('goidc.correlation_id',true),'');

-- Fanout may happen after the originating request. The event, rather than the
-- current poller or administrative retry, owns the delivery's diagnostic origin.
CREATE FUNCTION notification_delivery_correlation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 SELECT correlation_id INTO NEW.correlation_id FROM notification_events WHERE id=NEW.event_id;
 RETURN NEW;
END;
$$;
CREATE TRIGGER notification_delivery_correlation BEFORE INSERT ON notification_deliveries
 FOR EACH ROW EXECUTE FUNCTION notification_delivery_correlation();

CREATE INDEX audit_events_correlation ON audit_events(correlation_id) WHERE correlation_id IS NOT NULL;
CREATE INDEX admin_api_logs_correlation ON admin_api_logs(correlation_id) WHERE correlation_id IS NOT NULL;
