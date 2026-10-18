-- Product categories are independent of service navigation groups.
ALTER TABLE product_categories ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE product_categories ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX product_categories_legacy_identity ON product_categories(legacy_source,legacy_id)
 WHERE legacy_source<>'' AND legacy_id IS NOT NULL;
ALTER TABLE product_categories ADD CONSTRAINT product_categories_legacy_identity_valid
 CHECK ((legacy_source='' AND legacy_id IS NULL) OR (legacy_source<>'' AND legacy_id>0));
