CREATE TABLE customer_files (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 customer_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 255),
 remarks text NOT NULL DEFAULT '' CHECK(char_length(remarks)<=10000),
 filename text NOT NULL CHECK(char_length(filename) BETWEEN 1 AND 255),
 mime_type text NOT NULL,
 size_bytes bigint NOT NULL CHECK(size_bytes BETWEEN 1 AND 52428800),
 sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),
 content bytea NOT NULL CHECK(octet_length(content)=size_bytes),
 uploader_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 downloads bigint NOT NULL DEFAULT 0 CHECK(downloads>=0),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX customer_files_owner ON customer_files(customer_id,id DESC);
