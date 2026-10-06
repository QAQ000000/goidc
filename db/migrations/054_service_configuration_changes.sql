ALTER TABLE products ADD COLUMN config_upgrade_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE products ADD COLUMN config_downgrade_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE services ADD COLUMN option_ids jsonb CHECK (jsonb_typeof(option_ids)='array');
ALTER TABLE services ADD COLUMN option_quantities jsonb CHECK (jsonb_typeof(option_quantities)='object');
ALTER TABLE services ADD COLUMN config_snapshot jsonb CHECK (jsonb_typeof(config_snapshot)='array');
ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_action_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_action_check
  CHECK (action IN ('create','suspend','resume','terminate','sync','upgrade'));

CREATE TABLE service_configuration_changes (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  service_id bigint NOT NULL REFERENCES services(id),
  user_id bigint NOT NULL REFERENCES users(id),
  product_revision bigint NOT NULL,
  service_version bigint NOT NULL,
  billing_cycle text NOT NULL,
  due_at timestamptz NOT NULL,
  old_recurring_cents bigint NOT NULL CHECK (old_recurring_cents>=0),
  new_recurring_cents bigint NOT NULL CHECK (new_recurring_cents>=0),
  amount_cents bigint NOT NULL CHECK (amount_cents>=0),
  credit_cents bigint NOT NULL CHECK (credit_cents>=0),
  old_snapshot jsonb NOT NULL CHECK (jsonb_typeof(old_snapshot)='array'),
  new_snapshot jsonb NOT NULL CHECK (jsonb_typeof(new_snapshot)='array'),
  option_ids jsonb NOT NULL CHECK (jsonb_typeof(option_ids)='array'),
  option_quantities jsonb NOT NULL CHECK (jsonb_typeof(option_quantities)='object'),
  status text NOT NULL DEFAULT 'quoted' CHECK (status IN ('quoted','unpaid','queued','waiting_manual','completed','cancelled')),
  invoice_id bigint UNIQUE REFERENCES invoices(id),
  job_id bigint UNIQUE REFERENCES provision_jobs(id),
  idempotency_key text,
  expires_at timestamptz NOT NULL DEFAULT now()+interval '10 minutes',
  completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(user_id,idempotency_key),
  CHECK (amount_cents=0 OR credit_cents=0)
);
CREATE UNIQUE INDEX service_configuration_changes_one_pending ON service_configuration_changes(service_id)
 WHERE status IN ('unpaid','queued','waiting_manual');
CREATE INDEX service_configuration_changes_service ON service_configuration_changes(service_id,id DESC);
