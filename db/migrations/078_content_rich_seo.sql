ALTER TABLE content_articles
 ADD COLUMN body_html text NOT NULL DEFAULT '' CHECK (octet_length(body_html) <= 120000),
 ADD COLUMN seo_title text NOT NULL DEFAULT '' CHECK (char_length(seo_title) <= 240),
 ADD COLUMN seo_description text NOT NULL DEFAULT '' CHECK (char_length(seo_description) <= 1000),
 ADD COLUMN seo_canonical text NOT NULL DEFAULT '' CHECK (char_length(seo_canonical) <= 500);

CREATE TABLE content_images (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 article_id bigint NOT NULL REFERENCES content_articles(id) ON DELETE CASCADE,
 uploader_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 filename text NOT NULL CHECK (char_length(filename) BETWEEN 1 AND 255),
 mime_type text NOT NULL CHECK (mime_type IN ('image/png','image/jpeg','image/gif','image/webp')),
 size_bytes bigint NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 5242880),
 content bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK (octet_length(content)=size_bytes)
);
CREATE INDEX content_images_article ON content_images(article_id,id);
