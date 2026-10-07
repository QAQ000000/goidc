CREATE TABLE payment_settings (
 id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1),
 configured boolean NOT NULL DEFAULT false,
 enabled boolean NOT NULL DEFAULT true,
 mock_enabled boolean NOT NULL DEFAULT true,
 api_url text NOT NULL DEFAULT '',
 pid text NOT NULL DEFAULT '',
 types text[] NOT NULL DEFAULT '{}',
 key_ciphertext bytea NOT NULL DEFAULT '\x',
 version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO payment_settings(id) VALUES(1);
