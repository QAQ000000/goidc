CREATE TABLE channel_instances (
 id text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9_-]{2,63}$'),
 category text NOT NULL CHECK (category IN ('payment','oauth','sms','mail','certification','provision')),
 implementation_id text NOT NULL CHECK (implementation_id ~ '^[a-z][a-z0-9_-]{1,63}$'),
 display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 160),
 enabled boolean NOT NULL DEFAULT false,
 sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN -100000 AND 100000),
 config jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config)='object'),
 secret_ciphertext bytea NOT NULL DEFAULT ''::bytea,
 secret_aad text NOT NULL DEFAULT '',
 version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(category, implementation_id, display_name)
);
CREATE INDEX channel_instances_category ON channel_instances(category, sort_order, id);

ALTER TABLE payment_transactions ADD COLUMN channel_instance_id text REFERENCES channel_instances(id) ON DELETE SET NULL;
ALTER TABLE email_outbox ADD COLUMN channel_instance_id text REFERENCES channel_instances(id) ON DELETE SET NULL;
ALTER TABLE sms_outbox ADD COLUMN channel_instance_id text REFERENCES channel_instances(id) ON DELETE SET NULL;

INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,sort_order,config,secret_ciphertext,secret_aad)
SELECT 'payment-default','payment','epay','默认易支付',enabled,0,
       jsonb_build_object('api_url',api_url,'pid',pid,'types',to_jsonb(types)),key_ciphertext,
       'zjmfgo:payment-settings:epay-key:v1' FROM payment_settings
ON CONFLICT (id) DO NOTHING;
INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,sort_order,config)
SELECT 'payment-mock-default','payment','mock','模拟支付',mock_enabled,10,
       jsonb_build_object('secret_source','deployment') FROM payment_settings
ON CONFLICT (id) DO NOTHING;
INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,sort_order,config,secret_ciphertext,secret_aad)
SELECT 'mail-smtp-default','mail','smtp','默认 SMTP',enabled,0,
       jsonb_build_object('host',host,'port',port,'username',username,'from',from_address,'tls_mode',tls_mode,'from_name',from_name),
       password_encrypted,'zjmfgo:smtp-settings:password:v1' FROM smtp_settings
ON CONFLICT (id) DO NOTHING;
INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,sort_order,config,secret_ciphertext,secret_aad)
SELECT 'sms-default','sms',COALESCE(NULLIF(provider,''),'generic_http_json'),'默认短信',enabled,0,
       jsonb_build_object('endpoint',endpoint),token_encrypted,'sms:token' FROM sms_settings
ON CONFLICT (id) DO NOTHING;

UPDATE payment_transactions SET channel_instance_id='payment-default' WHERE channel_instance_id IS NULL AND provider LIKE 'epay_%';
UPDATE email_outbox SET channel_instance_id='mail-smtp-default' WHERE channel_instance_id IS NULL;
UPDATE sms_outbox SET channel_instance_id='sms-default' WHERE channel_instance_id IS NULL;
