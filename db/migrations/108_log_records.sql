-- Admin notification and request log records. Message payloads remain encrypted
-- in the outbox; the admin log exposes delivery metadata only.
CREATE TABLE admin_api_logs (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  actor_id bigint REFERENCES users(id) ON DELETE SET NULL,
  method text NOT NULL CHECK (method IN ('GET','POST','PUT','PATCH','DELETE','HEAD','OPTIONS')),
  path text NOT NULL CHECK (char_length(path) BETWEEN 1 AND 2048),
  status_code integer NOT NULL CHECK (status_code BETWEEN 100 AND 599),
  duration_ms integer NOT NULL CHECK (duration_ms >= 0),
  remote_ip inet,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_api_logs_created ON admin_api_logs(created_at DESC, id DESC);
CREATE INDEX admin_api_logs_actor ON admin_api_logs(actor_id, created_at DESC, id DESC);

CREATE TABLE admin_inbox_messages (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 240),
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 100000),
  status text NOT NULL DEFAULT 'sent' CHECK (status IN ('sent','read','failed')),
  created_at timestamptz NOT NULL DEFAULT now(),
  read_at timestamptz
);
CREATE INDEX admin_inbox_messages_created ON admin_inbox_messages(created_at DESC, id DESC);
CREATE INDEX admin_inbox_messages_user ON admin_inbox_messages(user_id, created_at DESC, id DESC);
