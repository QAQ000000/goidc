-- Lock the catalogue revision while validating a profile, including concurrent
-- inserts and deletes. Definition writers increment it in the same transaction.
CREATE TABLE customer_field_catalogue (
 id smallint PRIMARY KEY CHECK(id=1),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0)
);
INSERT INTO customer_field_catalogue(id) VALUES(1);
CREATE FUNCTION advance_customer_field_catalogue() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE customer_field_catalogue SET version=version+1 WHERE id=1;
 RETURN NULL;
END
$$;
CREATE TRIGGER customer_field_catalogue_revision BEFORE INSERT OR UPDATE OR DELETE ON customer_custom_fields
 FOR EACH STATEMENT EXECUTE FUNCTION advance_customer_field_catalogue();
-- All customers have a real profile version, including pre-existing customers.
INSERT INTO customer_profiles(customer_id,company_name,phone)
SELECT u.id,COALESCE((SELECT c.company_name FROM customer_contacts c WHERE c.customer_id=u.id AND c.company_name<>'' ORDER BY c.id LIMIT 1),''),COALESCE((SELECT c.phone_number FROM customer_contacts c WHERE c.customer_id=u.id AND c.phone_number<>'' ORDER BY c.id LIMIT 1),'') FROM users u WHERE role='customer' ON CONFLICT DO NOTHING;
