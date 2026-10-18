-- Preserve every existing snapshot byte and original merchant identity. New
-- adapters share the same conservative, namespaced external-money identity.
ALTER TABLE external_refunds
  DROP CONSTRAINT external_refunds_merchant_scope_check,
  ADD CONSTRAINT external_refunds_merchant_scope_check
    CHECK (merchant_scope='mock:test-only' OR merchant_scope ~ '^[a-z][a-z0-9_-]{1,63}:[0-9a-f]{64}$');
ALTER TABLE payment_close_anomaly_merchant_bindings
  DROP CONSTRAINT payment_close_anomaly_merchant_bindings_merchant_scope_check,
  ADD CONSTRAINT payment_close_anomaly_merchant_bindings_merchant_scope_check
    CHECK (merchant_scope='mock:test-only' OR merchant_scope ~ '^[a-z][a-z0-9_-]{1,63}:[0-9a-f]{64}$');
ALTER TABLE payment_close_anomaly_refunds
  DROP CONSTRAINT payment_close_anomaly_refunds_merchant_scope_check,
  ADD CONSTRAINT payment_close_anomaly_refunds_merchant_scope_check
    CHECK (merchant_scope='mock:test-only' OR merchant_scope ~ '^[a-z][a-z0-9_-]{1,63}:[0-9a-f]{64}$');

DROP INDEX payment_transactions_epay_external_unique;
CREATE UNIQUE INDEX payment_transactions_external_unique
  ON payment_transactions(external_transaction_id)
  WHERE provider <> 'mock' AND external_transaction_id <> '';

DROP INDEX payment_transactions_reconcile_due;
CREATE INDEX payment_transactions_reconcile_due
  ON payment_transactions(next_reconcile_at,id)
  WHERE status IN ('pending','expired') AND provider <> 'mock'
    AND next_reconcile_at IS NOT NULL;
