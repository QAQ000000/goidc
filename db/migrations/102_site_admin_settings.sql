ALTER TABLE site_settings
 ADD COLUMN admin_settings jsonb NOT NULL DEFAULT jsonb_build_object(
   'default_language','zh_CN',
   'language_menu_enabled',false,
   'records_per_page',20,
   'frontend_credit_enabled',false,
   'custom_customer_id_enabled',false,
   'admin_login_days',7,
   'frontend_ip_check',false,
   'admin_ip_check',false,
   'debug_enabled',false
 ) CHECK (jsonb_typeof(admin_settings)='object');
