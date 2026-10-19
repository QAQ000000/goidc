-- Full-catalog imports retain their original provider schema and local ID map.
-- Historical fixed selections remain unchanged and keep their single cycle.
CREATE TABLE supplier_catalog_imports (
 binding_id bigint PRIMARY KEY REFERENCES supplier_product_bindings(id) ON DELETE RESTRICT,
 configuration jsonb NOT NULL CHECK (jsonb_typeof(configuration)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK (coalesce(jsonb_typeof(configuration->'product')='object',false)),
 CHECK (coalesce(jsonb_typeof(configuration->'product'->'prices')='object',false)),
 CHECK (coalesce(jsonb_typeof(configuration->'options')='object',false)),
 CHECK (coalesce(jsonb_typeof(configuration->'fields')='object',false)),
 CHECK (coalesce(jsonb_typeof(configuration->'multiplier_bps')='number'
  AND (configuration->>'multiplier_bps')::numeric BETWEEN 1 AND 1000000,false))
);
CREATE FUNCTION guard_supplier_catalog_import() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (
  SELECT 1 FROM supplier_product_bindings b JOIN products p ON p.id=b.product_id
  WHERE b.id=NEW.binding_id AND p.provision_mode='manual' AND NOT p.active
   AND NEW.configuration->'product'->'id'=to_jsonb(b.upstream_product_id)
   AND NEW.configuration->'product'->'revision'=to_jsonb(b.upstream_revision)
   AND NEW.configuration->'product'->>'currency'=b.currency
   AND NEW.configuration->'product'->'supported'='true'::jsonb
 ) THEN
  RAISE EXCEPTION 'supplier catalog import identity is invalid' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER supplier_catalog_import_identity BEFORE INSERT ON supplier_catalog_imports
 FOR EACH ROW EXECUTE FUNCTION guard_supplier_catalog_import();
CREATE TRIGGER supplier_catalog_import_immutable BEFORE UPDATE OR DELETE ON supplier_catalog_imports
 FOR EACH ROW EXECUTE FUNCTION preserve_supplier_binding();

CREATE OR REPLACE FUNCTION guard_supplier_price_cycle() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.active AND EXISTS(
  SELECT 1 FROM supplier_product_bindings b
  LEFT JOIN supplier_catalog_imports ci ON ci.binding_id=b.id
  WHERE b.product_id=NEW.product_id AND (
   (ci.binding_id IS NULL AND b.billing_cycle<>NEW.billing_cycle)
   OR (ci.binding_id IS NOT NULL AND NOT coalesce((ci.configuration->'product'->'prices') ? NEW.billing_cycle,false))
  )
 ) THEN
  RAISE EXCEPTION 'supplier product billing cycle is bound';
 END IF;
 RETURN NEW;
END $$;
