ALTER TABLE customer_groups ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE customer_groups ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX customer_groups_legacy_identity ON customer_groups(legacy_source,legacy_id)
 WHERE legacy_source<>'' AND legacy_id IS NOT NULL;

ALTER TABLE customer_contacts ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE customer_contacts ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX customer_contacts_legacy_identity ON customer_contacts(legacy_source,legacy_id)
 WHERE legacy_source<>'' AND legacy_id IS NOT NULL;
