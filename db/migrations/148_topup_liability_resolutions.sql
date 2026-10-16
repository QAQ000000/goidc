CREATE TABLE balance_topup_liability_resolutions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 topup_id bigint NOT NULL UNIQUE REFERENCES balance_topup_settlements(topup_id),
 invoice_id bigint NOT NULL UNIQUE REFERENCES invoices(id),
 payment_id bigint NOT NULL UNIQUE REFERENCES payment_transactions(id),
 user_id bigint NOT NULL REFERENCES users(id), amount_cents bigint NOT NULL CHECK(amount_cents>0),
 ledger_id bigint NOT NULL UNIQUE REFERENCES balance_ledger(id),
 actor_id bigint NOT NULL REFERENCES users(id), note text NOT NULL CHECK(char_length(note) BETWEEN 1 AND 1000),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 idempotency_key text NOT NULL CHECK(char_length(idempotency_key) BETWEEN 8 AND 128),
 request_hash text NOT NULL, result jsonb NOT NULL,
 UNIQUE(actor_id,idempotency_key)
);
CREATE FUNCTION immutable_topup_liability_resolution() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'topup liability resolution is immutable'; END $$;
CREATE TRIGGER protect_topup_liability_resolution BEFORE UPDATE OR DELETE ON balance_topup_liability_resolutions FOR EACH ROW EXECUTE FUNCTION immutable_topup_liability_resolution();
CREATE FUNCTION validate_topup_liability_resolution() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM balance_topup_settlements s WHERE s.topup_id=NEW.topup_id AND s.invoice_id=NEW.invoice_id AND s.payment_id=NEW.payment_id AND s.user_id=NEW.user_id AND s.amount_cents=NEW.amount_cents AND s.status='pending_liability' AND s.ledger_id IS NULL) OR
 NOT EXISTS(SELECT 1 FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id JOIN balance_topups t ON t.invoice_id=i.id WHERE p.id=NEW.payment_id AND p.invoice_id=NEW.invoice_id AND p.user_id=NEW.user_id AND p.amount_cents=NEW.amount_cents AND p.status='succeeded' AND i.user_id=NEW.user_id AND i.amount_cents=NEW.amount_cents AND i.invoice_kind='topup' AND i.status='paid' AND t.id=NEW.topup_id AND t.user_id=NEW.user_id AND t.amount_cents=NEW.amount_cents) OR
 NOT EXISTS(SELECT 1 FROM balance_ledger l WHERE l.id=NEW.ledger_id AND l.user_id=NEW.user_id AND l.entry_type='topup' AND l.reference_type='gateway_topup' AND l.reference_id=NEW.invoice_id AND l.delta_cents=NEW.amount_cents)
 THEN RAISE EXCEPTION 'topup liability resolution associations invalid' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER validate_topup_liability_resolution BEFORE INSERT ON balance_topup_liability_resolutions FOR EACH ROW EXECUTE FUNCTION validate_topup_liability_resolution();
