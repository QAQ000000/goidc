-- Formal purchase commission confirmations; these are not payouts.
CREATE TABLE sales_settlements (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 month text NOT NULL CHECK(month ~ '^\d{4}-\d{2}$'),
 actor_id bigint NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE sales_settlement_items (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 settlement_id bigint NOT NULL REFERENCES sales_settlements(id),
 order_id bigint NOT NULL UNIQUE REFERENCES orders(id),
 invoice_id bigint NOT NULL UNIQUE REFERENCES invoices(id),
 customer_id bigint NOT NULL REFERENCES users(id),
 sales_owner_id bigint NOT NULL REFERENCES users(id),
 snapshot jsonb NOT NULL,
 commission_cents bigint NOT NULL CHECK(commission_cents>=0),
 status text NOT NULL DEFAULT 'confirmed' CHECK(status IN ('confirmed','reversed')),
 reversed_at timestamptz
);
CREATE FUNCTION reverse_sales_invoice() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status='paid' AND NEW.status<>'paid' THEN
  WITH reversed AS (UPDATE sales_settlement_items SET status='reversed',reversed_at=now() WHERE invoice_id=NEW.id AND status='confirmed' RETURNING id,settlement_id,commission_cents)
  INSERT INTO audit_events(action,entity_type,entity_id,payload) SELECT 'sales.settlement_reversed','sales_settlement',settlement_id::text,jsonb_build_object('invoice_id',NEW.id,'item_id',id,'commission_cents',commission_cents,'invoice_status',NEW.status) FROM reversed;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER sales_invoice_reversal AFTER UPDATE OF status ON invoices FOR EACH ROW EXECUTE FUNCTION reverse_sales_invoice();

-- A cancelled/refunded purchase can be invalidated independently by imported
-- or administrative order transitions. Never retain commission for it.
CREATE FUNCTION reverse_sales_order() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status IN ('cancelled','refunded') AND OLD.status IS DISTINCT FROM NEW.status THEN
  WITH reversed AS (UPDATE sales_settlement_items SET status='reversed',reversed_at=now() WHERE order_id=NEW.id AND status='confirmed' RETURNING id,settlement_id,commission_cents)
  INSERT INTO audit_events(action,entity_type,entity_id,payload) SELECT 'sales.settlement_reversed','sales_settlement',settlement_id::text,jsonb_build_object('order_id',NEW.id,'item_id',id,'commission_cents',commission_cents,'order_status',NEW.status) FROM reversed;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER sales_order_reversal AFTER UPDATE OF status ON orders FOR EACH ROW EXECUTE FUNCTION reverse_sales_order();
