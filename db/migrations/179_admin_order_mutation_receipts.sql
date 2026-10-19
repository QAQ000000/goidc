-- A receipt is not the order itself. Preserve its historical request/result and
-- resource foreign keys while moving its key domain into the shared protocol.
-- Migrate executes this file atomically; stop old writers before upgrading.
LOCK TABLE admin_order_creation_receipts IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
 IF EXISTS (
  SELECT 1 FROM admin_order_creation_receipts r
  JOIN orders o ON o.id=r.order_id
  WHERE o.user_id IS DISTINCT FROM r.customer_id
     OR (r.result->'order_id') IS DISTINCT FROM to_jsonb(r.order_id)
 ) THEN
  RAISE EXCEPTION 'admin_order_receipt_requires_reconciliation'
   USING ERRCODE='23514', HINT='Verify the historical customer, order and saved result before retrying the upgrade.';
 END IF;
END $$;

ALTER TABLE mutation_operations
 ADD COLUMN customer_id bigint REFERENCES users(id) ON DELETE RESTRICT,
 ADD COLUMN order_id bigint REFERENCES orders(id) ON DELETE RESTRICT,
 ADD CONSTRAINT mutation_operations_order_creation_references CHECK (
  (scope='admin:order.create' AND customer_id IS NOT NULL AND customer_id>0
   AND order_id IS NOT NULL AND order_id>0
   AND ((result->'order_id')=to_jsonb(order_id)) IS TRUE)
  OR (scope<>'admin:order.create' AND customer_id IS NULL AND order_id IS NULL)
 );

CREATE INDEX mutation_operations_customer_reference ON mutation_operations(customer_id) WHERE customer_id IS NOT NULL;
CREATE INDEX mutation_operations_order_reference ON mutation_operations(order_id) WHERE order_id IS NOT NULL;

-- No upsert: a pre-existing receipt in this formerly unused scope is ambiguous.
-- A collision or invalid row must roll back the entire migration, not overwrite
-- either result or silently discard the old receipt.
INSERT INTO mutation_operations(actor_id,scope,idempotency_key,request_hash,result,created_at,customer_id,order_id)
 SELECT actor_id,'admin:order.create',idempotency_key,request_hash,result,created_at,customer_id,order_id
 FROM admin_order_creation_receipts;

DROP TABLE admin_order_creation_receipts;
