ALTER TABLE smtp_settings ADD COLUMN tls_mode text NOT NULL DEFAULT 'starttls' CHECK (tls_mode IN ('implicit_tls','starttls','none'));
ALTER TABLE smtp_settings ADD COLUMN from_name text NOT NULL DEFAULT '';
UPDATE smtp_settings SET tls_mode='implicit_tls' WHERE port=465;
