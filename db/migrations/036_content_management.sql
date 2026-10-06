ALTER TABLE content_categories
 ADD COLUMN description text NOT NULL DEFAULT '' CHECK (char_length(description)<=500),
 ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 ADD COLUMN created_at timestamptz NOT NULL DEFAULT now(),
 ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE content_articles
 ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 ADD COLUMN access_level text NOT NULL DEFAULT 'public' CHECK (access_level IN ('public','login','active_service'));
ALTER TABLE content_articles ADD CONSTRAINT news_public_access CHECK (content_type='knowledge' OR access_level='public');
-- Preserve editorial access while introducing a separately assignable read grant.
UPDATE admin_roles SET permissions=array_append(permissions,'content.view'),version=version+1,updated_at=now()
 WHERE 'content.manage'=ANY(permissions) AND NOT 'content.view'=ANY(permissions);
