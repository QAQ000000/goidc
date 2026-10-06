CREATE TABLE product_sections (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 255),
 slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z][a-z0-9-]{0,119}$'),
 description text NOT NULL DEFAULT '' CHECK (char_length(description)<=1000),
 active boolean NOT NULL DEFAULT true,
 sort_order int NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 1000000),
 version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE product_categories
 ADD COLUMN section_id bigint REFERENCES product_sections(id),
 ADD COLUMN description text NOT NULL DEFAULT '' CHECK (char_length(description)<=1000),
 ADD COLUMN headline text NOT NULL DEFAULT '' CHECK (char_length(headline)<=255),
 ADD COLUMN tagline text NOT NULL DEFAULT '' CHECK (char_length(tagline)<=255),
 ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 ADD COLUMN created_at timestamptz NOT NULL DEFAULT now(),
 ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX product_categories_section ON product_categories(section_id,sort_order,id);
ALTER TABLE products ADD COLUMN sort_order int NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 1000000);
CREATE OR REPLACE FUNCTION catalog_category_visible(category bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN category IS NULL THEN true ELSE coalesce((SELECT c.active AND coalesce(s.active,true)
 FROM product_categories c LEFT JOIN product_sections s ON s.id=c.section_id WHERE c.id=category),false) END
$$;
