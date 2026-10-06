-- Upgrade-only promotion codes are evaluated against a positive product
-- upgrade delta. They never affect cart purchases or recurring renewals.
ALTER TABLE promo_codes
  ADD COLUMN upgrade_only boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT promo_upgrade_only_not_recurring CHECK (NOT (upgrade_only AND recurring));

ALTER TABLE service_configuration_changes
  ADD COLUMN upgrade_base_cents bigint NOT NULL DEFAULT 0 CHECK (upgrade_base_cents>=0),
  ADD COLUMN upgrade_discount_cents bigint NOT NULL DEFAULT 0 CHECK (upgrade_discount_cents>=0),
  ADD COLUMN upgrade_promo_code text NOT NULL DEFAULT '',
  ADD CONSTRAINT service_change_upgrade_promo CHECK (
    (upgrade_promo_code='' AND upgrade_base_cents=0 AND upgrade_discount_cents=0)
    OR (change_type='product' AND upgrade_promo_code<>'' AND upgrade_base_cents>=upgrade_discount_cents
        AND amount_cents=upgrade_base_cents-upgrade_discount_cents));

ALTER TABLE invoice_items
  ADD COLUMN upgrade_base_cents bigint,
  ADD COLUMN upgrade_discount_cents bigint NOT NULL DEFAULT 0 CHECK (upgrade_discount_cents>=0),
  ADD COLUMN upgrade_promo_code text NOT NULL DEFAULT '',
  ADD CONSTRAINT invoice_item_upgrade_promo CHECK (
    (upgrade_promo_code='' AND upgrade_base_cents IS NULL AND upgrade_discount_cents=0)
    OR (item_type='upgrade' AND upgrade_promo_code<>'' AND upgrade_base_cents IS NOT NULL
        AND upgrade_base_cents>=upgrade_discount_cents AND amount_cents=upgrade_base_cents-upgrade_discount_cents));

CREATE TABLE promo_upgrade_redemptions (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  promo_code_id bigint NOT NULL REFERENCES promo_codes(id) ON DELETE RESTRICT,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  change_id bigint NOT NULL UNIQUE REFERENCES service_configuration_changes(id) ON DELETE RESTRICT,
  discount_cents bigint NOT NULL CHECK (discount_cents>=0),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(promo_code_id,user_id,change_id)
);
CREATE INDEX promo_upgrade_redemptions_user ON promo_upgrade_redemptions(user_id,promo_code_id,id);
