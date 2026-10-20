-- Contact CAS is additive: historical recipients and their notification settings
-- remain unchanged. Stop old writers before deploying version-aware clients;
-- legacy API callers omitting expected_version retain their previous semantics.
ALTER TABLE customer_contacts ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version > 0);
