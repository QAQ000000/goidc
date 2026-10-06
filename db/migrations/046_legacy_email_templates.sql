ALTER TABLE notification_templates ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE notification_templates ADD COLUMN legacy_id bigint;
ALTER TABLE notification_templates ADD COLUMN legacy_type text NOT NULL DEFAULT '';
ALTER TABLE notification_templates ADD COLUMN legacy_language text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX notification_templates_legacy_identity ON notification_templates(legacy_source,legacy_id)
 WHERE legacy_source<>'' AND legacy_id IS NOT NULL;
