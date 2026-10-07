CREATE TABLE IF NOT EXISTS smtp_settings (
 id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1), configured boolean NOT NULL DEFAULT false, enabled boolean NOT NULL DEFAULT false,
 host text NOT NULL DEFAULT '', port integer NOT NULL DEFAULT 587 CHECK (port BETWEEN 1 AND 65535),
 username text NOT NULL DEFAULT '', password_encrypted bytea NOT NULL DEFAULT ''::bytea,
 from_address text NOT NULL DEFAULT '', version bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO smtp_settings(id) VALUES (1) ON CONFLICT (id) DO NOTHING;
