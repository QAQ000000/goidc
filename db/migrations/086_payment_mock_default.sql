ALTER TABLE payment_settings ALTER COLUMN mock_enabled SET DEFAULT false;
UPDATE payment_settings SET mock_enabled=false WHERE NOT configured;
