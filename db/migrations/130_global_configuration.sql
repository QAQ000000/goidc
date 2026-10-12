-- Shared definitions continuously project into the existing pricing catalog.
CREATE TABLE global_configuration_groups (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 120),
 description text NOT NULL DEFAULT '',
 definitions jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(definitions)='array'),
 version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE global_configuration_assignments (
 global_group_id bigint NOT NULL REFERENCES global_configuration_groups(id),
 product_id bigint NOT NULL REFERENCES products(id) ON DELETE CASCADE,
 PRIMARY KEY(global_group_id,product_id)
);
ALTER TABLE product_option_groups ADD COLUMN global_group_id bigint REFERENCES global_configuration_groups(id);
CREATE UNIQUE INDEX global_configuration_projection ON product_option_groups(global_group_id,product_id,slug) WHERE global_group_id IS NOT NULL;
CREATE FUNCTION protect_global_configuration_projection() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE linked bigint;
BEGIN
 IF current_setting('zjmfgo.global_configuration_write',true) = 'on' THEN RETURN COALESCE(NEW,OLD); END IF;
 IF TG_TABLE_NAME='product_option_groups' THEN
  IF TG_OP<>'INSERT' THEN linked:=OLD.global_group_id; END IF;
  IF TG_OP<>'DELETE' AND NEW.global_group_id IS NOT NULL THEN linked:=NEW.global_group_id; END IF;
 ELSIF TG_TABLE_NAME='product_options' THEN
  IF TG_OP<>'INSERT' THEN SELECT global_group_id INTO linked FROM product_option_groups WHERE id=OLD.group_id; END IF;
  IF linked IS NULL AND TG_OP<>'DELETE' THEN SELECT global_group_id INTO linked FROM product_option_groups WHERE id=NEW.group_id; END IF;
 ELSE
  IF TG_OP<>'INSERT' THEN SELECT g.global_group_id INTO linked FROM product_options o JOIN product_option_groups g ON g.id=o.group_id WHERE o.id=OLD.option_id; END IF;
  IF linked IS NULL AND TG_OP<>'DELETE' THEN SELECT g.global_group_id INTO linked FROM product_options o JOIN product_option_groups g ON g.id=o.group_id WHERE o.id=NEW.option_id; END IF;
 END IF;
 IF linked IS NOT NULL THEN RAISE EXCEPTION 'global_configuration_managed' USING ERRCODE='23514'; END IF;
 RETURN COALESCE(NEW,OLD);
END $$;
CREATE TRIGGER protect_global_group BEFORE INSERT OR UPDATE OR DELETE ON product_option_groups FOR EACH ROW EXECUTE FUNCTION protect_global_configuration_projection();
CREATE TRIGGER protect_global_option BEFORE INSERT OR UPDATE OR DELETE ON product_options FOR EACH ROW EXECUTE FUNCTION protect_global_configuration_projection();
CREATE TRIGGER protect_global_price BEFORE INSERT OR UPDATE OR DELETE ON product_option_prices FOR EACH ROW EXECUTE FUNCTION protect_global_configuration_projection();
CREATE TRIGGER protect_global_tier BEFORE INSERT OR UPDATE OR DELETE ON product_option_price_tiers FOR EACH ROW EXECUTE FUNCTION protect_global_configuration_projection();
