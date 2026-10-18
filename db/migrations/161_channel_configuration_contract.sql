ALTER TABLE channel_instances
 ADD COLUMN schema_version integer NOT NULL DEFAULT 1 CHECK (schema_version > 0),
 ADD COLUMN secret_fields jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(secret_fields) = 'object');

COMMENT ON COLUMN channel_instances.schema_version IS 'Persisted module configuration schema, distinct from the optimistic-lock version';
COMMENT ON COLUMN channel_instances.secret_fields IS 'Named AES-GCM ciphertexts; single-credential modules retain the legacy secret_ciphertext column';
