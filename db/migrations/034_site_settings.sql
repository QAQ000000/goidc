CREATE TABLE site_settings (
 id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1),
 site_name text NOT NULL DEFAULT '智简魔方' CHECK (length(site_name) BETWEEN 1 AND 80),
 description text NOT NULL DEFAULT '云服务、订单与财务管理' CHECK (length(description)<=300),
 company_name text NOT NULL DEFAULT '' CHECK (length(company_name)<=160),
 logo_url text NOT NULL DEFAULT '/assets/brand-logo.png',
 support_email text NOT NULL DEFAULT '',
 support_phone text NOT NULL DEFAULT '',
 address text NOT NULL DEFAULT '',
 support_url text NOT NULL DEFAULT '',
 terms_url text NOT NULL DEFAULT '',
 privacy_url text NOT NULL DEFAULT '',
 record_number text NOT NULL DEFAULT '',
 copyright text NOT NULL DEFAULT 'ZJMF',
 registration_enabled boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO site_settings(id) VALUES(1);
