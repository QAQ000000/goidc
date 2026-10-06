ALTER TABLE product_option_groups
  ADD COLUMN parent_group_id bigint REFERENCES product_option_groups(id) DEFERRABLE INITIALLY IMMEDIATE,
  ADD COLUMN hierarchy_native boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT product_option_group_parent_valid CHECK (
    parent_group_id IS NULL OR (parent_group_id <> id AND option_type = 20 AND hierarchy_native)
  );
ALTER TABLE product_options
  ADD COLUMN parent_option_id bigint REFERENCES product_options(id) DEFERRABLE INITIALLY IMMEDIATE,
  ADD COLUMN hierarchy_native boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT product_option_parent_valid CHECK (
    parent_option_id IS NULL OR (parent_option_id <> id AND hierarchy_native)
  );

-- Existing records keep the source-backed resolver; new configurations use IDs.
ALTER TABLE product_option_groups ALTER COLUMN hierarchy_native SET DEFAULT true;
ALTER TABLE product_options ALTER COLUMN hierarchy_native SET DEFAULT true;
CREATE INDEX product_option_groups_parent ON product_option_groups(parent_group_id) WHERE parent_group_id IS NOT NULL;
CREATE INDEX product_options_parent ON product_options(parent_option_id) WHERE parent_option_id IS NOT NULL;
