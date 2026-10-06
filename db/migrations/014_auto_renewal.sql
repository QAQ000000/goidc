ALTER TABLE services ADD COLUMN auto_renew boolean NOT NULL DEFAULT false;
ALTER TABLE services ADD COLUMN auto_renew_error text NOT NULL DEFAULT '';
CREATE INDEX services_auto_renew_due ON services(auto_renew,next_due_at)
  WHERE auto_renew AND status IN ('active','suspended') AND next_due_at IS NOT NULL;
