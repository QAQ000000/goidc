CREATE TABLE customer_custom_fields (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  field_key text NOT NULL UNIQUE CHECK (field_key ~ '^[a-z][a-z0-9_]{1,63}$'),
  label text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 120),
  field_type text NOT NULL CHECK (field_type IN ('text','textarea','number','date','select','boolean')),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
  validation_pattern text NOT NULL DEFAULT '' CHECK (char_length(validation_pattern) <= 500),
  choices text[] NOT NULL DEFAULT '{}',
  sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN -100000 AND 100000),
  admin_only boolean NOT NULL DEFAULT false,
  required boolean NOT NULL DEFAULT false,
  show_on_register boolean NOT NULL DEFAULT false,
  active boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (field_type <> 'select' OR cardinality(choices) > 0),
  CHECK (field_type = 'select' OR cardinality(choices) = 0)
);
CREATE INDEX customer_custom_fields_order ON customer_custom_fields(active, sort_order, id);
