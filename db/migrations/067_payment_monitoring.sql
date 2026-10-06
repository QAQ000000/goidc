ALTER TABLE payment_transactions
  ADD COLUMN next_reconcile_at timestamptz DEFAULT now()+interval '2 minutes',
  ADD COLUMN auto_reconcile_attempts integer NOT NULL DEFAULT 0
    CHECK (auto_reconcile_attempts BETWEEN 0 AND 32);
UPDATE payment_transactions SET next_reconcile_at=NULL WHERE status<>'pending' OR provider NOT LIKE 'epay_%';
CREATE INDEX payment_transactions_reconcile_due ON payment_transactions(next_reconcile_at,id)
  WHERE status='pending' AND provider LIKE 'epay_%' AND next_reconcile_at IS NOT NULL;

ALTER TABLE payment_reconciliation_checks ALTER COLUMN actor_id DROP NOT NULL;
ALTER TABLE payment_reconciliation_checks
  ADD COLUMN source text NOT NULL DEFAULT 'manual' CHECK(source IN ('manual','worker')),
  ADD CONSTRAINT payment_reconciliation_checks_actor_source CHECK
    ((source='worker' AND actor_id IS NULL) OR (source='manual' AND actor_id IS NOT NULL));
