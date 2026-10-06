ALTER TABLE download_categories ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE download_categories ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX download_categories_legacy_identity ON download_categories(legacy_source,legacy_id)
 WHERE legacy_source<>'' AND legacy_id IS NOT NULL;
ALTER TABLE downloads ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX downloads_legacy_identity ON downloads(legacy_source,legacy_id)
 WHERE legacy_source<>'' AND legacy_id IS NOT NULL;
ALTER TABLE customer_files ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE customer_files ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX customer_files_legacy_identity ON customer_files(legacy_source,legacy_id)
 WHERE legacy_source<>'' AND legacy_id IS NOT NULL;
ALTER TABLE download_products ADD COLUMN id bigserial;
CREATE UNIQUE INDEX download_products_id ON download_products(id);
ALTER TABLE download_products ADD COLUMN legacy_source text NOT NULL DEFAULT '';
ALTER TABLE download_products ADD COLUMN legacy_id bigint;
CREATE UNIQUE INDEX download_products_legacy_identity ON download_products(legacy_source,legacy_id)
 WHERE legacy_source<>'' AND legacy_id IS NOT NULL;

-- Empty entities must also be part of the immutable source contract.
CREATE TABLE legacy_snapshot_manifests (
 source text PRIMARY KEY, contract_version int NOT NULL CHECK(contract_version=1),
 entities text[] NOT NULL, run_id bigint NOT NULL REFERENCES legacy_import_runs(id)
);
