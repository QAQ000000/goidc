ALTER TABLE products ADD COLUMN provision_mode text NOT NULL DEFAULT 'manual'
  CHECK (provision_mode IN ('manual','simulation'));
ALTER TABLE services ADD COLUMN provision_mode text NOT NULL DEFAULT 'manual'
  CHECK (provision_mode IN ('manual','simulation'));
ALTER TABLE services ADD COLUMN unit_number int NOT NULL DEFAULT 1 CHECK (unit_number > 0);
ALTER TABLE services ADD COLUMN inventory_released boolean NOT NULL DEFAULT false;
ALTER TABLE services DROP CONSTRAINT services_order_item_id_key;
ALTER TABLE services ADD CONSTRAINT services_item_unit UNIQUE(order_item_id,unit_number);

-- Retain the provenance of instances produced by the initial development simulator.
UPDATE services SET provision_mode='simulation' WHERE upstream_id LIKE 'local-%';
ALTER TABLE provision_jobs DROP CONSTRAINT provision_jobs_status_check;
ALTER TABLE provision_jobs ADD CONSTRAINT provision_jobs_status_check
  CHECK (status IN ('pending','running','waiting_manual','succeeded','failed','cancelled'));
WITH ranked AS (
  SELECT id,row_number() OVER (PARTITION BY service_id ORDER BY id) AS n
  FROM provision_jobs WHERE status IN ('pending','running')
)
UPDATE provision_jobs SET status='cancelled',last_error='superseded duplicate task',completed_at=now()
WHERE id IN (SELECT id FROM ranked WHERE n>1);
CREATE UNIQUE INDEX provision_jobs_one_inflight ON provision_jobs(service_id)
  WHERE status IN ('pending','running','waiting_manual');

-- Backfill historical order items without changing their quantities or creating resources.
INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,unit_number,status)
SELECT o.user_id,o.id,oi.id,oi.product_id,oi.product_name,
  'svc-'||replace(gen_random_uuid()::text,'-',''),oi.billing_cycle,n,
  CASE WHEN o.status IN ('cancelled','refunded') THEN 'cancelled' ELSE 'pending_payment' END
FROM order_items oi JOIN orders o ON o.id=oi.order_id
CROSS JOIN LATERAL generate_series(1,oi.quantity) n
WHERE NOT EXISTS (SELECT 1 FROM services s WHERE s.order_item_id=oi.id AND s.unit_number=n);
UPDATE services SET next_due_at=NULL WHERE status IN ('pending_payment','queued');
UPDATE services SET inventory_released=true WHERE order_id IN (SELECT id FROM orders WHERE status='refunded');
WITH queued AS (
  UPDATE services SET status='queued' WHERE status='pending_payment'
  AND order_id IN (SELECT id FROM orders WHERE status IN ('paid','active')) RETURNING id
)
INSERT INTO provision_jobs(service_id,action,idempotency_key)
SELECT id,'create','service-create-'||id FROM queued ON CONFLICT DO NOTHING;

ALTER TABLE balance_ledger DROP CONSTRAINT balance_ledger_delta_cents_check;
ALTER TABLE balance_ledger ADD CONSTRAINT balance_ledger_delta_cents_check
  CHECK (delta_cents <> 0 OR entry_type IN ('payment','refund'));
