CREATE INDEX audit_events_entity_timeline
 ON audit_events(entity_type,entity_id,created_at DESC,id DESC);
