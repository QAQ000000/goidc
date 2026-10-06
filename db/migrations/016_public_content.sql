CREATE TABLE content_categories (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  content_type text NOT NULL CHECK (content_type IN ('news','knowledge')),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  slug text NOT NULL CHECK (slug ~ '^[a-z][a-z0-9-]{0,119}$'),
  sort_order int NOT NULL DEFAULT 0,
  active boolean NOT NULL DEFAULT true,
  UNIQUE(content_type,slug)
);
CREATE TABLE content_articles (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  content_type text NOT NULL CHECK (content_type IN ('news','knowledge')),
  category_id bigint REFERENCES content_categories(id) ON DELETE SET NULL,
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 240),
  slug text NOT NULL,
  excerpt text NOT NULL DEFAULT '',
  body text NOT NULL,
  author_id bigint REFERENCES users(id) ON DELETE SET NULL,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','archived')),
  published_at timestamptz,
  views bigint NOT NULL DEFAULT 0 CHECK (views >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(content_type,slug)
);
CREATE INDEX content_articles_public ON content_articles(content_type,status,published_at DESC,id DESC);
CREATE INDEX content_articles_category ON content_articles(category_id,status,id DESC);
