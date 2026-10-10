CREATE TABLE customer_profiles (
 customer_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 gender text NOT NULL DEFAULT 'unknown' CHECK (gender IN ('unknown','male','female')),
 company_name text NOT NULL DEFAULT '' CHECK (char_length(company_name)<=200),
 phone text NOT NULL DEFAULT '' CHECK (char_length(phone)<=80),
 qq text NOT NULL DEFAULT '' CHECK (char_length(qq)<=40),
 country text NOT NULL DEFAULT '' CHECK (char_length(country)<=100),
 province text NOT NULL DEFAULT '' CHECK (char_length(province)<=100),
 address text NOT NULL DEFAULT '' CHECK (char_length(address)<=500),
 postcode text NOT NULL DEFAULT '' CHECK (char_length(postcode)<=40),
 source_channel text NOT NULL DEFAULT '' CHECK (char_length(source_channel)<=200),
 payment_method text NOT NULL DEFAULT 'bank' CHECK (payment_method IN ('bank','alipay','wechat')),
 language text NOT NULL DEFAULT 'zh_CN' CHECK (language IN ('zh_CN','en_US')),
 admin_note text NOT NULL DEFAULT '' CHECK (char_length(admin_note)<=4000),
 receive_marketing boolean NOT NULL DEFAULT true,
 updated_at timestamptz NOT NULL DEFAULT now()
);
