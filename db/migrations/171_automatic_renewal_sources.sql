-- No historical key prefix proves that a customer authorized an automatic
-- collection. Only invoices created by the new automatic path gain a source.
CREATE TABLE auto_renewal_invoice_sources (
 invoice_id bigint PRIMARY KEY REFERENCES invoices(id) ON DELETE RESTRICT,
 service_id bigint NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 expected_due timestamptz NOT NULL CHECK(isfinite(expected_due)),
 generation bigint NOT NULL CHECK(generation>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX auto_renewal_invoice_sources_service ON auto_renewal_invoice_sources(service_id,invoice_id);

CREATE FUNCTION protect_auto_renewal_invoice_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'automatic renewal invoice source is immutable' USING ERRCODE='23514';
END $$;
CREATE TRIGGER auto_renewal_invoice_source_immutable BEFORE UPDATE OR DELETE ON auto_renewal_invoice_sources
 FOR EACH ROW EXECUTE FUNCTION protect_auto_renewal_invoice_source();

CREATE FUNCTION validate_auto_renewal_invoice_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM invoices i JOIN services s ON s.id=NEW.service_id
 WHERE i.id=NEW.invoice_id AND i.user_id=NEW.user_id AND s.user_id=NEW.user_id
  AND i.status='unpaid' AND i.order_id IS NULL AND i.invoice_kind='automatic'
  AND NOT i.import_review_required AND NOT s.import_review_required
  AND s.auto_renew AND s.status IN ('active','suspended') AND s.billing_cycle<>'once'
  AND s.next_due_at=NEW.expected_due AND s.renewal_generation=NEW.generation
  AND (SELECT count(*) FROM invoice_items WHERE invoice_id=i.id)=1
  AND EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.item_type='renew'
   AND ii.service_id=s.id AND ii.amount_cents=i.amount_cents)) THEN
  RAISE EXCEPTION 'automatic renewal invoice source association mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER auto_renewal_invoice_source_association BEFORE INSERT ON auto_renewal_invoice_sources
 FOR EACH ROW EXECUTE FUNCTION validate_auto_renewal_invoice_source();
