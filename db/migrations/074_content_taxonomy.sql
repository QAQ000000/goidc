ALTER TABLE content_articles ADD COLUMN sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 1000000);

CREATE TABLE content_article_categories (
  article_id bigint NOT NULL REFERENCES content_articles(id) ON DELETE CASCADE,
  category_id bigint NOT NULL REFERENCES content_categories(id) ON DELETE RESTRICT,
  PRIMARY KEY (article_id, category_id)
);
CREATE INDEX content_article_categories_category ON content_article_categories(category_id, article_id);
INSERT INTO content_article_categories(article_id, category_id)
 SELECT id, category_id FROM content_articles WHERE category_id IS NOT NULL;

CREATE TABLE content_tags (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  content_type text NOT NULL CHECK (content_type IN ('news','knowledge')),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  slug text NOT NULL CHECK (slug ~ '^[a-z][a-z0-9-]{0,119}$'),
  sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 1000000),
  active boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(content_type, slug)
);
CREATE TABLE content_article_tags (
  article_id bigint NOT NULL REFERENCES content_articles(id) ON DELETE CASCADE,
  tag_id bigint NOT NULL REFERENCES content_tags(id) ON DELETE RESTRICT,
  PRIMARY KEY (article_id, tag_id)
);
CREATE INDEX content_article_tags_tag ON content_article_tags(tag_id, article_id);
