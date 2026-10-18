-- Existing bytea payloads remain in place. External objects are attached only
-- after an independently committed, immutable upload journal records them.
-- staging proves Put has not started; uncertain is committed before Put and
-- cannot be automatically collected, including after process termination.
CREATE TABLE file_storage_objects (
 id text PRIMARY KEY CHECK (id ~ '^[a-f0-9]{64}$'),
 profile_id text NOT NULL CHECK (length(profile_id) BETWEEN 1 AND 100 AND profile_id<>'database'),
 location_sha256 text NOT NULL CHECK (location_sha256 ~ '^[a-f0-9]{64}$'),
 kind text NOT NULL CHECK (kind IN ('downloads','customer_files','ticket_attachments','content_images','invoice_application_documents')),
 object_key text NOT NULL CHECK (object_key ~ '^[a-f0-9]{64}$'),
 size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 52428800),
 sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
 state text NOT NULL CHECK (state IN ('staging','ready','attached','uncertain','deleting','deleted')),
 gc_after timestamptz DEFAULT clock_timestamp()+interval '24 hours',
 unreferenced_at timestamptz,
 delete_lease_until timestamptz,
 delete_lease_token text NOT NULL DEFAULT '',
 delete_attempts integer NOT NULL DEFAULT 0 CHECK (delete_attempts>=0),
 last_error_code text NOT NULL DEFAULT '' CHECK (last_error_code IN ('','put_failed','journal_failed','profile_unavailable','location_mismatch','delete_failed')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(profile_id,location_sha256,object_key)
);
CREATE INDEX file_storage_objects_gc ON file_storage_objects(gc_after,id) WHERE state IN ('staging','ready','deleting');

CREATE FUNCTION preserve_file_storage_intent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.id,NEW.profile_id,NEW.location_sha256,NEW.kind,NEW.object_key,NEW.size_bytes,NEW.sha256)
    IS DISTINCT FROM ROW(OLD.id,OLD.profile_id,OLD.location_sha256,OLD.kind,OLD.object_key,OLD.size_bytes,OLD.sha256) THEN
  RAISE EXCEPTION 'file storage intent is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER file_storage_intent_immutable BEFORE UPDATE ON file_storage_objects FOR EACH ROW EXECUTE FUNCTION preserve_file_storage_intent();

ALTER TABLE downloads ADD COLUMN storage_object_id text REFERENCES file_storage_objects(id);
ALTER TABLE customer_files ADD COLUMN storage_object_id text REFERENCES file_storage_objects(id), ALTER COLUMN content DROP NOT NULL;
ALTER TABLE ticket_attachments ADD COLUMN storage_object_id text REFERENCES file_storage_objects(id), ALTER COLUMN content DROP NOT NULL;
ALTER TABLE content_images ADD COLUMN storage_object_id text REFERENCES file_storage_objects(id), ALTER COLUMN content DROP NOT NULL;
ALTER TABLE invoice_application_documents ADD COLUMN storage_object_id text REFERENCES file_storage_objects(id), ALTER COLUMN content DROP NOT NULL;

-- Replace only the old checks that required an inline payload; size, digest,
-- MIME, ownership and the existing source-type checks remain in force.
DO $$ DECLARE entry record; BEGIN
 FOR entry IN SELECT c.conname,c.conrelid::regclass AS target FROM pg_constraint c
 WHERE c.contype='c' AND c.conrelid IN ('downloads'::regclass,'customer_files'::regclass,'content_images'::regclass,'invoice_application_documents'::regclass)
 AND pg_get_constraintdef(c.oid) LIKE '%content%' LOOP
  EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I',entry.target,entry.conname);
 END LOOP;
END $$;
ALTER TABLE downloads ADD CONSTRAINT downloads_payload_check CHECK (
 (source_type='local' AND remote_url='' AND size_bytes>0 AND filename<>'' AND mime_type<>'' AND sha256 ~ '^[a-f0-9]{64}$'
  AND ((content IS NOT NULL AND storage_object_id IS NULL AND octet_length(content)=size_bytes) OR (content IS NULL AND storage_object_id IS NOT NULL)))
 OR (source_type='remote' AND remote_url<>'' AND content IS NULL AND storage_object_id IS NULL AND size_bytes=0 AND sha256='' AND filename='' AND mime_type=''));
ALTER TABLE customer_files ADD CONSTRAINT customer_files_payload_check CHECK (
 (content IS NOT NULL AND storage_object_id IS NULL AND octet_length(content)=size_bytes) OR (content IS NULL AND storage_object_id IS NOT NULL));
ALTER TABLE ticket_attachments ADD CONSTRAINT ticket_attachments_payload_check CHECK (
 (content IS NOT NULL AND storage_object_id IS NULL) OR (content IS NULL AND storage_object_id IS NOT NULL));
ALTER TABLE content_images ADD CONSTRAINT content_images_payload_check CHECK (
 (content IS NOT NULL AND storage_object_id IS NULL AND octet_length(content)=size_bytes) OR (content IS NULL AND storage_object_id IS NOT NULL));
ALTER TABLE invoice_application_documents ADD CONSTRAINT invoice_application_documents_payload_check CHECK (
 (content IS NOT NULL AND storage_object_id IS NULL AND octet_length(content)=size_bytes) OR (content IS NULL AND storage_object_id IS NOT NULL));

CREATE UNIQUE INDEX downloads_storage_object ON downloads(storage_object_id) WHERE storage_object_id IS NOT NULL;
CREATE UNIQUE INDEX customer_files_storage_object ON customer_files(storage_object_id) WHERE storage_object_id IS NOT NULL;
CREATE UNIQUE INDEX ticket_attachments_storage_object ON ticket_attachments(storage_object_id) WHERE storage_object_id IS NOT NULL;
CREATE UNIQUE INDEX content_images_storage_object ON content_images(storage_object_id) WHERE storage_object_id IS NOT NULL;
CREATE UNIQUE INDEX invoice_application_documents_storage_object ON invoice_application_documents(storage_object_id) WHERE storage_object_id IS NOT NULL;

CREATE FUNCTION file_storage_object_referenced(target text) RETURNS boolean LANGUAGE sql VOLATILE AS $$
 SELECT EXISTS(SELECT 1 FROM downloads WHERE storage_object_id=target)
 OR EXISTS(SELECT 1 FROM customer_files WHERE storage_object_id=target)
 OR EXISTS(SELECT 1 FROM ticket_attachments WHERE storage_object_id=target)
 OR EXISTS(SELECT 1 FROM content_images WHERE storage_object_id=target)
 OR EXISTS(SELECT 1 FROM invoice_application_documents WHERE storage_object_id=target)
$$;

CREATE FUNCTION attach_file_storage_object() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE item file_storage_objects%ROWTYPE;
BEGIN
 IF NEW.storage_object_id IS NULL THEN RETURN NEW; END IF;
 SELECT * INTO item FROM file_storage_objects WHERE id=NEW.storage_object_id FOR UPDATE;
 IF NOT FOUND OR item.kind<>TG_TABLE_NAME OR item.state NOT IN ('ready','attached') OR
    item.size_bytes<>NEW.size_bytes OR NEW.content IS NOT NULL THEN
  RAISE EXCEPTION 'file storage object cannot be attached';
 END IF;
 IF TG_TABLE_NAME<>'content_images' THEN
  IF item.sha256<>NEW.sha256 THEN RAISE EXCEPTION 'file storage digest mismatch'; END IF;
 END IF;
 UPDATE file_storage_objects SET state='attached',gc_after=NULL,unreferenced_at=NULL,
  delete_lease_until=NULL,delete_lease_token='',updated_at=clock_timestamp() WHERE id=item.id;
 RETURN NEW;
END $$;

CREATE FUNCTION release_file_storage_object() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.storage_object_id IS NULL THEN RETURN NULL; END IF;
 IF TG_OP='UPDATE' AND OLD.storage_object_id IS NOT DISTINCT FROM NEW.storage_object_id THEN RETURN NULL; END IF;
 PERFORM id FROM file_storage_objects WHERE id=OLD.storage_object_id FOR UPDATE;
 IF NOT file_storage_object_referenced(OLD.storage_object_id) THEN
  UPDATE file_storage_objects SET state='ready',gc_after=clock_timestamp()+interval '1 hour',
   unreferenced_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=OLD.storage_object_id;
 END IF;
 RETURN NULL;
END $$;

DO $$ DECLARE target text; BEGIN
 FOREACH target IN ARRAY ARRAY['downloads','customer_files','ticket_attachments','content_images','invoice_application_documents'] LOOP
  EXECUTE format('CREATE TRIGGER file_storage_attach BEFORE INSERT OR UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION attach_file_storage_object()',target);
  EXECUTE format('CREATE TRIGGER file_storage_release AFTER UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION release_file_storage_object()',target);
 END LOOP;
END $$;
