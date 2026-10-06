ALTER TABLE site_settings
 ADD COLUMN maintenance_enabled boolean NOT NULL DEFAULT false,
 ADD COLUMN maintenance_message text NOT NULL DEFAULT '站点维护中，请稍后再试。' CHECK (length(maintenance_message)<=1000),
 ADD COLUMN maintenance_url text NOT NULL DEFAULT '' CHECK (length(maintenance_url)<=2048);
