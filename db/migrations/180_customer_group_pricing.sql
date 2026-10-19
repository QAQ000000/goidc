-- New purchases freeze pricing outside order rows: refund evidence serializes
-- those complete rows, so adding columns there would invalidate old evidence.
CREATE TABLE promo_customer_groups (
 promo_id bigint NOT NULL REFERENCES promo_codes(id) ON DELETE CASCADE,
 customer_group_id bigint NOT NULL REFERENCES customer_groups(id) ON DELETE RESTRICT,
 PRIMARY KEY(promo_id,customer_group_id)
);
CREATE INDEX promo_customer_groups_customer ON promo_customer_groups(customer_group_id,promo_id);

CREATE TABLE order_item_pricing (
 order_item_id bigint PRIMARY KEY REFERENCES order_items(id) ON DELETE RESTRICT,
 gross_cents bigint NOT NULL CHECK(gross_cents>=0),
 group_discount_cents bigint NOT NULL CHECK(group_discount_cents>=0),
 promo_discount_cents bigint NOT NULL CHECK(promo_discount_cents>=0),
 net_cents bigint NOT NULL CHECK(net_cents>=0),
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 CHECK(gross_cents::numeric=group_discount_cents::numeric+promo_discount_cents::numeric+net_cents::numeric)
);

-- A pricing reader holds the shared transaction lock before catalog/product
-- locks. Statement triggers include first membership/rule inserts (phantoms).
CREATE FUNCTION lock_group_pricing_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(70108206);
 RETURN NULL;
END $$;
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['customer_groups','customer_group_members','customer_product_groups','customer_product_group_products','customer_product_group_discounts','promo_customer_groups'] LOOP
  EXECUTE format('CREATE TRIGGER group_pricing_write BEFORE INSERT OR UPDATE OR DELETE ON %I FOR EACH STATEMENT EXECUTE FUNCTION lock_group_pricing_write()',t);
 END LOOP;
END $$;

-- A separate birth marker avoids adding columns to refund-snapshotted rows.
-- It also rejects late pricing on a legacy row even if that row was updated
-- in the current transaction. No historical births are inferred or backfilled.
CREATE TABLE purchase_pricing_births (
 order_id bigint PRIMARY KEY REFERENCES orders(id) ON DELETE RESTRICT,
 creation_xid xid8 NOT NULL
);
CREATE FUNCTION guard_purchase_pricing_birth() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' OR pg_trigger_depth()<2 THEN RAISE EXCEPTION 'purchase pricing birth is immutable and trigger-owned'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_purchase_pricing_birth BEFORE INSERT OR UPDATE OR DELETE ON purchase_pricing_births FOR EACH ROW EXECUTE FUNCTION guard_purchase_pricing_birth();
CREATE FUNCTION record_purchase_pricing_birth() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO purchase_pricing_births(order_id,creation_xid) VALUES(NEW.id,pg_current_xact_id());
 RETURN NEW;
END $$;
CREATE TRIGGER record_purchase_pricing_birth AFTER INSERT ON orders FOR EACH ROW EXECUTE FUNCTION record_purchase_pricing_birth();

CREATE FUNCTION guard_order_item_pricing() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE gross bigint;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'purchase pricing is immutable'; END IF;
 SELECT total_cents INTO STRICT gross FROM order_items WHERE id=NEW.order_item_id;
 IF NOT EXISTS(SELECT 1 FROM order_items oi JOIN orders o ON o.id=oi.order_id WHERE oi.id=NEW.order_item_id AND EXISTS(SELECT 1 FROM purchase_pricing_births b WHERE b.order_id=o.id AND b.creation_xid=pg_current_xact_id())) THEN
  RAISE EXCEPTION 'pricing must be born with its purchase';
 END IF;
 IF gross<>NEW.gross_cents OR EXISTS(SELECT 1 FROM external_refund_order_stock_reservations WHERE order_item_id=NEW.order_item_id) THEN
  RAISE EXCEPTION 'pricing must match and precede the original reservation';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_order_item_pricing BEFORE INSERT OR UPDATE OR DELETE ON order_item_pricing FOR EACH ROW EXECUTE FUNCTION guard_order_item_pricing();

CREATE FUNCTION check_purchase_pricing_total() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE oid bigint; n bigint; cnt bigint; gross numeric; grp numeric; promo numeric; net numeric; expected numeric; discount numeric;
BEGIN
 SELECT order_id INTO STRICT oid FROM order_items WHERE id=NEW.order_item_id;
 SELECT count(*),count(p.order_item_id),sum(p.gross_cents),sum(p.group_discount_cents),sum(p.promo_discount_cents),sum(p.net_cents)
 INTO n,cnt,gross,grp,promo,net FROM order_items i LEFT JOIN order_item_pricing p ON p.order_item_id=i.id WHERE i.order_id=oid;
 SELECT total_cents,discount_cents INTO expected,discount FROM orders WHERE id=oid;
 IF n<>cnt OR net<>expected OR grp+promo<>discount THEN RAISE EXCEPTION 'purchase pricing totals do not match order'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER purchase_pricing_total AFTER INSERT ON order_item_pricing DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_purchase_pricing_total();

-- Cumulative allocation conserves every cent. Legacy orders retain their
-- original gross weights; new orders use immutable net weights.
CREATE FUNCTION purchase_invoice_item_net(target_invoice bigint,target_item bigint) RETURNS bigint LANGUAGE sql STABLE AS $$
 WITH weighted AS (
  SELECT oi.id,coalesce(p.net_cents,oi.total_cents)::numeric AS weight,inv.amount_cents::numeric AS amount,p.order_item_id IS NOT NULL AS has_pricing
  FROM invoices inv JOIN order_items oi ON oi.order_id=inv.order_id
  LEFT JOIN order_item_pricing p ON p.order_item_id=oi.id
  WHERE inv.id=target_invoice
 ), ranked AS (
  SELECT *,sum(weight) OVER(ORDER BY id) AS cumulative,sum(weight) OVER() AS total,bool_or(has_pricing) OVER() AS frozen FROM weighted
 )
 SELECT coalesce((SELECT CASE WHEN total>0 THEN div((CASE WHEN frozen THEN amount ELSE least(amount,total) END)*cumulative,total)-div((CASE WHEN frozen THEN amount ELSE least(amount,total) END)*(cumulative-weight),total) ELSE 0 END::bigint FROM ranked WHERE id=target_item),0)
$$;
