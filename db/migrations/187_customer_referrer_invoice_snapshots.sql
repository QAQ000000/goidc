-- Deploy with all old invoice/attribution writers stopped. Current referral
-- changes only affect invoices inserted after that change; never reprice,
-- transfer, recalculate or rewrite existing financial facts.
ALTER TABLE affiliate_attributions ALTER COLUMN link_id DROP NOT NULL;
ALTER TABLE affiliate_attributions ALTER COLUMN click_id DROP NOT NULL;

CREATE TABLE customer_referral_states (
 user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0)
);
INSERT INTO customer_referral_states(user_id) SELECT id FROM users;

CREATE TABLE customer_referral_changes (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 old_account_id bigint REFERENCES affiliate_accounts(id) ON DELETE RESTRICT,
 new_account_id bigint REFERENCES affiliate_accounts(id) ON DELETE RESTRICT,
 actor_id bigint NOT NULL REFERENCES administrators(id) ON DELETE RESTRICT,
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 500),
 version_before bigint NOT NULL CHECK(version_before>0),
 version_after bigint NOT NULL CHECK(version_after=version_before+1),
 previous_attribution jsonb NOT NULL CHECK(jsonb_typeof(previous_attribution)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(user_id,version_after),
 CHECK(old_account_id IS DISTINCT FROM new_account_id)
);
CREATE INDEX customer_referral_history ON customer_referral_changes(user_id,id DESC);

CREATE TABLE invoice_affiliate_attributions (
 invoice_id bigint PRIMARY KEY REFERENCES invoices(id) ON DELETE RESTRICT,
 -- Schema182 preserves pre-split administrator commerce under its original
 -- principal. Historical facts must not fabricate a customer or lose that owner;
 -- invoices and current assignment writers retain their customer-only guards.
 user_id bigint NOT NULL REFERENCES account_principals(id) ON DELETE RESTRICT,
 account_id bigint REFERENCES affiliate_accounts(id) ON DELETE RESTRICT,
 referrer_user_id bigint REFERENCES account_principals(id) ON DELETE RESTRICT,
 referral_version bigint NOT NULL CHECK(referral_version>0),
 source_snapshot jsonb NOT NULL CHECK(jsonb_typeof(source_snapshot)='object'),
 capture_basis text NOT NULL CHECK(capture_basis IN ('legacy_financial','legacy_current','invoice_birth')),
 captured_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((account_id IS NULL)=(referrer_user_id IS NULL)),
 CHECK(referrer_user_id IS NULL OR referrer_user_id<>user_id)
);
CREATE INDEX invoice_affiliate_account ON invoice_affiliate_attributions(account_id,invoice_id) WHERE account_id IS NOT NULL;
-- Ambiguous pre-upgrade financial ownership is not guessed or overwritten.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM (
  SELECT invoice_id,account_id FROM affiliate_commission_entries
  UNION SELECT invoice_id,account_id FROM affiliate_turnover_entries
 ) facts GROUP BY invoice_id HAVING count(DISTINCT account_id)>1) THEN
  RAISE EXCEPTION 'conflicting historical affiliate invoice ownership';
 END IF;
 IF EXISTS(SELECT 1 FROM (
  SELECT invoice_id,account_id FROM affiliate_commission_entries
  UNION SELECT invoice_id,account_id FROM affiliate_turnover_entries
 ) facts JOIN invoices i ON i.id=facts.invoice_id JOIN affiliate_accounts a ON a.id=facts.account_id
 WHERE a.user_id=i.user_id) THEN
  RAISE EXCEPTION 'historical affiliate invoice self referral';
 END IF;
END $$;
INSERT INTO invoice_affiliate_attributions(invoice_id,user_id,account_id,referrer_user_id,referral_version,source_snapshot,capture_basis)
WITH financial AS (
 SELECT invoice_id,min(account_id) account_id FROM (
  SELECT invoice_id,account_id FROM affiliate_commission_entries
  UNION SELECT invoice_id,account_id FROM affiliate_turnover_entries
 ) facts GROUP BY invoice_id
), resolved AS (
 SELECT i.id,i.user_id,coalesce(f.account_id,CASE WHEN at.status='active' AND a.user_id<>i.user_id THEN at.account_id END) account_id,
 f.account_id IS NOT NULL financial,coalesce(st.version,1) version,
 CASE WHEN f.account_id IS NOT NULL THEN jsonb_build_object('historical_account_id',f.account_id)
 ELSE coalesce(at.source_snapshot,'{}'::jsonb) END source
 FROM invoices i LEFT JOIN financial f ON f.invoice_id=i.id
 LEFT JOIN affiliate_attributions at ON at.user_id=i.user_id
 LEFT JOIN affiliate_accounts a ON a.id=at.account_id
 LEFT JOIN customer_referral_states st ON st.user_id=i.user_id
)
SELECT r.id,r.user_id,r.account_id,a.user_id,r.version,r.source,
 CASE WHEN r.financial THEN 'legacy_financial' ELSE 'legacy_current' END
FROM resolved r LEFT JOIN affiliate_accounts a ON a.id=r.account_id;

-- A single graph lock serializes assignment and cycle checks. Invoice birth
-- holds its shared counterpart. Neither function acquires a customer row lock:
-- normal invoice writers already own user/finance rows before INSERT.
CREATE FUNCTION guard_affiliate_referral_graph() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE parent bigint; invalid boolean;
BEGIN
 -- The advisory wait refreshes subsequent statement snapshots only at RC.
 -- Snapshot-isolated writers could otherwise miss a committed parent change.
 IF current_setting('transaction_isolation')<>'read committed' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='affiliate_referral_requires_read_committed';
 END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended('affiliate-referral-graph',0));
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'affiliate referral history cannot be deleted'; END IF;
 IF TG_OP='UPDATE' AND NEW.user_id<>OLD.user_id THEN RAISE EXCEPTION 'affiliate referral customer is immutable'; END IF;
 IF NEW.status='active' THEN
  SELECT user_id INTO STRICT parent FROM affiliate_accounts WHERE id=NEW.account_id;
  WITH RECURSIVE chain(id,path,cycle) AS (
   SELECT parent,ARRAY[parent],false
   UNION ALL
   SELECT a.user_id,c.path||a.user_id,a.user_id=ANY(c.path)
   FROM chain c JOIN affiliate_attributions at ON at.user_id=c.id AND at.status='active'
   JOIN affiliate_accounts a ON a.id=at.account_id WHERE NOT c.cycle
  ) SELECT EXISTS(SELECT 1 FROM chain WHERE id=NEW.user_id OR cycle) INTO invalid;
  IF invalid THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='affiliate_referral_cycle'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER affiliate_referral_graph BEFORE INSERT OR UPDATE OR DELETE ON affiliate_attributions
 FOR EACH ROW EXECUTE FUNCTION guard_affiliate_referral_graph();
CREATE FUNCTION advance_customer_referral_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' OR NEW.account_id IS DISTINCT FROM OLD.account_id OR NEW.status IS DISTINCT FROM OLD.status THEN
  INSERT INTO customer_referral_states(user_id,version) VALUES(NEW.user_id,2)
  ON CONFLICT(user_id) DO UPDATE SET version=customer_referral_states.version+1;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER customer_referral_version AFTER INSERT OR UPDATE ON affiliate_attributions
 FOR EACH ROW EXECUTE FUNCTION advance_customer_referral_version();

CREATE FUNCTION capture_invoice_affiliate_attribution() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE aid bigint; referrer bigint; source jsonb; v bigint;
BEGIN
 -- An invoice must have a fresh, explicit attributed-or-unattributed birth fact;
 -- a repeatable snapshot cannot become current after waiting on the graph.
 IF current_setting('transaction_isolation')<>'read committed' THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='affiliate_referral_requires_read_committed';
 END IF;
 PERFORM pg_advisory_xact_lock_shared(hashtextextended('affiliate-referral-graph',0));
 SELECT at.account_id,a.user_id,at.source_snapshot INTO aid,referrer,source
 FROM affiliate_attributions at JOIN affiliate_accounts a ON a.id=at.account_id
 WHERE at.user_id=NEW.user_id AND at.status='active' AND a.user_id<>NEW.user_id;
 SELECT coalesce((SELECT version FROM customer_referral_states WHERE user_id=NEW.user_id),1) INTO v;
 INSERT INTO invoice_affiliate_attributions(invoice_id,user_id,account_id,referrer_user_id,referral_version,source_snapshot,capture_basis)
 VALUES(NEW.id,NEW.user_id,aid,referrer,v,coalesce(source,'{}'::jsonb),'invoice_birth');
 RETURN NEW;
END $$;
CREATE TRIGGER invoice_affiliate_birth AFTER INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION capture_invoice_affiliate_attribution();
CREATE FUNCTION immutable_referral_fact() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'affiliate referral fact is immutable'; END $$;
CREATE TRIGGER invoice_affiliate_immutable BEFORE UPDATE OR DELETE ON invoice_affiliate_attributions
 FOR EACH ROW EXECUTE FUNCTION immutable_referral_fact();
CREATE TRIGGER customer_referral_history_immutable BEFORE UPDATE OR DELETE ON customer_referral_changes
 FOR EACH ROW EXECUTE FUNCTION immutable_referral_fact();
CREATE FUNCTION guard_invoice_referral_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.user_id<>OLD.user_id THEN RAISE EXCEPTION 'invoice referral owner is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER invoice_referral_owner BEFORE UPDATE OF user_id ON invoices FOR EACH ROW EXECUTE FUNCTION guard_invoice_referral_owner();
