-- Unfinished challenges cannot prove their login audience. Existing sessions
-- retain their identity and are governed by the current customer/admin guards.
ALTER TABLE mfa_challenges ADD COLUMN login_portal text;
DELETE FROM mfa_challenges;
ALTER TABLE mfa_challenges
  ALTER COLUMN login_portal SET NOT NULL,
  ADD CONSTRAINT mfa_challenges_login_portal_check
    CHECK (login_portal IN ('customer', 'admin'));
