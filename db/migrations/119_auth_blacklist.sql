-- Administrator login blocks from the legacy security page.  Entries are
-- explicit and auditable; blocked_until is nullable for a permanent block.
CREATE TABLE auth_blacklist (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  subject_type text NOT NULL CHECK (subject_type IN ('username','ip')),
  subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 254),
  operation_type text NOT NULL DEFAULT 'login' CHECK (operation_type IN ('login')),
  remote_addr text NOT NULL DEFAULT '',
  reason text NOT NULL DEFAULT '' CHECK (char_length(reason) <= 500),
  blocked_until timestamptz,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(subject_type, subject, operation_type)
);
CREATE INDEX auth_blacklist_active_idx ON auth_blacklist(subject_type, subject, blocked_until);
