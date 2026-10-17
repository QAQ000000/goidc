ALTER TABLE promo_codes ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version > 0);
-- Existing deployments may contain invalid windows from the legacy partial PATCH.
-- Preserve those records for correction while enforcing every new/updated window.
ALTER TABLE promo_codes ADD CONSTRAINT promo_codes_date_window CHECK (
  starts_at IS NULL OR expires_at IS NULL OR starts_at < expires_at
) NOT VALID;
