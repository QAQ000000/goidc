-- New purchases capture their renewal benefit; historical purchases are not
-- inferred from mutable promotion settings.
ALTER TABLE order_items
  ADD COLUMN renewal_discount_percent integer NOT NULL DEFAULT 0 CHECK (renewal_discount_percent BETWEEN 0 AND 100),
  ADD COLUMN renewal_promo_code text NOT NULL DEFAULT '',
  ADD CONSTRAINT order_item_renewal_promo CHECK ((renewal_discount_percent=0)=(renewal_promo_code=''));
ALTER TABLE services
  ADD COLUMN renewal_discount_percent integer NOT NULL DEFAULT 0 CHECK (renewal_discount_percent BETWEEN 0 AND 100),
  ADD COLUMN renewal_promo_code text NOT NULL DEFAULT '',
  ADD CONSTRAINT service_renewal_promo CHECK ((renewal_discount_percent=0)=(renewal_promo_code=''));
ALTER TABLE invoice_items
  ADD COLUMN renewal_base_cents bigint,
  ADD COLUMN renewal_discount_cents bigint NOT NULL DEFAULT 0 CHECK (renewal_discount_cents>=0),
  ADD COLUMN renewal_discount_percent integer NOT NULL DEFAULT 0 CHECK (renewal_discount_percent BETWEEN 0 AND 100),
  ADD COLUMN renewal_promo_code text NOT NULL DEFAULT '',
  ADD CONSTRAINT invoice_item_renewal_promo CHECK (
    (renewal_discount_percent=0)=(renewal_promo_code='') AND
    CASE WHEN renewal_base_cents IS NULL THEN renewal_discount_cents=0 AND renewal_discount_percent=0
    ELSE item_type='renew' AND renewal_base_cents>=0 AND
      renewal_discount_cents=(renewal_base_cents/100)*renewal_discount_percent+(renewal_base_cents%100)*renewal_discount_percent/100 AND
      amount_cents=renewal_base_cents-renewal_discount_cents END
  );
