ALTER TABLE payment_transactions ADD COLUMN external_transaction_id text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX payment_transactions_epay_external_unique
  ON payment_transactions(external_transaction_id)
  WHERE provider LIKE 'epay_%' AND external_transaction_id <> '';
