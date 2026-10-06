ALTER TABLE invoice_application_operations DROP CONSTRAINT invoice_application_operations_action_check;
ALTER TABLE invoice_application_operations ADD CONSTRAINT invoice_application_operations_action_check CHECK (action IN ('create','review','adjust'));

CREATE TABLE invoice_application_adjustments (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  application_id bigint NOT NULL REFERENCES invoice_applications(id) ON DELETE CASCADE,
  action text NOT NULL CHECK (action IN ('void','red')),
  status text NOT NULL DEFAULT 'recorded' CHECK (status='recorded'),
  document_number text NOT NULL DEFAULT '',
  note text NOT NULL,
  actor_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(application_id, action),
  CHECK (char_length(trim(note)) BETWEEN 1 AND 500),
  CHECK (char_length(trim(document_number)) <= 120)
);
CREATE INDEX invoice_application_adjustments_app ON invoice_application_adjustments(application_id,id DESC);

CREATE TABLE invoice_application_documents (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  application_id bigint NOT NULL REFERENCES invoice_applications(id) ON DELETE CASCADE,
  adjustment_id bigint REFERENCES invoice_application_adjustments(id) ON DELETE SET NULL,
  filename text NOT NULL CHECK (char_length(filename) BETWEEN 1 AND 255),
  mime_type text NOT NULL CHECK (mime_type IN ('application/pdf','application/xml','text/xml','application/octet-stream')),
  size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 20971520),
  sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
  content bytea NOT NULL CHECK (octet_length(content)=size_bytes),
  uploader_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  downloads bigint NOT NULL DEFAULT 0 CHECK (downloads>=0),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(application_id, adjustment_id)
);
CREATE INDEX invoice_application_documents_app ON invoice_application_documents(application_id,id DESC);
CREATE UNIQUE INDEX invoice_application_documents_primary ON invoice_application_documents(application_id) WHERE adjustment_id IS NULL;
