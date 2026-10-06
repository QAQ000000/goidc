CREATE TABLE ticket_departments (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z][a-z0-9-]{0,119}$'),
  sort_order int NOT NULL DEFAULT 0,
  active boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE tickets ADD COLUMN department_id bigint REFERENCES ticket_departments(id) ON DELETE SET NULL;
ALTER TABLE tickets ADD COLUMN assignee_id bigint REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX tickets_department ON tickets(department_id,status,updated_at DESC);
CREATE INDEX tickets_assignee ON tickets(assignee_id,status,updated_at DESC);
