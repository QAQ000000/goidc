ALTER TABLE customer_profiles ADD COLUMN custom_fields jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(custom_fields)='object');
ALTER TABLE customer_profiles ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version >= 1);
