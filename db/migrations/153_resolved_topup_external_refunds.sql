-- Credit proof is shared by the application and reservation trigger. A top-up
-- marked credited alone is insufficient: follow either the original settlement
-- ledger or the immutable liability resolution to its actual positive ledger.
CREATE FUNCTION external_refund_topup_credit_valid(target_invoice bigint, target_payment bigint, target_user bigint, target_amount bigint)
RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS (
  SELECT 1 FROM invoices i
  JOIN payment_transactions p ON p.invoice_id=i.id
  JOIN balance_topups t ON t.invoice_id=i.id
  JOIN balance_topup_settlements s ON s.topup_id=t.id
  LEFT JOIN balance_topup_liability_resolutions x ON x.topup_id=t.id
  JOIN balance_ledger l ON l.id=CASE WHEN s.status='credited' THEN s.ledger_id ELSE x.ledger_id END
  WHERE i.id=target_invoice AND i.user_id=target_user AND i.amount_cents=target_amount AND i.invoice_kind='topup' AND i.status='paid'
   AND p.id=target_payment AND p.user_id=target_user AND p.amount_cents=target_amount AND p.status='succeeded'
   AND t.user_id=target_user AND t.amount_cents=target_amount AND t.status='credited'
   AND s.invoice_id=i.id AND s.payment_id=p.id AND s.user_id=target_user AND s.amount_cents=target_amount
   AND ((s.status='credited' AND s.ledger_id IS NOT NULL AND x.id IS NULL)
     OR (s.status='pending_liability' AND s.ledger_id IS NULL
       AND x.invoice_id=i.id AND x.payment_id=p.id AND x.user_id=target_user AND x.amount_cents=target_amount))
   AND l.user_id=target_user AND l.reference_type='gateway_topup' AND l.reference_id=i.id AND l.entry_type='topup' AND l.delta_cents=target_amount
 );
$$;

CREATE OR REPLACE FUNCTION validate_external_refund_reversal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.status='released' THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM external_refunds r
   WHERE r.id=NEW.refund_id AND r.invoice_id=NEW.invoice_id AND r.user_id=NEW.user_id AND r.amount_cents=NEW.amount_cents
   AND r.status='pending_review'
   AND external_refund_topup_credit_valid(NEW.invoice_id,r.payment_id,NEW.user_id,NEW.amount_cents))
 THEN RAISE EXCEPTION 'external refund reversal associations invalid' USING ERRCODE='23514'; END IF;
 IF NEW.status='applied' AND NOT EXISTS(SELECT 1 FROM balance_ledger l WHERE l.id=NEW.ledger_id
   AND l.user_id=NEW.user_id AND l.reference_type='invoice' AND l.reference_id=NEW.invoice_id
   AND l.entry_type='refund' AND l.delta_cents=-NEW.amount_cents
   AND l.idempotency_key='external-refund-balance-'||NEW.refund_id::text)
 THEN RAISE EXCEPTION 'external refund reversal ledger invalid' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
