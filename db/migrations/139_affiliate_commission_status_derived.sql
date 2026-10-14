-- affiliate_commission_entries.status is a projection of the bucket columns,
-- not an independent business state. The BEFORE UPDATE trigger from migration
-- 100 rewrites it on every bucket change; this constraint makes the invariant a
-- hard database guarantee for inserts and for updates that ever bypass the
-- trigger, so a stale copy can no longer be returned to customers.
-- The repair statement spells the derivation out instead of relying on the
-- trigger, so the migration also succeeds on a schema where the trigger was
-- dropped. It only rewrites status; bucket amounts and earned amounts stay put.
UPDATE affiliate_commission_entries
SET status = CASE
 WHEN reversal_at IS NOT NULL AND reserved_cents+paid_cents>0 THEN 'clawback'
 WHEN reversal_at IS NOT NULL THEN 'reversed'
 WHEN pending_cents>0 THEN 'pending'
 WHEN reserved_cents>0 THEN 'reserved'
 WHEN available_cents>0 THEN 'available'
 WHEN paid_cents>0 THEN 'paid'
 ELSE 'reversed' END
WHERE status IS DISTINCT FROM CASE
 WHEN reversal_at IS NOT NULL AND reserved_cents+paid_cents>0 THEN 'clawback'
 WHEN reversal_at IS NOT NULL THEN 'reversed'
 WHEN pending_cents>0 THEN 'pending'
 WHEN reserved_cents>0 THEN 'reserved'
 WHEN available_cents>0 THEN 'available'
 WHEN paid_cents>0 THEN 'paid'
 ELSE 'reversed' END;
DO $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='affiliate_commission_status_derived' AND conrelid='affiliate_commission_entries'::regclass) THEN
  ALTER TABLE affiliate_commission_entries ADD CONSTRAINT affiliate_commission_status_derived CHECK (status = CASE
   WHEN reversal_at IS NOT NULL AND reserved_cents+paid_cents>0 THEN 'clawback'
   WHEN reversal_at IS NOT NULL THEN 'reversed'
   WHEN pending_cents>0 THEN 'pending'
   WHEN reserved_cents>0 THEN 'reserved'
   WHEN available_cents>0 THEN 'available'
   WHEN paid_cents>0 THEN 'paid'
   ELSE 'reversed' END);
 END IF;
END $$;
