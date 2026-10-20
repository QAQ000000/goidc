-- Manual inventory is independent of customer billing and product stock.
-- Stop old writers before upgrading API and worker together. No historical
-- service, money, credential, receipt or existing migration row is rewritten.
CREATE TABLE manual_resources (
 id bigserial PRIMARY KEY,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 120),
 resource_type text NOT NULL DEFAULT 'server' CHECK(length(resource_type) BETWEEN 1 AND 40),
 provider text NOT NULL DEFAULT '' CHECK(length(provider)<=120),
 region text NOT NULL DEFAULT '' CHECK(length(region)<=120),
 address text NOT NULL DEFAULT '' CHECK(length(address)<=256),
 upstream_id text NOT NULL DEFAULT '' CHECK(length(upstream_id)<=256),
 cost_cents bigint NOT NULL DEFAULT 0 CHECK(cost_cents>=0 AND cost_cents<=9000000000000000),
 currency text NOT NULL DEFAULT 'CNY' CHECK(currency='CNY'),
 upstream_expires_at timestamptz,
 status text NOT NULL DEFAULT 'available' CHECK(status IN ('available','disabled','retired')),
 notes text NOT NULL DEFAULT '' CHECK(length(notes)<=4000),
 credentials_encrypted bytea NOT NULL DEFAULT ''::bytea,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE manual_resource_bindings (
 id bigserial PRIMARY KEY,
 resource_id bigint NOT NULL REFERENCES manual_resources(id) ON DELETE RESTRICT,
 service_id bigint NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
 bound_by bigint NOT NULL REFERENCES administrators(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT now(),
 released_at timestamptz,
 released_by bigint REFERENCES administrators(id) ON DELETE RESTRICT,
 release_note text NOT NULL DEFAULT '',
 CHECK((released_at IS NULL AND released_by IS NULL AND release_note='') OR
       (released_at IS NOT NULL AND released_by IS NOT NULL AND length(release_note) BETWEEN 3 AND 2000))
);
CREATE UNIQUE INDEX manual_resource_active_binding ON manual_resource_bindings(resource_id) WHERE released_at IS NULL;
CREATE UNIQUE INDEX manual_service_active_binding ON manual_resource_bindings(service_id) WHERE released_at IS NULL;
CREATE INDEX manual_resource_binding_history ON manual_resource_bindings(resource_id,id DESC);
CREATE INDEX manual_resources_status ON manual_resources(status,id DESC);

-- A historical binding is immutable. Releasing it records who/when/why once;
-- it cannot be moved to another resource/service, reopened or physically erased.
CREATE FUNCTION preserve_manual_resource_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'manual resource binding history is immutable'; END IF;
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.resource_id IS DISTINCT FROM OLD.resource_id OR
 NEW.service_id IS DISTINCT FROM OLD.service_id OR NEW.bound_by IS DISTINCT FROM OLD.bound_by OR
 NEW.created_at IS DISTINCT FROM OLD.created_at OR OLD.released_at IS NOT NULL OR NEW.released_at IS NULL THEN
  RAISE EXCEPTION 'manual resource binding history is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER manual_resource_binding_preserve BEFORE UPDATE OR DELETE ON manual_resource_bindings
 FOR EACH ROW EXECUTE FUNCTION preserve_manual_resource_binding();
