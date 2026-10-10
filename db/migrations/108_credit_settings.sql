CREATE TABLE credit_settings (id smallint PRIMARY KEY DEFAULT 1 CHECK(id=1), enabled boolean NOT NULL DEFAULT false, limit_cents bigint NOT NULL DEFAULT 0 CHECK(limit_cents>=0), auto_recharge boolean NOT NULL DEFAULT false, version bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now());
INSERT INTO credit_settings(id) VALUES(1) ON CONFLICT DO NOTHING;
