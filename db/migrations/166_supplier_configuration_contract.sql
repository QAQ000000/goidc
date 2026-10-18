-- Keep supplier accounts as the durable delivery/serialization identity.  Old
-- ciphertext is deliberately not re-encrypted by a SQL migration.
ALTER TABLE supplier_accounts
 ADD COLUMN config jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config)='object'),
 ADD COLUMN schema_version integer NOT NULL DEFAULT 1 CHECK (schema_version>0),
 ADD COLUMN credential_format integer NOT NULL DEFAULT 0 CHECK (credential_format IN (0,1)),
 ADD COLUMN secure_fields jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(secure_fields)='object'),
 ADD COLUMN identity_key text NOT NULL DEFAULT '';

UPDATE supplier_accounts SET config=jsonb_build_object('endpoint',base_url,'username',username)
 WHERE provider='zjmf_finance_v1';

-- New implementations need not have an endpoint/username pair.  Their
-- implementation-owned, credential-independent identity replaces this tuple.
-- Legacy identities are checked against normalized config under the catalogue
-- lock when an account is created; no secret is needed for that comparison.
ALTER TABLE supplier_accounts DROP CONSTRAINT supplier_accounts_provider_base_url_username_key;
CREATE UNIQUE INDEX supplier_accounts_identity ON supplier_accounts(provider,identity_key)
 WHERE identity_key<>'';

ALTER TABLE supplier_accounts ADD CONSTRAINT supplier_accounts_legacy_fields_check
 CHECK (credential_format<>0 OR secure_fields='{}'::jsonb);

CREATE FUNCTION preserve_supplier_account_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.provider IS DISTINCT FROM OLD.provider OR
    NEW.base_url IS DISTINCT FROM OLD.base_url OR
    NEW.username IS DISTINCT FROM OLD.username OR
    NEW.config IS DISTINCT FROM OLD.config OR
    NEW.schema_version IS DISTINCT FROM OLD.schema_version OR
    (OLD.identity_key<>'' AND NEW.identity_key IS DISTINCT FROM OLD.identity_key) THEN
  RAISE EXCEPTION 'supplier account configuration identity is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER supplier_account_identity_immutable BEFORE UPDATE ON supplier_accounts
 FOR EACH ROW EXECUTE FUNCTION preserve_supplier_account_identity();
