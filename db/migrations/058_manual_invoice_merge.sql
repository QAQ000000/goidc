ALTER TABLE manual_invoice_operations DROP CONSTRAINT manual_invoice_operations_action_check;
ALTER TABLE manual_invoice_operations ADD CONSTRAINT manual_invoice_operations_action_check CHECK (action IN ('create','update','cancel','merge'));
