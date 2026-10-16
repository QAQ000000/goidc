-- Customer names and internal account identifiers are not login credentials.
-- Keep the legacy setting in API responses for older management clients.
ALTER TABLE auth_settings ALTER COLUMN login_username_enabled SET DEFAULT false;
UPDATE auth_settings
SET login_username_enabled=false,
    login_email_enabled=login_email_enabled OR NOT login_phone_enabled,
    version=version+1, updated_at=now()
WHERE login_username_enabled OR (NOT login_email_enabled AND NOT login_phone_enabled);
