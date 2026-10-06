ALTER TABLE tickets ADD COLUMN merged_ticket_id bigint REFERENCES tickets(id) ON DELETE SET NULL;
ALTER TABLE tickets ADD CONSTRAINT tickets_not_self_merged CHECK (merged_ticket_id IS NULL OR merged_ticket_id <> id);
CREATE INDEX tickets_merged ON tickets(merged_ticket_id,id);

CREATE TABLE ticket_notes (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id bigint NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  author_id bigint NOT NULL REFERENCES users(id),
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 10000),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_notes_ticket ON ticket_notes(ticket_id,id);

CREATE TABLE ticket_transfer_logs (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ticket_id bigint NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  actor_id bigint NOT NULL REFERENCES users(id),
  previous_department_id bigint REFERENCES ticket_departments(id) ON DELETE SET NULL,
  department_id bigint REFERENCES ticket_departments(id) ON DELETE SET NULL,
  previous_assignee_id bigint REFERENCES users(id) ON DELETE SET NULL,
  assignee_id bigint REFERENCES users(id) ON DELETE SET NULL,
  reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 500),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_transfer_logs_ticket ON ticket_transfer_logs(ticket_id,id DESC);
