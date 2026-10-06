ALTER TABLE invoices ADD COLUMN idempotency_key text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX invoices_user_idempotency ON invoices(user_id,idempotency_key)
  WHERE idempotency_key <> '';
ALTER TABLE invoice_items ADD COLUMN service_id bigint REFERENCES services(id);
