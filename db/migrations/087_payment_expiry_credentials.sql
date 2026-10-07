ALTER TABLE payment_transactions DROP CONSTRAINT payment_transactions_status_check;
ALTER TABLE payment_transactions ADD CONSTRAINT payment_transactions_status_check
 CHECK (status IN ('pending','expired','succeeded','failed'));
ALTER TABLE payment_transactions ADD COLUMN credential_snapshot bytea NOT NULL DEFAULT ''::bytea;

CREATE FUNCTION preserve_payment_credentials() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF octet_length(OLD.credential_snapshot)>0 AND NEW.credential_snapshot IS DISTINCT FROM OLD.credential_snapshot THEN
  RAISE EXCEPTION 'payment credentials are immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER payment_credentials_immutable BEFORE UPDATE ON payment_transactions
 FOR EACH ROW EXECUTE FUNCTION preserve_payment_credentials();
