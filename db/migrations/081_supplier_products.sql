ALTER TABLE products DROP CONSTRAINT products_provision_mode_check;
ALTER TABLE products ADD CONSTRAINT products_provision_mode_check CHECK (provision_mode IN ('manual','simulation','nokvm','supplier'));
ALTER TABLE services DROP CONSTRAINT services_provision_mode_check;
ALTER TABLE services ADD CONSTRAINT services_provision_mode_check CHECK (provision_mode IN ('manual','simulation','nokvm','supplier'));

CREATE TABLE supplier_accounts (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 180),
 provider text NOT NULL,
 base_url text NOT NULL,
 username text NOT NULL,
 credentials_encrypted bytea NOT NULL,
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1,
 running_job_id bigint REFERENCES provision_jobs(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(provider,base_url,username)
);
CREATE TABLE supplier_product_bindings (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 product_id bigint NOT NULL UNIQUE REFERENCES products(id),
 supplier_id bigint NOT NULL REFERENCES supplier_accounts(id),
 provider text NOT NULL,
 upstream_product_id bigint NOT NULL CHECK (upstream_product_id>0),
 upstream_revision text NOT NULL,
 billing_cycle text NOT NULL CHECK (billing_cycle IN ('monthly','quarterly','annually','once')),
 currency text NOT NULL CHECK (currency='CNY'),
 maximum_cost_cents bigint NOT NULL CHECK (maximum_cost_cents BETWEEN 0 AND 100000000),
 config_options jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(config_options)='object'),
 custom_fields_encrypted bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION preserve_supplier_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'supplier product binding is immutable';
END $$;
CREATE TRIGGER supplier_binding_immutable BEFORE UPDATE OR DELETE ON supplier_product_bindings FOR EACH ROW EXECUTE FUNCTION preserve_supplier_binding();
CREATE UNIQUE INDEX services_supplier_resource ON services((delivery_snapshot->>'supplier_id'),upstream_id)
 WHERE provision_mode='supplier' AND upstream_id<>'';

CREATE FUNCTION guard_supplier_product_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.provision_mode='supplier' AND (NEW.provision_mode<>OLD.provision_mode OR NEW.delivery_config<>OLD.delivery_config OR NEW.billing_cycle<>OLD.billing_cycle) THEN
  RAISE EXCEPTION 'supplier product source is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER products_supplier_source BEFORE UPDATE ON products FOR EACH ROW EXECUTE FUNCTION guard_supplier_product_source();
CREATE FUNCTION guard_supplier_price_cycle() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.active AND EXISTS(SELECT 1 FROM supplier_product_bindings b WHERE b.product_id=NEW.product_id AND b.billing_cycle<>NEW.billing_cycle) THEN
  RAISE EXCEPTION 'supplier product billing cycle is bound';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER supplier_price_cycle BEFORE INSERT OR UPDATE ON product_prices FOR EACH ROW EXECUTE FUNCTION guard_supplier_price_cycle();
