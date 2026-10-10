ALTER TABLE certification_settings
  ADD COLUMN IF NOT EXISTS advanced_enabled boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS auto_update_name boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS require_phone_match boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS notify_on_approval boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS suspend_unverified boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS allow_file_upload boolean NOT NULL DEFAULT true;
