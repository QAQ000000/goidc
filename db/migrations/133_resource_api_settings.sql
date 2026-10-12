-- Global resource API controls from the legacy admin API settings page.
-- The table only stores policy switches. API credentials remain per account
-- in api_keys and supplier credentials remain in supplier_accounts.
CREATE TABLE resource_api_settings (
 id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1),
 -- Existing Go API-key clients remain usable until an administrator disables
 -- customer API access explicitly.
 enabled boolean NOT NULL DEFAULT true,
 require_certification boolean NOT NULL DEFAULT false,
 require_verified_phone boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO resource_api_settings(id) VALUES (1) ON CONFLICT DO NOTHING;
