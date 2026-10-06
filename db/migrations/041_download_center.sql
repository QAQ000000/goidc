CREATE TABLE download_settings (
 id int PRIMARY KEY CHECK(id=1), enabled boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0), updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO download_settings(id) VALUES(1);
CREATE TABLE download_categories (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 255),
 description text NOT NULL DEFAULT '' CHECK(char_length(description)<=1000),
 sort_order int NOT NULL DEFAULT 0 CHECK(sort_order BETWEEN 0 AND 1000000),
 active boolean NOT NULL DEFAULT true, version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE downloads (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 category_id bigint REFERENCES download_categories(id) ON DELETE RESTRICT,
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 255),
 description text NOT NULL DEFAULT '' CHECK(char_length(description)<=10000),
 active boolean NOT NULL DEFAULT false,
 access_level text NOT NULL CHECK(access_level IN ('public','login','product')),
 source_type text NOT NULL CHECK(source_type IN ('local','remote')),
 remote_url text NOT NULL DEFAULT '',
 filename text NOT NULL DEFAULT '', mime_type text NOT NULL DEFAULT '',
 size_bytes bigint NOT NULL DEFAULT 0 CHECK(size_bytes BETWEEN 0 AND 52428800),
 sha256 text NOT NULL DEFAULT '', content bytea,
 downloads bigint NOT NULL DEFAULT 0 CHECK(downloads>=0),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0), author_id bigint NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK((source_type='local' AND remote_url='' AND content IS NOT NULL AND size_bytes>0
   AND octet_length(content)=size_bytes AND filename<>'' AND mime_type<>'' AND sha256 ~ '^[a-f0-9]{64}$')
  OR (source_type='remote' AND remote_url<>'' AND content IS NULL AND size_bytes=0 AND sha256='' AND filename='' AND mime_type=''))
);
CREATE TABLE download_products (
 download_id bigint NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,
 product_id bigint NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
 PRIMARY KEY(download_id,product_id)
);
CREATE INDEX downloads_catalog ON downloads(category_id,active,id DESC);
CREATE INDEX download_products_product ON download_products(product_id,download_id);
CREATE FUNCTION download_allowed(download_id bigint, customer_id bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT d.access_level='public' OR (d.access_level='login' AND customer_id>0)
  OR (d.access_level='product' AND EXISTS(SELECT 1 FROM services s JOIN download_products p ON p.product_id=s.product_id
    WHERE p.download_id=d.id AND s.user_id=customer_id AND s.status='active' AND NOT s.import_review_required))
 FROM downloads d WHERE d.id=download_id),false)
$$;
