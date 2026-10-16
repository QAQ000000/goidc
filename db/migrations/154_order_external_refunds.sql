-- Snapshot timestamps have a stable representation across dump/restore and
-- session timezone changes. This setting is local to the helper, never global.
CREATE FUNCTION external_refund_order_snapshot(value anyelement) RETURNS jsonb
 LANGUAGE sql STABLE SET timezone='UTC' SET datestyle='ISO,YMD' AS $$ SELECT to_jsonb(value) $$;

-- Only objects born under these guards can prove they have never delivered.
-- History intentionally has no parent FK: a service writer must never acquire
-- an order lock after its service lock, and deletion must not erase evidence.
CREATE TABLE external_refund_order_births(order_id bigint PRIMARY KEY, user_id bigint NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp());
CREATE TABLE external_refund_service_births(service_id bigint PRIMARY KEY, order_id bigint NOT NULL, identity jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp());
CREATE TABLE external_refund_delivery_history(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, order_id bigint NOT NULL, service_id bigint, event text NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp());
CREATE INDEX external_refund_delivery_history_order ON external_refund_delivery_history(order_id);
CREATE FUNCTION protect_external_refund_origin() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' OR pg_trigger_depth()<2 THEN RAISE EXCEPTION 'external refund origin is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER external_refund_order_birth_immutable BEFORE INSERT OR UPDATE OR DELETE ON external_refund_order_births FOR EACH ROW EXECUTE FUNCTION protect_external_refund_origin();
CREATE TRIGGER external_refund_service_birth_immutable BEFORE INSERT OR UPDATE OR DELETE ON external_refund_service_births FOR EACH ROW EXECUTE FUNCTION protect_external_refund_origin();
CREATE TRIGGER external_refund_delivery_history_immutable BEFORE INSERT OR UPDATE OR DELETE ON external_refund_delivery_history FOR EACH ROW EXECUTE FUNCTION protect_external_refund_origin();

CREATE TABLE external_refund_order_stock_reservations(
 order_item_id bigint PRIMARY KEY REFERENCES order_items(id), order_id bigint NOT NULL REFERENCES orders(id),
 product_id bigint NOT NULL REFERENCES products(id), units int NOT NULL CHECK(units>0),
 item_snapshot jsonb NOT NULL, service_ids jsonb NOT NULL, service_snapshots jsonb NOT NULL DEFAULT '[]'::jsonb,
 stock_before int, stock_after int, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((stock_before IS NULL AND stock_after IS NULL) OR (stock_before IS NOT NULL AND stock_after IS NOT NULL AND stock_after::bigint=stock_before::bigint-units))
);
CREATE TABLE external_refund_order_holds(
 refund_id bigint PRIMARY KEY REFERENCES external_refunds(id), invoice_id bigint NOT NULL REFERENCES invoices(id),
 payment_id bigint NOT NULL REFERENCES payment_transactions(id), order_id bigint NOT NULL REFERENCES orders(id), user_id bigint NOT NULL REFERENCES users(id),
 amount_cents bigint NOT NULL CHECK(amount_cents>0), invoice_version bigint NOT NULL, order_version bigint NOT NULL,
 invoice_snapshot jsonb NOT NULL, order_snapshot jsonb NOT NULL, items_snapshot jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','applying','released','applied')),
 stock_units_released bigint NOT NULL DEFAULT 0 CHECK(stock_units_released>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), applied_at timestamptz, released_at timestamptz
);
CREATE UNIQUE INDEX external_refund_order_one_live ON external_refund_order_holds(order_id) WHERE status<>'released';
CREATE TABLE external_refund_order_services(
 refund_id bigint NOT NULL REFERENCES external_refund_order_holds(refund_id), service_id bigint NOT NULL REFERENCES services(id),
 order_item_id bigint NOT NULL, product_id bigint NOT NULL, unit_number int NOT NULL, version bigint NOT NULL,
 snapshot jsonb NOT NULL, stock_at_request int, PRIMARY KEY(refund_id,service_id)
);
CREATE TABLE external_refund_order_stock_releases(
 refund_id bigint NOT NULL REFERENCES external_refund_order_holds(refund_id), product_id bigint NOT NULL REFERENCES products(id),
 units bigint NOT NULL CHECK(units>0), stock_before int, stock_after int,
 PRIMARY KEY(refund_id,product_id),
 CHECK((stock_before IS NULL AND stock_after IS NULL) OR (stock_before IS NOT NULL AND stock_after IS NOT NULL AND stock_after::bigint=stock_before::bigint+units))
);
CREATE FUNCTION immutable_external_refund_order_detail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 RAISE EXCEPTION 'external refund order detail is immutable' USING ERRCODE='23514';
END $$;
CREATE TRIGGER external_refund_order_services_immutable BEFORE UPDATE OR DELETE ON external_refund_order_services FOR EACH ROW EXECUTE FUNCTION immutable_external_refund_order_detail();
CREATE TRIGGER external_refund_order_stock_immutable BEFORE UPDATE OR DELETE ON external_refund_order_stock_releases FOR EACH ROW EXECUTE FUNCTION immutable_external_refund_order_detail();

CREATE FUNCTION external_refund_service_identity(s services) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT jsonb_build_array(s.user_id,s.order_id,s.order_item_id,s.product_id,s.unit_number,s.provision_mode,s.delivery_snapshot,s.legacy_source,s.legacy_id);
$$;
CREATE FUNCTION external_refund_order_blocked(target_order bigint) RETURNS boolean LANGUAGE sql VOLATILE AS $$
 SELECT EXISTS(SELECT 1 FROM external_refund_order_holds WHERE order_id=target_order AND status IN ('pending','applying','applied'));
$$;
CREATE FUNCTION guard_external_refund_order() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE h text;
BEGIN
 IF TG_OP='INSERT' THEN
  INSERT INTO external_refund_order_births(order_id,user_id) VALUES(NEW.id,NEW.user_id);
  IF NEW.status<>'pending' OR NEW.review_status<>'pending' OR NEW.legacy_source<>'' OR NEW.legacy_id IS NOT NULL THEN
   INSERT INTO external_refund_delivery_history(order_id,event) VALUES(NEW.id,'order_birth_not_pending');
  END IF;
  RETURN NEW;
 END IF;
 SELECT status INTO h FROM external_refund_order_holds WHERE order_id=OLD.id AND status<>'released';
 IF h IS NOT NULL THEN
  IF TG_OP='DELETE' OR h<>'applying' OR NEW.status<>'refunded' OR
   (external_refund_order_snapshot(NEW)-ARRAY['status','version','updated_at'])<>(external_refund_order_snapshot(OLD)-ARRAY['status','version','updated_at'])
  THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END IF;
 ELSE
  IF TG_OP='DELETE' OR NEW.user_id<>OLD.user_id OR NEW.review_status<>OLD.review_status OR NEW.status NOT IN ('pending','paid') OR NEW.deleted_at IS NOT NULL OR NEW.legacy_source IS DISTINCT FROM OLD.legacy_source OR NEW.legacy_id IS DISTINCT FROM OLD.legacy_id THEN
   INSERT INTO external_refund_delivery_history(order_id,event) VALUES(OLD.id,'order_changed');
  END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER zz_external_refund_order_birth AFTER INSERT ON orders FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order();
CREATE TRIGGER zz_external_refund_order_guard BEFORE UPDATE OR DELETE ON orders FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order();

CREATE FUNCTION guard_external_refund_service() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE h text;
BEGIN
 IF TG_OP='INSERT' THEN
  PERFORM id FROM orders WHERE id=NEW.order_id FOR KEY SHARE;
  IF external_refund_order_blocked(NEW.order_id) THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END IF;
  INSERT INTO external_refund_service_births(service_id,order_id,identity) VALUES(NEW.id,NEW.order_id,external_refund_service_identity(NEW));
 ELSE
  IF TG_OP='UPDATE' AND NEW.order_id<>OLD.order_id THEN
   BEGIN PERFORM id FROM orders WHERE id=NEW.order_id FOR KEY SHARE NOWAIT;
   EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END;
   IF external_refund_order_blocked(NEW.order_id) THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END IF;
  END IF;
  SELECT status INTO h FROM external_refund_order_holds WHERE order_id=OLD.order_id AND status<>'released';
  IF h IS NOT NULL THEN
   IF TG_OP='DELETE' OR h<>'applying' OR NEW.status<>'cancelled' OR NOT NEW.inventory_released OR
    (external_refund_order_snapshot(NEW)-ARRAY['status','version','inventory_released','updated_at'])<>(external_refund_order_snapshot(OLD)-ARRAY['status','version','inventory_released','updated_at'])
   THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END IF;
   RETURN NEW;
  END IF;
 END IF;
 IF TG_OP='DELETE' THEN
  INSERT INTO external_refund_delivery_history(order_id,service_id,event) VALUES(OLD.order_id,OLD.id,'service_deleted'); RETURN OLD;
 END IF;
 IF NEW.status<>'pending_payment' OR NEW.provision_mode<>'manual' OR NEW.upstream_id<>'' OR NEW.username<>'' OR NEW.next_due_at IS NOT NULL OR NEW.credentials_encrypted IS NOT NULL OR NEW.resource_info<>'{}'::jsonb OR NEW.installed_os<>'{}'::jsonb OR NEW.last_error<>'' OR NEW.inventory_released OR NEW.import_review_required OR NEW.legacy_source<>'' OR NEW.legacy_id IS NOT NULL OR
  (TG_OP='UPDATE' AND (external_refund_service_identity(NEW)<>external_refund_service_identity(OLD) OR (EXISTS(SELECT 1 FROM external_refund_order_stock_reservations WHERE order_item_id=OLD.order_item_id) AND external_refund_order_snapshot(NEW)<>external_refund_order_snapshot(OLD)))) THEN
  INSERT INTO external_refund_delivery_history(order_id,service_id,event) VALUES(NEW.order_id,NEW.id,'service_changed');
  IF TG_OP='UPDATE' AND NEW.order_id<>OLD.order_id THEN INSERT INTO external_refund_delivery_history(order_id,service_id,event) VALUES(OLD.order_id,OLD.id,'service_moved'); END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER zz_external_refund_service_guard BEFORE INSERT OR UPDATE OR DELETE ON services FOR EACH ROW EXECUTE FUNCTION guard_external_refund_service();

-- A job insertion takes only its service parent first, matching all processors.
-- A refund already owns that row; the fresh following query observes its hold.
CREATE FUNCTION guard_external_refund_job() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE sid bigint; oid bigint;
BEGIN
 sid:=CASE WHEN TG_OP='DELETE' THEN OLD.service_id ELSE NEW.service_id END;
 SELECT order_id INTO oid FROM services WHERE id=sid FOR UPDATE;
 IF external_refund_order_blocked(oid) OR (TG_OP='UPDATE' AND OLD.service_id<>NEW.service_id AND EXISTS(SELECT 1 FROM services s WHERE s.id=OLD.service_id AND external_refund_order_blocked(s.order_id))) THEN
  RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514';
 END IF;
 INSERT INTO external_refund_delivery_history(order_id,service_id,event) VALUES(oid,sid,'job_history');
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
-- INSERT is the race we must serialize. UPDATE/DELETE already imply durable
-- job history (including cancelled/failed); never take service after job locks.
CREATE TRIGGER external_refund_job_insert BEFORE INSERT ON provision_jobs FOR EACH ROW EXECUTE FUNCTION guard_external_refund_job();
-- Workers lock job before service when claiming; identity protection must not
-- acquire parents. Every job keeps the service/action whose birth was logged.
CREATE FUNCTION protect_external_refund_job_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.service_id IS DISTINCT FROM OLD.service_id OR NEW.action IS DISTINCT FROM OLD.action THEN
  RAISE EXCEPTION 'provision job service and action are immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER external_refund_job_identity BEFORE UPDATE ON provision_jobs FOR EACH ROW EXECUTE FUNCTION protect_external_refund_job_identity();


CREATE FUNCTION guard_external_refund_order_item() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE oid bigint;
BEGIN
 oid:=CASE WHEN TG_OP='DELETE' THEN OLD.order_id ELSE NEW.order_id END;
 IF TG_OP='INSERT' THEN PERFORM id FROM orders WHERE id=oid FOR KEY SHARE;
 ELSIF TG_OP='UPDATE' AND NEW.order_id<>OLD.order_id THEN
  BEGIN PERFORM id FROM orders WHERE id=oid FOR KEY SHARE NOWAIT; EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END;
 END IF;
 IF external_refund_order_blocked(oid) OR (TG_OP='UPDATE' AND external_refund_order_blocked(OLD.order_id)) THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END IF;
 IF TG_OP<>'INSERT' AND (EXISTS(SELECT 1 FROM external_refund_order_stock_reservations WHERE order_item_id=OLD.id) OR EXISTS(SELECT 1 FROM invoices WHERE order_id=OLD.order_id) OR EXISTS(SELECT 1 FROM orders WHERE id=OLD.order_id AND status<>'pending')) THEN
  INSERT INTO external_refund_delivery_history(order_id,event) VALUES(OLD.order_id,'order_item_changed');
  IF TG_OP='UPDATE' AND NEW.order_id<>OLD.order_id THEN INSERT INTO external_refund_delivery_history(order_id,event) VALUES(NEW.order_id,'order_item_moved'); END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE TRIGGER external_refund_order_item_guard BEFORE INSERT OR UPDATE OR DELETE ON order_items FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_item();

-- Pure qualification: shared by Store and deferred DB consistency checks.
CREATE FUNCTION external_refund_order_reason(target_invoice bigint) RETURNS text LANGUAGE plpgsql VOLATILE AS $$
DECLARE i invoices; o orders;
BEGIN
 SELECT * INTO i FROM invoices WHERE id=target_invoice;
 IF NOT FOUND OR i.order_id IS NULL THEN RETURN 'external_refund_order_not_eligible'; END IF;
 SELECT * INTO o FROM orders WHERE id=i.order_id;
 IF i.invoice_kind<>'automatic' OR i.status<>'paid' OR i.import_review_required OR i.legacy_source<>'' OR i.legacy_id IS NOT NULL OR o.legacy_source<>'' OR o.legacy_id IS NOT NULL OR o.status<>'paid' OR o.review_status<>'pending' OR o.deleted_at IS NOT NULL OR o.user_id<>i.user_id OR o.total_cents<>i.amount_cents OR
  NOT EXISTS(SELECT 1 FROM external_refund_order_births b WHERE b.order_id=o.id AND b.user_id=o.user_id) OR
  (SELECT count(*) FROM invoices WHERE order_id=o.id)<>1 OR
  (SELECT count(*) FROM payment_transactions WHERE invoice_id=i.id AND status='succeeded')<>1 OR
  NOT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=i.id AND user_id=i.user_id AND amount_cents=i.amount_cents AND status='succeeded') OR
  EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=i.id AND status IN ('pending','expired')) OR
  EXISTS(SELECT 1 FROM payment_close_operations c WHERE c.invoice_id=i.id AND payment_close_operation_unresolved(c.id)) OR
  EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) OR
  EXISTS(SELECT 1 FROM balance_ledger WHERE reference_type='invoice' AND reference_id=i.id AND entry_type IN ('payment','refund')) OR
  EXISTS(SELECT 1 FROM credit_invoice_payments WHERE invoice_id=i.id) OR EXISTS(SELECT 1 FROM traffic_orders WHERE invoice_id=i.id) OR
  EXISTS(SELECT 1 FROM invoice_items WHERE invoice_id=i.id) OR
  EXISTS(SELECT 1 FROM invoice_application_items x JOIN invoice_applications a ON a.id=x.application_id WHERE x.source_invoice_id=i.id AND a.status IN ('pending','issued'))
 THEN RETURN 'external_refund_order_not_eligible'; END IF;
 IF EXISTS(SELECT 1 FROM affiliate_commission_entries WHERE invoice_id=i.id OR order_id=o.id) OR EXISTS(SELECT 1 FROM affiliate_turnover_entries WHERE invoice_id=i.id) THEN RETURN 'external_refund_affiliate_linked'; END IF;
 IF EXISTS(SELECT 1 FROM external_refund_delivery_history WHERE order_id=o.id) OR NOT EXISTS(SELECT 1 FROM order_items WHERE order_id=o.id) OR
  (SELECT coalesce(sum(total_cents),0) FROM order_items WHERE order_id=o.id)-o.discount_cents<>o.total_cents OR
  EXISTS(SELECT 1 FROM order_items oi WHERE oi.order_id=o.id AND (oi.legacy_source<>'' OR oi.legacy_id IS NOT NULL OR oi.total_cents<>(oi.price_cents+oi.setup_fee_cents)*oi.quantity OR (SELECT count(*) FROM services WHERE order_item_id=oi.id AND order_id=o.id)<>oi.quantity)) OR
  NOT EXISTS(SELECT 1 FROM services WHERE order_id=o.id) OR
  EXISTS(SELECT 1 FROM order_items oi LEFT JOIN external_refund_order_stock_reservations z ON z.order_item_id=oi.id WHERE oi.order_id=o.id AND (z.order_item_id IS NULL OR z.order_id<>o.id OR z.product_id<>oi.product_id OR z.units<>oi.quantity OR z.item_snapshot<>external_refund_order_snapshot(oi) OR z.service_ids<>(SELECT jsonb_agg(id ORDER BY id) FROM services WHERE order_item_id=oi.id AND order_id=o.id) OR z.service_snapshots<>(SELECT jsonb_agg(external_refund_order_snapshot(s) ORDER BY s.id) FROM services s WHERE s.order_item_id=oi.id AND s.order_id=o.id))) OR
  EXISTS(SELECT 1 FROM services s LEFT JOIN order_items oi ON oi.id=s.order_item_id LEFT JOIN external_refund_service_births b ON b.service_id=s.id WHERE s.order_id=o.id AND
   (oi.id IS NULL OR oi.order_id<>o.id OR oi.product_id<>s.product_id OR s.unit_number<1 OR s.unit_number>oi.quantity OR s.user_id<>i.user_id OR s.status<>'pending_payment' OR s.provision_mode<>'manual' OR s.upstream_id<>'' OR s.username<>'' OR s.next_due_at IS NOT NULL OR s.credentials_encrypted IS NOT NULL OR s.resource_info<>'{}'::jsonb OR s.installed_os<>'{}'::jsonb OR s.last_error<>'' OR s.delivery_snapshot<>'{}'::jsonb OR s.inventory_released OR s.import_review_required OR b.service_id IS NULL OR b.order_id<>o.id OR b.identity<>external_refund_service_identity(s))) OR
  EXISTS(SELECT 1 FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE s.order_id=o.id) OR
  EXISTS(SELECT 1 FROM service_configuration_changes c JOIN services s ON s.id=c.service_id WHERE s.order_id=o.id) OR
  EXISTS(SELECT 1 FROM service_cancellation_requests c JOIN services s ON s.id=c.service_id WHERE s.order_id=o.id) OR
  EXISTS(SELECT 1 FROM invoice_items x JOIN services s ON s.id=x.service_id WHERE s.order_id=o.id)
 THEN RETURN 'external_refund_delivery_started'; END IF;
 RETURN '';
END $$;

CREATE FUNCTION protect_external_refund_order_hold() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.status IN ('released','applied') OR
  (external_refund_order_snapshot(NEW)-ARRAY['status','stock_units_released','applied_at','released_at'])<>(external_refund_order_snapshot(OLD)-ARRAY['status','stock_units_released','applied_at','released_at']) OR
  NOT((OLD.status='pending' AND NEW.status IN ('applying','released')) OR (OLD.status='applying' AND NEW.status='applied')) THEN
  RAISE EXCEPTION 'external refund order hold is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER external_refund_order_hold_immutable BEFORE UPDATE OR DELETE ON external_refund_order_holds FOR EACH ROW EXECUTE FUNCTION protect_external_refund_order_hold();

CREATE FUNCTION check_external_refund_order_complete() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE h external_refund_order_holds; r external_refunds; i invoices; o orders; n bigint;
BEGIN
 IF TG_TABLE_NAME='external_refunds' THEN SELECT * INTO r FROM external_refunds WHERE id=NEW.id;
 ELSE SELECT * INTO r FROM external_refunds WHERE id=NEW.refund_id; END IF;
 SELECT * INTO i FROM invoices WHERE id=r.invoice_id;
 IF i.order_id IS NULL THEN RETURN NEW; END IF;
 SELECT * INTO h FROM external_refund_order_holds WHERE refund_id=r.id;
 SELECT * INTO o FROM orders WHERE id=i.order_id;
 IF h.refund_id IS NULL OR h.order_id<>i.order_id OR h.invoice_id<>r.invoice_id OR h.payment_id<>r.payment_id OR h.user_id<>r.user_id OR h.amount_cents<>r.amount_cents OR r.amount_cents<>i.amount_cents OR r.user_id<>i.user_id OR NOT EXISTS(SELECT 1 FROM payment_transactions p WHERE p.id=r.payment_id AND p.invoice_id=i.id AND p.user_id=r.user_id AND p.amount_cents=r.amount_cents AND p.status='succeeded') THEN
  RAISE EXCEPTION 'external refund order associations invalid' USING ERRCODE='23514'; END IF;
 SELECT count(*) INTO n FROM external_refund_order_services WHERE refund_id=r.id;
 IF n=0 OR n<>(SELECT count(*) FROM services WHERE order_id=h.order_id) OR EXISTS(SELECT 1 FROM external_refund_order_services x JOIN services s ON s.id=x.service_id WHERE x.refund_id=r.id AND (s.order_id<>h.order_id OR s.order_item_id<>x.order_item_id OR s.product_id<>x.product_id OR s.unit_number<>x.unit_number)) THEN RAISE EXCEPTION 'external refund order service set invalid' USING ERRCODE='23514'; END IF;
 IF h.status='pending' THEN
  IF r.status<>'pending_review' OR external_refund_order_reason(i.id)<>'' OR i.version<>h.invoice_version OR o.version<>h.order_version OR external_refund_order_snapshot(i)<>h.invoice_snapshot OR external_refund_order_snapshot(o)<>h.order_snapshot OR (SELECT jsonb_agg(external_refund_order_snapshot(oi) ORDER BY oi.id) FROM order_items oi WHERE oi.order_id=o.id)<>h.items_snapshot OR h.applied_at IS NOT NULL OR h.released_at IS NOT NULL OR h.stock_units_released<>0 OR
   EXISTS(SELECT 1 FROM external_refund_order_services x JOIN services s ON s.id=x.service_id WHERE x.refund_id=r.id AND (x.snapshot<>external_refund_order_snapshot(s) OR x.version<>s.version)) THEN RAISE EXCEPTION 'external refund order pending invalid' USING ERRCODE='23514'; END IF;
 ELSIF h.status='released' THEN
  IF r.status<>'rejected' OR h.released_at IS NULL OR h.applied_at IS NOT NULL OR h.stock_units_released<>0 OR EXISTS(SELECT 1 FROM external_refund_order_stock_releases WHERE refund_id=r.id) THEN RAISE EXCEPTION 'external refund order release invalid' USING ERRCODE='23514'; END IF;
 ELSIF h.status='applied' THEN
  IF r.status<>'confirmed' OR h.applied_at IS NULL OR h.released_at IS NOT NULL OR i.status<>'refunded' OR o.status<>'refunded' OR i.version<>h.invoice_version+1 OR o.version<>h.order_version+1 OR
   (external_refund_order_snapshot(i)-ARRAY['status','version','updated_at'])<>(h.invoice_snapshot-ARRAY['status','version','updated_at']) OR
   (external_refund_order_snapshot(o)-ARRAY['status','version','updated_at'])<>(h.order_snapshot-ARRAY['status','version','updated_at']) OR
   (SELECT jsonb_agg(external_refund_order_snapshot(oi) ORDER BY oi.id) FROM order_items oi WHERE oi.order_id=o.id)<>h.items_snapshot OR h.stock_units_released<>n OR
   EXISTS(SELECT 1 FROM external_refund_order_services x JOIN services s ON s.id=x.service_id WHERE x.refund_id=r.id AND (s.status<>'cancelled' OR NOT s.inventory_released OR s.version<>x.version+1 OR (external_refund_order_snapshot(s)-ARRAY['status','inventory_released','version','updated_at'])<>(x.snapshot-ARRAY['status','inventory_released','version','updated_at']))) OR
   EXISTS(SELECT 1 FROM (SELECT product_id,count(*) AS units FROM external_refund_order_services WHERE refund_id=r.id GROUP BY product_id) x LEFT JOIN external_refund_order_stock_releases z ON z.refund_id=r.id AND z.product_id=x.product_id WHERE z.product_id IS NULL OR z.units<>x.units) OR
   (SELECT count(*) FROM external_refund_order_stock_releases WHERE refund_id=r.id)<>(SELECT count(DISTINCT product_id) FROM external_refund_order_services WHERE refund_id=r.id) OR
   EXISTS(SELECT 1 FROM external_refund_order_stock_releases z JOIN products p ON p.id=z.product_id WHERE z.refund_id=r.id AND p.stock IS DISTINCT FROM z.stock_after) OR
   NOT EXISTS(SELECT 1 FROM audit_events WHERE action='external_refund.order_reversed' AND entity_type='external_refund' AND entity_id=r.id::text AND actor_id=r.reviewed_by)
  THEN RAISE EXCEPTION 'external refund order application incomplete' USING ERRCODE='23514'; END IF;
 ELSE RAISE EXCEPTION 'external refund order application incomplete' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER external_refund_order_complete AFTER INSERT OR UPDATE ON external_refund_order_holds DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_external_refund_order_complete();
CREATE CONSTRAINT TRIGGER external_refund_order_fact_complete AFTER INSERT OR UPDATE ON external_refunds DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_external_refund_order_complete();

CREATE FUNCTION guard_external_refund_order_service_fact() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE h external_refund_order_holds; s services; stock_now int;
BEGIN
 BEGIN
  SELECT * INTO h FROM external_refund_order_holds WHERE refund_id=NEW.refund_id FOR UPDATE NOWAIT;
  SELECT * INTO s FROM services WHERE id=NEW.service_id FOR UPDATE NOWAIT;
 EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END;
 SELECT stock INTO stock_now FROM products WHERE id=s.product_id;
 IF h.status<>'pending' OR s.order_id<>h.order_id OR NEW.snapshot<>external_refund_order_snapshot(s) OR NEW.order_item_id<>s.order_item_id OR NEW.product_id<>s.product_id OR NEW.unit_number<>s.unit_number OR NEW.version<>s.version OR NEW.stock_at_request IS DISTINCT FROM stock_now THEN
  RAISE EXCEPTION 'external refund order snapshot invalid' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER external_refund_order_service_fact BEFORE INSERT ON external_refund_order_services FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_service_fact();

-- The immutable release fact performs the stock mutation itself under the
-- product lock. Callers cannot manufacture arithmetic without releasing stock.
CREATE FUNCTION apply_external_refund_order_stock() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE h external_refund_order_holds; actual_units bigint; stock_now int;
BEGIN
 SELECT * INTO h FROM external_refund_order_holds WHERE refund_id=NEW.refund_id FOR UPDATE;
 SELECT count(*) INTO actual_units FROM external_refund_order_services WHERE refund_id=h.refund_id AND product_id=NEW.product_id;
 IF h.status<>'applying' OR actual_units=0 OR NEW.units<>actual_units THEN RAISE EXCEPTION 'external refund stock release invalid' USING ERRCODE='23514'; END IF;
 SELECT stock INTO stock_now FROM products WHERE id=NEW.product_id FOR UPDATE;
 IF NOT FOUND OR (stock_now IS NOT NULL AND stock_now::bigint+actual_units>2147483647) THEN RAISE EXCEPTION 'external refund stock overflow' USING ERRCODE='23514'; END IF;
 NEW.stock_before:=stock_now;
 NEW.stock_after:=stock_now+actual_units;
 UPDATE products SET stock=NEW.stock_after WHERE id=NEW.product_id;
 RETURN NEW;
END $$;
CREATE TRIGGER external_refund_order_stock_apply BEFORE INSERT ON external_refund_order_stock_releases FOR EACH ROW EXECUTE FUNCTION apply_external_refund_order_stock();

-- Invoice and service association writers serialize on their actual parent,
-- then read the hold in a fresh statement. No association writer takes order
-- after a service row, and refund never locks an association child.
CREATE FUNCTION guard_external_refund_order_invoice() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE h text;
BEGIN
 IF TG_OP='INSERT' OR (TG_OP='UPDATE' AND NEW.order_id IS DISTINCT FROM OLD.order_id) THEN
  IF NEW.order_id IS NOT NULL THEN
   BEGIN PERFORM id FROM orders WHERE id=NEW.order_id FOR KEY SHARE NOWAIT;
   EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END;
   IF external_refund_order_blocked(NEW.order_id) THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END IF;
  END IF;
 END IF;
 IF TG_OP<>'INSERT' THEN
  SELECT status INTO h FROM external_refund_order_holds WHERE invoice_id=OLD.id AND status<>'released';
  IF h IS NOT NULL AND (TG_OP='DELETE' OR h<>'applying' OR NEW.status<>'refunded' OR (external_refund_order_snapshot(NEW)-ARRAY['status','version','updated_at'])<>(external_refund_order_snapshot(OLD)-ARRAY['status','version','updated_at'])) THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END IF;
  IF TG_OP='UPDATE' AND (NEW.legacy_source IS DISTINCT FROM OLD.legacy_source OR NEW.legacy_id IS DISTINCT FROM OLD.legacy_id OR NEW.import_review_required IS DISTINCT FROM OLD.import_review_required) THEN
   IF OLD.order_id IS NOT NULL THEN INSERT INTO external_refund_delivery_history(order_id,event) VALUES(OLD.order_id,'invoice_origin_changed'); END IF;
   IF NEW.order_id IS NOT NULL AND NEW.order_id IS DISTINCT FROM OLD.order_id THEN INSERT INTO external_refund_delivery_history(order_id,event) VALUES(NEW.order_id,'invoice_origin_changed'); END IF;
  END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE TRIGGER zz_external_refund_order_invoice BEFORE INSERT OR UPDATE OR DELETE ON invoices FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_invoice();

CREATE FUNCTION guard_external_refund_order_association() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; oid bigint; iid bigint; sid bigint; item record;
BEGIN
 -- An existing commission already excludes its invoice/order from refund
 -- qualification. Bucket movement preserves every association and must not
 -- invert withdrawal account -> entry into financial invoice -> account locks.
 -- Subtract an explicit mutable-field allowlist so future identity fields are
 -- guarded by default, as are INSERT/DELETE and every association change.
 IF TG_TABLE_NAME='affiliate_commission_entries' AND TG_OP='UPDATE' AND
  (external_refund_order_snapshot(NEW)-ARRAY['status','pending_cents','available_cents','reserved_cents','paid_cents','reversed_cents','reversal_at'])=
  (external_refund_order_snapshot(OLD)-ARRAY['status','pending_cents','available_cents','reserved_cents','paid_cents','reversed_cents','reversal_at']) THEN RETURN NEW; END IF;

 FOR v IN SELECT value FROM jsonb_array_elements(CASE TG_OP WHEN 'INSERT' THEN jsonb_build_array(external_refund_order_snapshot(NEW)) WHEN 'DELETE' THEN jsonb_build_array(external_refund_order_snapshot(OLD)) ELSE jsonb_build_array(external_refund_order_snapshot(OLD),external_refund_order_snapshot(NEW)) END) LOOP
  iid:=coalesce((v->>'invoice_id')::bigint,(v->>'source_invoice_id')::bigint);
  sid:=(v->>'service_id')::bigint;
  IF iid IS NOT NULL THEN
   BEGIN SELECT order_id INTO oid FROM invoices WHERE id=iid FOR UPDATE NOWAIT;
   EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END;
   IF external_refund_order_blocked(oid) THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END IF;
  END IF;
  IF sid IS NOT NULL THEN
   SELECT order_id INTO oid FROM services WHERE id=sid FOR UPDATE;
   IF external_refund_order_blocked(oid) THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END IF;
  END IF;
  IF TG_TABLE_NAME='affiliate_commission_entries' AND (v->>'order_id')::bigint IS NOT NULL THEN
   -- Every order commission also has the invoice parent above. A missing or
   -- wrong invoice must not bypass protection of the order association.
   BEGIN PERFORM id FROM orders WHERE id=(v->>'order_id')::bigint FOR KEY SHARE NOWAIT; EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END;
   IF external_refund_order_blocked((v->>'order_id')::bigint) THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END IF;
  END IF;
 END LOOP;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE TRIGGER external_refund_order_invoice_item_guard BEFORE INSERT OR UPDATE OR DELETE ON invoice_items FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_association();
CREATE TRIGGER external_refund_order_config_guard BEFORE INSERT OR UPDATE OR DELETE ON service_configuration_changes FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_association();
CREATE TRIGGER external_refund_order_cancel_guard BEFORE INSERT OR UPDATE OR DELETE ON service_cancellation_requests FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_association();
CREATE TRIGGER external_refund_order_affiliate_guard BEFORE INSERT OR UPDATE OR DELETE ON affiliate_commission_entries FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_association();
CREATE TRIGGER external_refund_order_turnover_guard BEFORE INSERT OR UPDATE OR DELETE ON affiliate_turnover_entries FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_association();
CREATE TRIGGER external_refund_order_credit_guard BEFORE INSERT OR UPDATE OR DELETE ON credit_invoice_payments FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_association();
CREATE TRIGGER external_refund_order_traffic_guard BEFORE INSERT OR UPDATE OR DELETE ON traffic_orders FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_association();
CREATE TRIGGER external_refund_order_application_guard BEFORE INSERT OR UPDATE OR DELETE ON invoice_application_items FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_association();

CREATE FUNCTION external_refund_order_reversal_json(target_refund bigint) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT jsonb_build_object('status',CASE h.status WHEN 'applying' THEN 'pending' ELSE h.status END,'order_id',h.order_id,'invoice_version',h.invoice_version,'order_version',h.order_version,'service_count',(SELECT count(*) FROM external_refund_order_services WHERE refund_id=h.refund_id),'stock_units_released',h.stock_units_released,'created_at',h.created_at,'applied_at',h.applied_at,'released_at',h.released_at) FROM external_refund_order_holds h WHERE h.refund_id=target_refund;
$$;
CREATE FUNCTION external_refund_order_hold_json(target_order bigint) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT external_refund_order_reversal_json(h.refund_id) FROM external_refund_order_holds h WHERE h.order_id=target_order ORDER BY h.refund_id DESC LIMIT 1;
$$;

-- Both customer cart and staff checkout insert this fact instead of changing
-- stock directly. The DB records the real subtraction even for infinite stock.
CREATE FUNCTION reserve_external_refund_order_stock() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE oi order_items; o orders; actual_services bigint; stock_now int;
BEGIN
 SELECT * INTO oi FROM order_items WHERE id=NEW.order_item_id FOR UPDATE;
 BEGIN SELECT * INTO o FROM orders WHERE id=oi.order_id FOR UPDATE NOWAIT;
 EXCEPTION WHEN lock_not_available THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END;
 IF oi.id IS NULL OR o.status<>'pending' OR NOT EXISTS(SELECT 1 FROM external_refund_order_births WHERE order_id=o.id) OR EXISTS(SELECT 1 FROM invoices WHERE order_id=o.id) OR NEW.order_id<>o.id OR NEW.product_id<>oi.product_id OR NEW.units<>oi.quantity THEN RAISE EXCEPTION 'external refund stock reservation invalid' USING ERRCODE='23514'; END IF;
 SELECT count(*) INTO actual_services FROM services WHERE order_item_id=oi.id AND order_id=o.id AND product_id=oi.product_id AND user_id=o.user_id AND status='pending_payment' AND NOT inventory_released AND unit_number BETWEEN 1 AND oi.quantity;
 IF actual_services<>oi.quantity THEN RAISE EXCEPTION 'external refund stock reservation service set invalid' USING ERRCODE='23514'; END IF;
 SELECT stock INTO stock_now FROM products WHERE id=oi.product_id FOR UPDATE;
 IF NOT FOUND OR stock_now<oi.quantity THEN RAISE EXCEPTION 'external refund stock reservation insufficient' USING ERRCODE='23514'; END IF;
 NEW.item_snapshot:=external_refund_order_snapshot(oi);
 SELECT jsonb_agg(id ORDER BY id),jsonb_agg(external_refund_order_snapshot(s) ORDER BY id) INTO NEW.service_ids,NEW.service_snapshots FROM services s WHERE order_item_id=oi.id AND order_id=o.id;
 NEW.stock_before:=stock_now; NEW.stock_after:=stock_now-oi.quantity;
 UPDATE products SET stock=NEW.stock_after,updated_at=clock_timestamp() WHERE id=oi.product_id;
 RETURN NEW;
END $$;
CREATE TRIGGER external_refund_order_stock_reserve BEFORE INSERT ON external_refund_order_stock_reservations FOR EACH ROW EXECUTE FUNCTION reserve_external_refund_order_stock();
CREATE TRIGGER external_refund_order_stock_reservation_immutable BEFORE UPDATE OR DELETE ON external_refund_order_stock_reservations FOR EACH ROW EXECUTE FUNCTION immutable_external_refund_order_detail();

CREATE FUNCTION validate_external_refund_order_hold() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE i invoices; o orders; r external_refunds;
BEGIN
 SELECT * INTO r FROM external_refunds WHERE id=NEW.refund_id;
 SELECT * INTO i FROM invoices WHERE id=NEW.invoice_id FOR UPDATE;
 SELECT * INTO o FROM orders WHERE id=NEW.order_id FOR UPDATE;
 IF r.status<>'pending_review' OR NEW.status<>'pending' OR NEW.stock_units_released<>0 OR NEW.applied_at IS NOT NULL OR NEW.released_at IS NOT NULL OR
 NEW.invoice_snapshot<>external_refund_order_snapshot(i) OR NEW.order_snapshot<>external_refund_order_snapshot(o) OR NEW.invoice_version<>i.version OR NEW.order_version<>o.version OR
 NEW.items_snapshot IS DISTINCT FROM (SELECT jsonb_agg(external_refund_order_snapshot(oi) ORDER BY oi.id) FROM order_items oi WHERE oi.order_id=o.id) OR external_refund_order_reason(i.id)<>'' THEN RAISE EXCEPTION 'external refund order hold snapshot invalid' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER external_refund_order_hold_validate BEFORE INSERT ON external_refund_order_holds FOR EACH ROW EXECUTE FUNCTION validate_external_refund_order_hold();

CREATE FUNCTION guard_external_refund_order_balance() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v jsonb; oid bigint;
BEGIN
 FOR v IN SELECT value FROM jsonb_array_elements(CASE TG_OP WHEN 'INSERT' THEN jsonb_build_array(external_refund_order_snapshot(NEW)) WHEN 'DELETE' THEN jsonb_build_array(external_refund_order_snapshot(OLD)) ELSE jsonb_build_array(external_refund_order_snapshot(OLD),external_refund_order_snapshot(NEW)) END) LOOP
  IF v->>'reference_type'='invoice' AND v->>'entry_type' IN ('payment','refund') THEN
   SELECT order_id INTO oid FROM invoices WHERE id=(v->>'reference_id')::bigint FOR UPDATE;
   IF external_refund_order_blocked(oid) THEN RAISE EXCEPTION 'external_refund_order_hold' USING ERRCODE='23514'; END IF;
  END IF;
 END LOOP;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE TRIGGER external_refund_order_balance_guard BEFORE INSERT OR UPDATE OR DELETE ON balance_ledger FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_balance();
CREATE TRIGGER external_refund_order_payment_guard BEFORE INSERT OR UPDATE OR DELETE ON payment_transactions FOR EACH ROW EXECUTE FUNCTION guard_external_refund_order_association();

CREATE CONSTRAINT TRIGGER external_refund_order_stock_complete AFTER INSERT ON external_refund_order_stock_releases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_external_refund_order_complete();
CREATE CONSTRAINT TRIGGER external_refund_order_service_complete AFTER INSERT ON external_refund_order_services DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_external_refund_order_complete();
