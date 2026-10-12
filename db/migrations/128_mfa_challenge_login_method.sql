ALTER TABLE mfa_challenges
  ADD COLUMN login_method text CHECK (login_method IN ('username', 'email', 'phone'));

-- Challenges created before the method was recorded cannot prove which policy applied.
DELETE FROM mfa_challenges WHERE login_method IS NULL;
