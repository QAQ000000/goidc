-- Affiliate settlement lifecycle: release, withdrawal reservation and offline payout.
-- Extend the migrated operator role with the two capabilities introduced by
-- this batch. Existing role grants remain unchanged.
UPDATE admin_roles
SET permissions = ARRAY(SELECT DISTINCT unnest(permissions || ARRAY['affiliates.withdrawals.review','affiliates.payout']::text[]))
WHERE name = '现有运营管理员';

ALTER TABLE affiliate_programs
  ADD COLUMN min_withdraw_cents bigint NOT NULL DEFAULT 1 CHECK (min_withdraw_cents > 0),
  ADD COLUMN fee_cents bigint NOT NULL DEFAULT 0 CHECK (fee_cents >= 0);

CREATE SEQUENCE affiliate_withdrawal_no_seq AS bigint START WITH 100000 INCREMENT BY 1;

CREATE TABLE affiliate_withdrawals (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 withdrawal_no text NOT NULL DEFAULT ('AW-'||lpad(nextval('affiliate_withdrawal_no_seq')::text,12,'0')) UNIQUE
   CHECK (withdrawal_no ~ '^AW-[0-9]{12,}$'),
 account_id bigint NOT NULL REFERENCES affiliate_accounts(id) ON DELETE RESTRICT,
 amount_cents bigint NOT NULL CHECK (amount_cents > 0),
 fee_cents bigint NOT NULL CHECK (fee_cents >= 0 AND fee_cents <= amount_cents),
 payout_cents bigint NOT NULL CHECK (payout_cents > 0 AND payout_cents = amount_cents-fee_cents),
 status text NOT NULL DEFAULT 'submitted' CHECK (status IN ('submitted','reviewing','approved','rejected','paying','paid','failed','cancelled')),
 payout_method text NOT NULL CHECK (payout_method IN ('bank','alipay','wechat','other')),
 payout_target_ciphertext bytea NOT NULL CHECK (octet_length(payout_target_ciphertext) > 0),
 payout_target_aad text NOT NULL CHECK (char_length(payout_target_aad) BETWEEN 1 AND 200),
 payout_target_masked text NOT NULL CHECK (char_length(payout_target_masked) BETWEEN 1 AND 200),
 payout_target_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payout_target_snapshot)='object'),
 idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
 version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
 submitted_at timestamptz NOT NULL DEFAULT now(),
 reviewed_at timestamptz,
 paid_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(account_id,idempotency_key)
);
CREATE INDEX affiliate_withdrawals_queue ON affiliate_withdrawals(status,created_at,id);
CREATE INDEX affiliate_withdrawals_account ON affiliate_withdrawals(account_id,created_at DESC,id DESC);

-- Independent buckets permit partial reservations while preserving earned amounts.
ALTER TABLE affiliate_commission_entries
 ADD COLUMN pending_cents bigint NOT NULL DEFAULT 0 CHECK(pending_cents>=0),
 ADD COLUMN available_cents bigint NOT NULL DEFAULT 0 CHECK(available_cents>=0),
 ADD COLUMN reserved_cents bigint NOT NULL DEFAULT 0 CHECK(reserved_cents>=0),
 ADD COLUMN paid_cents bigint NOT NULL DEFAULT 0 CHECK(paid_cents>=0),
 ADD COLUMN reversed_cents bigint NOT NULL DEFAULT 0 CHECK(reversed_cents>=0),
 ADD COLUMN reversal_at timestamptz;
UPDATE affiliate_commission_entries SET
 pending_cents=CASE WHEN status='pending' THEN amount_cents ELSE 0 END,
 available_cents=CASE WHEN status='available' THEN amount_cents ELSE 0 END,
 reserved_cents=CASE WHEN status='reserved' THEN amount_cents ELSE 0 END,
 paid_cents=CASE WHEN status IN ('paid','clawback') THEN amount_cents ELSE 0 END,
 reversed_cents=CASE WHEN status='reversed' THEN amount_cents ELSE 0 END;
ALTER TABLE affiliate_commission_entries ADD CONSTRAINT affiliate_commission_buckets_conserved
 CHECK(pending_cents::numeric+available_cents+reserved_cents+paid_cents+reversed_cents=amount_cents);
CREATE FUNCTION affiliate_initialize_commission_buckets() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.pending_cents=0 AND NEW.available_cents=0 AND NEW.reserved_cents=0 AND NEW.paid_cents=0 AND NEW.reversed_cents=0 THEN
  IF NEW.status='pending' THEN NEW.pending_cents=NEW.amount_cents;
  ELSE RAISE EXCEPTION 'new commission must be pending'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER affiliate_initialize_commission_buckets BEFORE INSERT ON affiliate_commission_entries
 FOR EACH ROW EXECUTE FUNCTION affiliate_initialize_commission_buckets();
CREATE TABLE affiliate_withdrawal_allocations (
 withdrawal_id bigint NOT NULL REFERENCES affiliate_withdrawals(id),
 entry_id bigint NOT NULL REFERENCES affiliate_commission_entries(id),
 amount_cents bigint NOT NULL CHECK(amount_cents>0),
 status text NOT NULL CHECK(status IN ('reserved','released','paid','reversed')),
 PRIMARY KEY(withdrawal_id,entry_id)
);
CREATE INDEX affiliate_allocations_entry ON affiliate_withdrawal_allocations(entry_id,status);
CREATE TABLE affiliate_clawbacks (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 entry_id bigint NOT NULL REFERENCES affiliate_commission_entries(id),
 withdrawal_id bigint NOT NULL REFERENCES affiliate_withdrawals(id),
 amount_cents bigint NOT NULL CHECK(amount_cents>0),
 outstanding_cents bigint NOT NULL CHECK(outstanding_cents>=0 AND outstanding_cents<=amount_cents),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(entry_id,withdrawal_id)
);
ALTER TABLE affiliate_commission_events
 ADD COLUMN withdrawal_id bigint REFERENCES affiliate_withdrawals(id),
 ADD COLUMN settlement_run_id bigint,
 ADD COLUMN bucket_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE affiliate_withdrawal_reviews (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 withdrawal_id bigint NOT NULL REFERENCES affiliate_withdrawals(id) ON DELETE RESTRICT,
 reviewer_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 decision text NOT NULL CHECK (decision IN ('approved','rejected','request_info')),
 note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 2000),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX affiliate_withdrawal_reviews_withdrawal ON affiliate_withdrawal_reviews(withdrawal_id,id DESC);

CREATE TABLE affiliate_payouts (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 withdrawal_id bigint NOT NULL UNIQUE REFERENCES affiliate_withdrawals(id) ON DELETE RESTRICT,
 method text NOT NULL CHECK (method IN ('bank','alipay','wechat','other')),
 external_ref text NOT NULL CHECK (char_length(external_ref) BETWEEN 1 AND 160),
 channel_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(channel_snapshot)='object'),
 status text NOT NULL DEFAULT 'paid' CHECK (status IN ('pending','paid','failed')),
 paid_at timestamptz,
 proof_hash text NOT NULL DEFAULT '' CHECK (char_length(proof_hash) <= 128),
 error text NOT NULL DEFAULT '' CHECK (char_length(error) <= 1000),
 actor_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(method,external_ref)
);

CREATE TABLE affiliate_settlement_runs (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 run_key text NOT NULL UNIQUE CHECK (char_length(run_key) BETWEEN 8 AND 160),
 kind text NOT NULL CHECK (kind IN ('commission_release','reconciliation')),
 window_start timestamptz NOT NULL,
 window_end timestamptz NOT NULL CHECK (window_end >= window_start),
 status text NOT NULL DEFAULT 'running' CHECK (status IN ('running','succeeded','failed')),
 lease_owner text NOT NULL CHECK (char_length(lease_owner) BETWEEN 1 AND 160),
 lease_until timestamptz NOT NULL,
 processed_count integer NOT NULL DEFAULT 0 CHECK (processed_count >= 0),
 error_count integer NOT NULL DEFAULT 0 CHECK (error_count >= 0),
 error text NOT NULL DEFAULT '' CHECK (char_length(error) <= 1000),
 started_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz
);
CREATE INDEX affiliate_settlement_runs_lease ON affiliate_settlement_runs(kind,status,lease_until);
ALTER TABLE affiliate_commission_events ADD CONSTRAINT affiliate_event_settlement_run
 FOREIGN KEY(settlement_run_id) REFERENCES affiliate_settlement_runs(id);
CREATE TABLE affiliate_settlement_errors (
 run_id bigint NOT NULL REFERENCES affiliate_settlement_runs(id),
 entry_id bigint NOT NULL REFERENCES affiliate_commission_entries(id),
 error text NOT NULL CHECK(char_length(error)<=1000),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(run_id,entry_id)
);

-- Status summarizes mixed buckets; monetary projections always sum buckets.
CREATE FUNCTION affiliate_derive_commission_status() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.amount_cents<>OLD.amount_cents OR NEW.account_id<>OLD.account_id OR NEW.invoice_id<>OLD.invoice_id THEN
  RAISE EXCEPTION 'commission source and earned amount are immutable';
 END IF;
 NEW.status=CASE
  WHEN NEW.reversal_at IS NOT NULL AND NEW.reserved_cents+NEW.paid_cents>0 THEN 'clawback'
  WHEN NEW.reversal_at IS NOT NULL THEN 'reversed'
  WHEN NEW.pending_cents>0 THEN 'pending'
  WHEN NEW.reserved_cents>0 THEN 'reserved'
  WHEN NEW.available_cents>0 THEN 'available'
  WHEN NEW.paid_cents>0 THEN 'paid'
  ELSE 'reversed' END;
 RETURN NEW;
END $$;
CREATE TRIGGER affiliate_derive_commission_status BEFORE UPDATE ON affiliate_commission_entries
 FOR EACH ROW EXECUTE FUNCTION affiliate_derive_commission_status();
CREATE INDEX affiliate_pending_release ON affiliate_commission_entries(id)
 WHERE pending_cents>0 AND reversal_at IS NULL;
CREATE TABLE affiliate_withdrawal_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 withdrawal_id bigint NOT NULL REFERENCES affiliate_withdrawals(id),
 actor_id bigint REFERENCES users(id),
 event_type text NOT NULL,
 before_status text,
 after_status text NOT NULL,
 note text NOT NULL DEFAULT '' CHECK(char_length(note)<=2000),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX affiliate_withdrawal_events_timeline ON affiliate_withdrawal_events(withdrawal_id,id);

ALTER TABLE affiliate_commission_events ADD CONSTRAINT affiliate_event_buckets_object
 CHECK(jsonb_typeof(bucket_snapshot)='object');
CREATE FUNCTION affiliate_snapshot_created_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.event_type='created' THEN
  SELECT jsonb_build_object('pending',pending_cents,'available',available_cents,
   'reserved',reserved_cents,'paid',paid_cents,'reversed',reversed_cents)
  INTO NEW.bucket_snapshot FROM affiliate_commission_entries WHERE id=NEW.entry_id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER affiliate_snapshot_created_event BEFORE INSERT ON affiliate_commission_events
 FOR EACH ROW EXECUTE FUNCTION affiliate_snapshot_created_event();
