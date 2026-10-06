ALTER TABLE product_purchase_fields
  ADD COLUMN field_scope text NOT NULL DEFAULT 'purchase' CHECK (field_scope IN ('purchase','admin','customer')),
  ADD COLUMN encrypted boolean NOT NULL DEFAULT false,
  ADD COLUMN customer_profile_key text NOT NULL DEFAULT '' CHECK (char_length(customer_profile_key) <= 80);

ALTER TABLE product_purchase_fields
  ADD CONSTRAINT product_purchase_fields_sensitive_rules CHECK (
    (field_scope = 'purchase' AND customer_profile_key = '')
    OR (field_scope = 'admin' AND NOT required AND NOT show_invoice AND customer_profile_key = '')
    OR (field_scope = 'customer' AND NOT show_invoice AND customer_profile_key <> '')
  );
