-- Authentication and MFA policies are persisted separately from presentation settings.
-- Existing site_settings fields remain the compatibility source for public clients.
CREATE TABLE auth_settings (
  id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1),
  login_enabled boolean NOT NULL DEFAULT true,
  registration_email_enabled boolean NOT NULL DEFAULT true,
  registration_phone_enabled boolean NOT NULL DEFAULT false,
  login_username_enabled boolean NOT NULL DEFAULT true,
  login_email_enabled boolean NOT NULL DEFAULT true,
  login_phone_enabled boolean NOT NULL DEFAULT true,
  phone_verification_required boolean NOT NULL DEFAULT false,
  verification_email_enabled boolean NOT NULL DEFAULT true,
  verification_sms_enabled boolean NOT NULL DEFAULT true,
  captcha_auto_enabled boolean NOT NULL DEFAULT false,
  login_max_attempts integer NOT NULL DEFAULT 3 CHECK (login_max_attempts BETWEEN 1 AND 20),
  login_lockout_minutes integer NOT NULL DEFAULT 180 CHECK (login_lockout_minutes BETWEEN 1 AND 1440),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO auth_settings(id) VALUES (1) ON CONFLICT DO NOTHING;

CREATE TABLE mfa_settings (
  id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1),
  customer_enabled boolean NOT NULL DEFAULT true,
  admin_enabled boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO mfa_settings(id) VALUES (1) ON CONFLICT DO NOTHING;
