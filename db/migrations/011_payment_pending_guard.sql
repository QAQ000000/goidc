CREATE UNIQUE INDEX payment_transactions_one_pending_invoice
  ON payment_transactions(provider, invoice_id)
  WHERE status = 'pending';
