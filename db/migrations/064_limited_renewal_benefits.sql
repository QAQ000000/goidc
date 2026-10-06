ALTER TABLE promo_codes ADD COLUMN recurring_limit integer NOT NULL DEFAULT 0
  CHECK (recurring_limit BETWEEN 0 AND 10000 AND (recurring OR recurring_limit=0));
ALTER TABLE order_items DROP CONSTRAINT order_item_renewal_promo;
ALTER TABLE order_items
  ADD COLUMN renewal_fixed_discount_cents bigint NOT NULL DEFAULT 0 CHECK (renewal_fixed_discount_cents>=0),
  ADD COLUMN renewal_discount_limit integer NOT NULL DEFAULT 0 CHECK (renewal_discount_limit BETWEEN 0 AND 10000),
  ADD CONSTRAINT order_item_renewal_promo CHECK (
    NOT (renewal_discount_percent>0 AND renewal_fixed_discount_cents>0) AND
    ((renewal_discount_percent>0 OR renewal_fixed_discount_cents>0)=(renewal_promo_code<>'')) AND
    (renewal_promo_code<>'' OR renewal_discount_limit=0));
ALTER TABLE services DROP CONSTRAINT service_renewal_promo;
ALTER TABLE services
  ADD COLUMN renewal_fixed_discount_cents bigint NOT NULL DEFAULT 0 CHECK (renewal_fixed_discount_cents>=0),
  ADD COLUMN renewal_discount_limit integer NOT NULL DEFAULT 0 CHECK (renewal_discount_limit BETWEEN 0 AND 10000),
  ADD COLUMN renewal_discounts_used bigint NOT NULL DEFAULT 0 CHECK (renewal_discounts_used>=0),
  ADD CONSTRAINT service_renewal_promo CHECK (
    NOT (renewal_discount_percent>0 AND renewal_fixed_discount_cents>0) AND
    ((renewal_discount_percent>0 OR renewal_fixed_discount_cents>0)=(renewal_promo_code<>'')) AND
    (renewal_promo_code<>'' OR (renewal_discount_limit=0 AND renewal_discounts_used=0)) AND
    (renewal_discount_limit=0 OR renewal_discounts_used<=renewal_discount_limit));
ALTER TABLE invoice_items DROP CONSTRAINT invoice_item_renewal_promo;
ALTER TABLE invoice_items
  ADD COLUMN renewal_fixed_discount_cents bigint NOT NULL DEFAULT 0 CHECK (renewal_fixed_discount_cents>=0),
  ADD COLUMN renewal_discount_limit integer NOT NULL DEFAULT 0 CHECK (renewal_discount_limit BETWEEN 0 AND 10000),
  ADD COLUMN renewal_discount_ordinal bigint NOT NULL DEFAULT 0 CHECK (renewal_discount_ordinal>=0),
  ADD CONSTRAINT invoice_item_renewal_promo CHECK (
    NOT (renewal_discount_percent>0 AND renewal_fixed_discount_cents>0) AND
    ((renewal_discount_percent>0 OR renewal_fixed_discount_cents>0)=(renewal_promo_code<>'')) AND
    (renewal_discount_limit=0 OR renewal_discount_ordinal BETWEEN 1 AND renewal_discount_limit) AND
    (renewal_promo_code<>'' OR (renewal_discount_limit=0 AND renewal_discount_ordinal=0)) AND
    CASE WHEN renewal_base_cents IS NULL THEN renewal_discount_cents=0 AND renewal_promo_code=''
    ELSE item_type='renew' AND renewal_base_cents>=0 AND
      renewal_discount_cents=CASE WHEN renewal_fixed_discount_cents>0 THEN LEAST(renewal_base_cents,renewal_fixed_discount_cents)
        ELSE (renewal_base_cents/100)*renewal_discount_percent+(renewal_base_cents%100)*renewal_discount_percent/100 END AND
      amount_cents=renewal_base_cents-renewal_discount_cents END);

-- Existing percentage benefits remain unlimited. Count their settled history,
-- including refunded renewals: refunds do not reissue promotional uses.
UPDATE services s SET renewal_discounts_used=(
  SELECT count(*) FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id
  WHERE ii.service_id=s.id AND ii.item_type='renew' AND ii.renewal_promo_code<>'' AND i.status IN ('paid','refunded'))
WHERE s.renewal_promo_code<>'';
UPDATE invoice_items ii SET renewal_discount_ordinal=s.renewal_discounts_used+1
FROM services s,invoices i WHERE ii.service_id=s.id AND i.id=ii.invoice_id
  AND ii.item_type='renew' AND ii.renewal_promo_code<>'' AND i.status='unpaid';

CREATE FUNCTION service_renewal_discount(base bigint,percent integer,fixed bigint,cap integer,used bigint)
RETURNS bigint LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
  SELECT CASE WHEN cap>0 AND used>=cap THEN 0
    WHEN fixed>0 THEN LEAST(base,fixed)
    ELSE (base/100)*percent+(base%100)*percent/100 END
$$;
