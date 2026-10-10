-- Customer group mutations use optimistic concurrency and the shared admin
-- setting operation journal for idempotent replays.
ALTER TABLE customer_groups
  ADD COLUMN IF NOT EXISTS version bigint NOT NULL DEFAULT 1 CHECK (version > 0);

CREATE INDEX IF NOT EXISTS customer_groups_version ON customer_groups(id, version);
