ALTER TABLE service_navigation_groups DROP CONSTRAINT service_navigation_groups_name_check;
ALTER TABLE service_navigation_groups ADD CONSTRAINT service_navigation_groups_name_check
 CHECK (length(name) BETWEEN 1 AND 255);
ALTER TABLE service_navigation_groups ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE service_navigation_groups ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX service_navigation_groups_legacy_identity ON service_navigation_groups(legacy_source,legacy_id)
 WHERE legacy_source<>'' AND legacy_id IS NOT NULL;

-- The customer/group pair stays the primary key. A stable internal ID permits
-- legacy row mappings and verification even after a customer changes visibility.
ALTER TABLE service_navigation_preferences ADD COLUMN id bigserial;
CREATE UNIQUE INDEX service_navigation_preferences_id ON service_navigation_preferences(id);
ALTER TABLE service_navigation_preferences ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE service_navigation_preferences ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX service_navigation_preferences_legacy_identity ON service_navigation_preferences(legacy_source,legacy_id)
 WHERE legacy_source<>'' AND legacy_id IS NOT NULL;
CREATE INDEX service_navigation_preferences_group ON service_navigation_preferences(group_id);
