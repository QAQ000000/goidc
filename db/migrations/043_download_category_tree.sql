ALTER TABLE download_categories ADD COLUMN parent_id bigint REFERENCES download_categories(id) ON DELETE RESTRICT;
CREATE INDEX download_categories_parent ON download_categories(parent_id,sort_order,id);

-- Keep direct SQL writers subject to the same catalog lock and cycle rule.
CREATE FUNCTION check_download_category_parent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(70108198);
 IF NEW.parent_id IS NOT NULL AND (NEW.parent_id=NEW.id OR EXISTS(
  WITH RECURSIVE ancestors(id,parent_id) AS (
   SELECT id,parent_id FROM download_categories WHERE id=NEW.parent_id
   UNION SELECT c.id,c.parent_id FROM download_categories c JOIN ancestors a ON c.id=a.parent_id
  ) SELECT 1 FROM ancestors WHERE id=NEW.id
 )) THEN
  RAISE EXCEPTION 'download category cycle' USING ERRCODE='23514',CONSTRAINT='download_category_cycle';
 END IF;
 RETURN NEW;
END
$$;
CREATE TRIGGER download_category_parent BEFORE INSERT OR UPDATE OF parent_id ON download_categories
 FOR EACH ROW EXECUTE FUNCTION check_download_category_parent();

CREATE VIEW download_category_tree AS
 WITH RECURSIVE tree AS (
  SELECT c.*,ARRAY[c.name] AS path,ARRAY[c.id] AS ancestor_ids,0 AS depth,c.active AS effective_active,
   ARRAY[c.sort_order::bigint,c.id] AS sort_path
  FROM download_categories c WHERE c.parent_id IS NULL
  UNION ALL
  SELECT c.*,t.path||c.name,t.ancestor_ids||c.id,t.depth+1,t.effective_active AND c.active,
   t.sort_path||ARRAY[c.sort_order::bigint,c.id]
  FROM download_categories c JOIN tree t ON c.parent_id=t.id
 ) SELECT * FROM tree;

CREATE FUNCTION download_category_visible(category_id bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 WITH RECURSIVE ancestors AS (
  SELECT id,parent_id,active FROM download_categories WHERE id=category_id
  UNION SELECT c.id,c.parent_id,c.active FROM download_categories c JOIN ancestors a ON c.id=a.parent_id
 ) SELECT category_id IS NULL OR (EXISTS(SELECT 1 FROM ancestors WHERE parent_id IS NULL)
  AND NOT EXISTS(SELECT 1 FROM ancestors WHERE NOT active))
$$;
CREATE FUNCTION download_category_contains(root_id bigint, child_id bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 WITH RECURSIVE ancestors AS (
  SELECT id,parent_id FROM download_categories WHERE id=child_id
  UNION SELECT c.id,c.parent_id FROM download_categories c JOIN ancestors a ON c.id=a.parent_id
 ) SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=root_id)
$$;
