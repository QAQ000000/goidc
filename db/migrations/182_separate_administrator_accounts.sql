-- Stop all pre-182 writers before applying. Account IDs remain globally unique;
-- principals contain no credentials, customer data or permission grants.
CREATE TABLE account_principals (
 id bigint PRIMARY KEY,
 kind text NOT NULL CHECK(kind IN ('customer','administrator')),
 UNIQUE(id,kind)
);
INSERT INTO account_principals(id,kind)
 SELECT id,CASE WHEN role='customer' THEN 'customer' ELSE 'administrator' END FROM users;

-- Copy the current shape, constraints and independent uniqueness indexes, but
-- deliberately share the original sequence instead of allocating a second ID space.
CREATE TABLE administrators (LIKE users INCLUDING DEFAULTS INCLUDING CONSTRAINTS INCLUDING INDEXES);
ALTER TABLE administrators DROP CONSTRAINT users_role_check;
ALTER TABLE administrators ALTER COLUMN role SET DEFAULT 'admin';
ALTER TABLE administrators ADD CONSTRAINT administrators_role CHECK(role IN ('admin','super_admin'));
DO $$ BEGIN
 EXECUTE format('ALTER TABLE administrators ALTER COLUMN id SET DEFAULT nextval(%L::regclass)',pg_get_serial_sequence('users','id'));
END $$;
INSERT INTO administrators SELECT * FROM users WHERE role IN ('admin','super_admin');

-- Preserve every pre-existing FK action and every historical reference. Shared
-- authors/recipients/actors and legacy administrator-owned financial facts use
-- the stable principal, never a fabricated customer account.
DO $$ DECLARE r record; BEGIN
 FOR r IN SELECT conrelid,conname,pg_get_constraintdef(oid) AS definition
 FROM pg_constraint WHERE contype='f' AND confrelid='users'::regclass
 LOOP
  EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I',r.conrelid::regclass,r.conname);
  EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I %s',r.conrelid::regclass,r.conname,
    regexp_replace(r.definition,'REFERENCES [^ ]*users[(]id[)]','REFERENCES account_principals(id)'));
 END LOOP;
END $$;
DELETE FROM users WHERE role IN ('admin','super_admin');
ALTER TABLE users DROP CONSTRAINT users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK(role='customer');

ALTER TABLE users ADD COLUMN principal_kind text GENERATED ALWAYS AS ('customer'::text) STORED;
ALTER TABLE administrators ADD COLUMN principal_kind text GENERATED ALWAYS AS ('administrator'::text) STORED;
ALTER TABLE users ADD CONSTRAINT users_principal FOREIGN KEY(id,principal_kind) REFERENCES account_principals(id,kind) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE administrators ADD CONSTRAINT administrators_principal FOREIGN KEY(id,principal_kind) REFERENCES account_principals(id,kind) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE administrators ADD CONSTRAINT administrators_admin_role_fk FOREIGN KEY(admin_role_id) REFERENCES admin_roles(id) ON DELETE RESTRICT;
ALTER TABLE administrators ADD CONSTRAINT administrators_commission_group_fk FOREIGN KEY(sales_commission_group_id) REFERENCES sales_commission_groups(id) ON DELETE SET NULL;
ALTER TABLE users ADD CONSTRAINT users_sales_administrator_fk FOREIGN KEY(sales_owner_id) REFERENCES administrators(id) ON DELETE RESTRICT;
ALTER TABLE admin_ticket_departments ADD CONSTRAINT admin_ticket_departments_administrator_fk FOREIGN KEY(user_id) REFERENCES administrators(id) ON DELETE CASCADE;

CREATE FUNCTION register_account_principal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  INSERT INTO account_principals(id,kind) VALUES(NEW.id,TG_ARGV[0]);
 ELSIF NEW.id IS DISTINCT FROM OLD.id THEN
  RAISE EXCEPTION 'account identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER account_principal AFTER INSERT ON users FOR EACH ROW EXECUTE FUNCTION register_account_principal('customer');
CREATE TRIGGER account_identity_guard BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION register_account_principal('customer');
CREATE TRIGGER account_principal AFTER INSERT ON administrators FOR EACH ROW EXECUTE FUNCTION register_account_principal('administrator');
CREATE TRIGGER account_identity_guard BEFORE UPDATE ON administrators FOR EACH ROW EXECUTE FUNCTION register_account_principal('administrator');
CREATE FUNCTION preserve_account_principal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'principal identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER account_principal_immutable BEFORE UPDATE ON account_principals FOR EACH ROW EXECUTE FUNCTION preserve_account_principal();
CREATE FUNCTION delete_account_principal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 DELETE FROM account_principals WHERE id=OLD.id;
 RETURN OLD;
END $$;
CREATE TRIGGER account_principal_delete AFTER DELETE ON users FOR EACH ROW EXECUTE FUNCTION delete_account_principal();
CREATE TRIGGER account_principal_delete AFTER DELETE ON administrators FOR EACH ROW EXECUTE FUNCTION delete_account_principal();
CREATE TRIGGER administrator_verification_invalidation BEFORE UPDATE ON administrators FOR EACH ROW EXECUTE FUNCTION invalidate_user_verification();

-- Read-only profile projection for mixed historical actors and notification
-- recipients. Authentication and security writes must select a concrete table.
CREATE VIEW account_identities AS
 SELECT id,username,email,display_name,role,status,balance_cents,created_at,updated_at,
 legacy_source,legacy_id,password_reset_required,mfa_enabled,mfa_confirmed_at,
 admin_role_id,admin_version,ticket_scope,ticket_unassigned,customer_scope,customer_ownerless,
 sales_enabled,sales_owner_id,sales_version,email_verified,phone,phone_verified,sales_commission_group_id
 FROM users
 UNION ALL
 SELECT id,username,email,display_name,role,status,balance_cents,created_at,updated_at,
 legacy_source,legacy_id,password_reset_required,mfa_enabled,mfa_confirmed_at,
 admin_role_id,admin_version,ticket_scope,ticket_unassigned,customer_scope,customer_ownerless,
 sales_enabled,sales_owner_id,sales_version,email_verified,phone,phone_verified,sales_commission_group_id
 FROM administrators;

CREATE OR REPLACE FUNCTION admin_customer_owner_allowed(actor bigint, owner bigint) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM administrators a WHERE a.id=actor AND a.status='active' AND
  (a.role='super_admin' OR (a.role='admin' AND
   (a.customer_scope='all' OR (owner IS NULL AND a.customer_ownerless) OR
    (owner IS NOT NULL AND (a.customer_scope='assigned' OR owner=actor))))))
$$;
CREATE OR REPLACE FUNCTION admin_customer_allowed(actor bigint, customer bigint) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM users c WHERE c.id=customer AND admin_customer_owner_allowed(actor,c.sales_owner_id))
$$;
CREATE OR REPLACE FUNCTION admin_ticket_department_allowed(actor bigint, department bigint) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM administrators u WHERE u.id=actor AND u.status='active' AND
  (u.role='super_admin' OR (u.role='admin' AND
   (u.ticket_scope='all' OR (department IS NULL AND u.ticket_unassigned) OR
    EXISTS(SELECT 1 FROM admin_ticket_departments a WHERE a.user_id=u.id AND a.department_id=department)))))
$$;

-- Keep historical owners and actors intact, including references that were
-- legal before the split. New or changed domain references must use the concrete
-- account type. Unchanged references may still be included in an UPDATE.
-- Authentication, audit, notification recipients and dual-portal actors retain
-- their principal FK because both account types legitimately use those features.
CREATE FUNCTION enforce_account_reference() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint; previous bigint; BEGIN
 target := (to_jsonb(NEW)->>TG_ARGV[0])::bigint;
 IF TG_OP='UPDATE' THEN
  previous := (to_jsonb(OLD)->>TG_ARGV[0])::bigint;
  IF target IS NOT DISTINCT FROM previous THEN RETURN NEW; END IF;
 END IF;
 IF target IS NOT NULL AND
    (CASE TG_ARGV[1]
     WHEN 'customer' THEN NOT EXISTS(SELECT 1 FROM users WHERE id=target)
     WHEN 'administrator' THEN NOT EXISTS(SELECT 1 FROM administrators WHERE id=target)
     ELSE true
    END) THEN
  RAISE EXCEPTION '% account required for %.%',TG_ARGV[1],TG_TABLE_NAME,TG_ARGV[0]
   USING ERRCODE='23503',TABLE=TG_TABLE_NAME,COLUMN=TG_ARGV[0],CONSTRAINT=TG_NAME;
 END IF;
 RETURN NEW;
END $$;
DO $$ DECLARE r record; BEGIN
 FOR r IN SELECT * FROM (VALUES
 -- Commercial ownership and customer-only profile/preferences.
 ('carts','user_id','customer'),('orders','user_id','customer'),
 ('invoices','user_id','customer'),('services','user_id','customer'),
 ('tickets','user_id','customer'),('customer_contacts','customer_id','customer'),
 ('customer_profiles','customer_id','customer'),('customer_group_members','customer_id','customer'),
 ('customer_files','customer_id','customer'),('balance_ledger','user_id','customer'),
 ('payment_transactions','user_id','customer'),('service_cancellation_requests','user_id','customer'),
 ('service_navigation_state','user_id','customer'),('service_navigation_preferences','user_id','customer'),
 ('service_configuration_changes','user_id','customer'),('traffic_orders','user_id','customer'),
 ('promo_code_redemptions','user_id','customer'),('promo_upgrade_redemptions','user_id','customer'),
 ('invoice_profiles','user_id','customer'),('invoice_addresses','user_id','customer'),
 ('invoice_applications','user_id','customer'),('contracts','customer_id','customer'),
 ('certification_profiles','user_id','customer'),('certification_submissions','user_id','customer'),
 ('affiliate_accounts','user_id','customer'),('affiliate_clicks','registered_user_id','customer'),
 ('affiliate_attributions','user_id','customer'),('affiliate_commission_entries','referred_user_id','customer'),
 ('marketing_campaign_recipients','user_id','customer'),('customer_credit_accounts','user_id','customer'),
 ('customer_credit_events','user_id','customer'),('credit_invoice_payments','user_id','customer'),
 ('credit_repayments','user_id','customer'),('balance_topups','user_id','customer'),
 ('balance_topup_settlements','user_id','customer'),('balance_topup_liability_resolutions','user_id','customer'),
 ('external_refunds','user_id','customer'),('external_refund_reversal_reservations','user_id','customer'),
 ('external_refund_order_holds','user_id','customer'),('external_refund_renewal_holds','user_id','customer'),
 ('service_renewal_effects','user_id','customer'),('payment_close_operations','user_id','customer'),
 ('payment_close_anomalies','user_id','customer'),('payment_close_verification_requests','user_id','customer'),
 ('payment_close_anomaly_refunds','user_id','customer'),('auto_renewal_invoice_sources','user_id','customer'),
 ('admin_ticket_creation_receipts','customer_id','customer'),('sales_settlement_items','customer_id','customer'),
 ('ticket_ratings','customer_id','customer'),('mutation_operations','customer_id','customer'),
 -- Staff assignment, authoring, financial review and administration.
 ('users','sales_owner_id','administrator'),('admin_ticket_departments','user_id','administrator'),
 ('tickets','assignee_id','administrator'),('tickets','created_by','administrator'),
 ('orders','created_by','administrator'),('orders','deleted_by','administrator'),
 ('ticket_notes','author_id','administrator'),('ticket_transfer_logs','actor_id','administrator'),
 ('ticket_transfer_logs','previous_assignee_id','administrator'),('ticket_transfer_logs','assignee_id','administrator'),
 ('ticket_ratings','staff_id','administrator'),('service_cancellation_requests','resolved_by','administrator'),
 ('content_articles','author_id','administrator'),('content_images','uploader_id','administrator'),
 ('downloads','author_id','administrator'),('customer_files','uploader_id','administrator'),
 ('promo_codes','created_by','administrator'),('order_settings_receipts','actor_id','administrator'),
 ('notification_templates','updated_by','administrator'),('legacy_review_decisions','actor_id','administrator'),
 ('invoice_applications','reviewer_id','administrator'),('invoice_application_adjustments','actor_id','administrator'),
 ('invoice_application_documents','uploader_id','administrator'),
 ('certification_submissions','reviewed_by','administrator'),('certification_reviews','reviewer_id','administrator'),
 ('affiliate_rule_sets','created_by','administrator'),('affiliate_withdrawal_reviews','reviewer_id','administrator'),
 ('affiliate_payouts','actor_id','administrator'),('marketing_campaigns','created_by','administrator'),
 ('admin_ticket_creation_receipts','actor_id','administrator'),('customer_credit_events','actor_id','administrator'),
 ('credit_repayments','actor_id','administrator'),('credit_invoice_payments','refund_actor_id','administrator'),
 ('sales_settlements','actor_id','administrator'),('sales_settlement_items','sales_owner_id','administrator'),
 ('external_refunds','requested_by','administrator'),('external_refunds','reviewed_by','administrator'),
 ('balance_topup_liability_resolutions','actor_id','administrator'),('payment_close_operations','actor_id','administrator'),
 ('payment_close_verification_requests','actor_id','administrator'),('payment_close_verification_decisions','actor_id','administrator'),
 ('payment_close_anomaly_refunds','requested_by','administrator'),('payment_close_anomaly_refunds','reviewed_by','administrator'),
 ('report_expenses','recorded_by','administrator'),('report_expense_voids','recorded_by','administrator')
 ) AS x(table_name,column_name,account_kind)
 LOOP
  -- Prefix before association/immutability triggers so type violations have a
  -- stable 23503 diagnostic instead of an unrelated missing-field failure.
  EXECUTE format('CREATE TRIGGER %I BEFORE INSERT OR UPDATE OF %I ON %I FOR EACH ROW EXECUTE FUNCTION enforce_account_reference(%L,%L)',
   'account_reference_'||r.column_name,r.column_name,r.table_name,r.column_name,r.account_kind);
 END LOOP;
END $$;
