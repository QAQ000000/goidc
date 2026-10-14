-- A settled invoice contributes to the turnover basis exactly once, including
-- invoices that predate the first effective rule set and invoices earning no
-- commission. Migration 134 built affiliate_turnover_entries with
-- rule_set_id NOT NULL, so the application could only record turnover for
-- invoices that matched a rule set; historical invoices were silently dropped
-- from every later tier threshold and under-paid the referral commission.
-- The rule set is optional evidence, not a precondition for counting turnover.
ALTER TABLE affiliate_turnover_entries ALTER COLUMN rule_set_id DROP NOT NULL;
