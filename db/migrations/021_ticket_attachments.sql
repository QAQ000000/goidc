CREATE TABLE ticket_attachments (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id bigint NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  uploader_id bigint NOT NULL REFERENCES users(id),
  filename text NOT NULL CHECK (char_length(filename) BETWEEN 1 AND 255),
  mime_type text NOT NULL CHECK (char_length(mime_type) BETWEEN 1 AND 160),
  size_bytes bigint NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 10485760),
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  content bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_attachments_ticket ON ticket_attachments(ticket_id,id);
