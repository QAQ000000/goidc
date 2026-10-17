-- A pending external refund reserves spendable balance without changing the
-- customer's displayed balance.  Only a confirmed review may apply it.
CREATE TABLE external_refund_reversal_reservations (
 refund_id bigint PRIMARY KEY REFERENCES external_refunds(id),
 invoice_id bigint NOT NULL REFERENCES invoices(id),
 user_id bigint NOT NULL REFERENCES users(id),
 amount_cents bigint NOT NULL CHECK(amount_cents>0),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','released','applied')),
 ledger_id bigint UNIQUE REFERENCES balance_ledger(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 released_at timestamptz,
 applied_at timestamptz,
 CHECK((status='pending' AND ledger_id IS NULL AND released_at IS NULL AND applied_at IS NULL) OR
       (status='released' AND ledger_id IS NULL AND released_at IS NOT NULL AND applied_at IS NULL) OR
       (status='applied' AND ledger_id IS NOT NULL AND released_at IS NULL AND applied_at IS NOT NULL))
);
CREATE UNIQUE INDEX external_refund_reversal_one_live_invoice
 ON external_refund_reversal_reservations(invoice_id) WHERE status<>'released';
CREATE INDEX external_refund_reversal_pending_user
 ON external_refund_reversal_reservations(user_id) WHERE status='pending';
CREATE FUNCTION protect_external_refund_reversal_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.status<>'pending' OR
    NEW.refund_id IS DISTINCT FROM OLD.refund_id OR NEW.invoice_id IS DISTINCT FROM OLD.invoice_id OR
    NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.amount_cents IS DISTINCT FROM OLD.amount_cents OR
    (NEW.ledger_id IS DISTINCT FROM OLD.ledger_id AND NOT(NEW.status='applied' AND OLD.ledger_id IS NULL)) OR
    NEW.created_at IS DISTINCT FROM OLD.created_at THEN
  RAISE EXCEPTION 'external refund reversal fact is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER external_refund_reversal_identity
 BEFORE UPDATE OR DELETE ON external_refund_reversal_reservations
 FOR EACH ROW EXECUTE FUNCTION protect_external_refund_reversal_identity();
CREATE FUNCTION validate_external_refund_reversal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.status='released' THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM external_refunds r JOIN invoices i ON i.id=r.invoice_id
   JOIN payment_transactions p ON p.id=r.payment_id JOIN balance_topups t ON t.invoice_id=i.id
   JOIN balance_topup_settlements s ON s.topup_id=t.id
   JOIN balance_ledger l ON l.id=s.ledger_id
   WHERE r.id=NEW.refund_id AND r.invoice_id=NEW.invoice_id AND r.user_id=NEW.user_id AND r.amount_cents=NEW.amount_cents
   AND r.status='pending_review' AND i.invoice_kind='topup' AND i.status='paid' AND i.user_id=NEW.user_id AND i.amount_cents=NEW.amount_cents
   AND p.invoice_id=i.id AND p.user_id=NEW.user_id AND p.amount_cents=NEW.amount_cents AND p.status='succeeded'
   AND t.user_id=NEW.user_id AND t.amount_cents=NEW.amount_cents AND t.status='credited'
   AND s.invoice_id=i.id AND s.user_id=NEW.user_id AND s.payment_id=p.id AND s.amount_cents=NEW.amount_cents AND s.status='credited'
   AND l.user_id=NEW.user_id AND l.reference_type='gateway_topup' AND l.reference_id=i.id AND l.entry_type='topup' AND l.delta_cents=NEW.amount_cents)
 THEN RAISE EXCEPTION 'external refund reversal associations invalid' USING ERRCODE='23514'; END IF;
 IF NEW.status='applied' AND NOT EXISTS(SELECT 1 FROM balance_ledger l WHERE l.id=NEW.ledger_id
   AND l.user_id=NEW.user_id AND l.reference_type='invoice' AND l.reference_id=NEW.invoice_id
   AND l.entry_type='refund' AND l.delta_cents=-NEW.amount_cents
   AND l.idempotency_key='external-refund-balance-'||NEW.refund_id::text)
 THEN RAISE EXCEPTION 'external refund reversal ledger invalid' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER validate_external_refund_reversal BEFORE INSERT OR UPDATE
 ON external_refund_reversal_reservations FOR EACH ROW EXECUTE FUNCTION validate_external_refund_reversal();
