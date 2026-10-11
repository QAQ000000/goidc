CREATE TABLE site_theme_settings (
 id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1),
 template text NOT NULL DEFAULT 'default' CHECK (template IN ('default','classic','minimal')),
 primary_color text NOT NULL DEFAULT '#165DFF' CHECK (primary_color ~ '^#[0-9A-Fa-f]{6}$'),
 logo_variant text NOT NULL DEFAULT 'full' CHECK (logo_variant IN ('full','compact','text')),
 footer_text text NOT NULL DEFAULT '' CHECK (char_length(footer_text)<=500),
 version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO site_theme_settings(id) VALUES(1) ON CONFLICT DO NOTHING;

CREATE TABLE site_custom_fields (
 id bigserial PRIMARY KEY,
 field_key text NOT NULL UNIQUE CHECK (field_key ~ '^[a-z][a-z0-9_]{0,63}$'),
 label text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 120),
 field_type text NOT NULL CHECK (field_type IN ('text','textarea','url','email','number','boolean')),
 description text NOT NULL DEFAULT '' CHECK (char_length(description)<=500),
 required boolean NOT NULL DEFAULT false,
 active boolean NOT NULL DEFAULT true,
 sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN -100000 AND 100000),
 version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX site_custom_fields_active_order ON site_custom_fields(active,sort_order,id);

CREATE TABLE site_navigation_items (
 id bigserial PRIMARY KEY,
 parent_id bigint REFERENCES site_navigation_items(id) ON DELETE CASCADE,
 label text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 120),
 href text NOT NULL CHECK (char_length(href) BETWEEN 1 AND 2048),
 icon text NOT NULL DEFAULT '',
 sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN -100000 AND 100000),
 open_new_tab boolean NOT NULL DEFAULT false,
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CONSTRAINT site_navigation_no_self CHECK (parent_id IS NULL OR parent_id<>id)
);
CREATE INDEX site_navigation_order ON site_navigation_items(parent_id,sort_order,id);
CREATE FUNCTION check_site_navigation_parent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.parent_id IS NOT NULL AND (NEW.parent_id=NEW.id OR EXISTS(
   WITH RECURSIVE ancestors(id,parent_id) AS (
     SELECT id,parent_id FROM site_navigation_items WHERE id=NEW.parent_id
     UNION SELECT n.id,n.parent_id FROM site_navigation_items n JOIN ancestors a ON n.id=a.parent_id
   ) SELECT 1 FROM ancestors WHERE id=NEW.id
 )) THEN RAISE EXCEPTION 'site navigation cycle' USING ERRCODE='23514',CONSTRAINT='site_navigation_cycle'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER site_navigation_parent BEFORE INSERT OR UPDATE OF parent_id ON site_navigation_items
 FOR EACH ROW EXECUTE FUNCTION check_site_navigation_parent();

CREATE TABLE friendly_links (
 id bigserial PRIMARY KEY,
 name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
 url text NOT NULL CHECK (char_length(url) BETWEEN 1 AND 2048),
 logo_url text NOT NULL DEFAULT '',
 description text NOT NULL DEFAULT '' CHECK (char_length(description)<=500),
 sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN -100000 AND 100000),
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX friendly_links_order ON friendly_links(active,sort_order,id);
