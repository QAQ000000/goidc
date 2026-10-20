-- Stop old membership writers before upgrading. This metadata is independent
-- of sales ownership, customer profiles and immutable purchase price snapshots.
-- A separate row avoids a pricing-lock -> users-row-lock dependency: checkout
-- and administrator ordering can already hold a users SHARE lock before pricing.
CREATE TABLE customer_group_membership_versions (
 customer_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0)
);
INSERT INTO customer_group_membership_versions(customer_id) SELECT id FROM users;

CREATE FUNCTION initialize_customer_group_membership_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO customer_group_membership_versions(customer_id) VALUES(NEW.id);
 RETURN NEW;
END $$;
CREATE TRIGGER customer_group_membership_version_birth AFTER INSERT ON users
 FOR EACH ROW EXECUTE FUNCTION initialize_customer_group_membership_version();

CREATE FUNCTION advance_customer_group_membership_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW IS NOT DISTINCT FROM OLD THEN RETURN NEW; END IF;
 IF TG_OP IN ('DELETE','UPDATE') AND
    (TG_OP='DELETE' OR OLD.customer_id IS DISTINCT FROM NEW.customer_id OR OLD.group_id IS DISTINCT FROM NEW.group_id) THEN
  UPDATE customer_group_membership_versions SET version=version+1 WHERE customer_id=OLD.customer_id;
 END IF;
 IF TG_OP='INSERT' OR (TG_OP='UPDATE' AND OLD.customer_id IS DISTINCT FROM NEW.customer_id) THEN
  UPDATE customer_group_membership_versions SET version=version+1 WHERE customer_id=NEW.customer_id;
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER customer_group_membership_version_change AFTER INSERT OR UPDATE OR DELETE ON customer_group_members
 FOR EACH ROW EXECUTE FUNCTION advance_customer_group_membership_version();
