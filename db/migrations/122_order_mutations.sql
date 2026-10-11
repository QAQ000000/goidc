ALTER TABLE orders
 ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 ADD COLUMN deleted_at timestamptz,
 ADD COLUMN deleted_by bigint REFERENCES users(id),
 ADD COLUMN deletion_reason text NOT NULL DEFAULT '';
CREATE FUNCTION advance_order_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.version := OLD.version+1;
 NEW.updated_at := clock_timestamp();
 RETURN NEW;
END
$$;
CREATE TRIGGER order_version BEFORE UPDATE ON orders FOR EACH ROW EXECUTE FUNCTION advance_order_version();
CREATE INDEX orders_visible_listing ON orders(user_id,id DESC) WHERE deleted_at IS NULL;
CREATE TABLE order_operation_receipts (
 actor_id bigint NOT NULL REFERENCES users(id),
 action text NOT NULL,
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 128),
 order_id bigint NOT NULL REFERENCES orders(id),
 request_hash text NOT NULL,
 result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor_id,action,idempotency_key)
);
CREATE TABLE order_batch_requests (
 actor_id bigint NOT NULL REFERENCES users(id),
 action text NOT NULL,
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 100),
 request_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor_id,action,idempotency_key)
);
