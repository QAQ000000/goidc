CREATE TABLE admin_roles (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 80),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
  active boolean NOT NULL DEFAULT true,
  permissions text[] NOT NULL DEFAULT '{}',
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE users ADD COLUMN admin_role_id bigint REFERENCES admin_roles(id) ON DELETE RESTRICT,
  ADD COLUMN admin_version bigint NOT NULL DEFAULT 1;

-- Preserve existing operator access through an explicit, reviewable role.
INSERT INTO admin_roles(name,description,permissions)
SELECT '现有运营管理员','迁移前已有普通管理员的业务权限，请按岗位复核。', ARRAY[
 'dashboard.view','customers.view','customers.manage','customer_groups.view','customer_groups.manage',
 'promos.view','promos.manage','products.view','products.manage','content.manage',
 'orders.view','orders.manage','invoices.view','invoices.refund','balance.manage',
 'services.view','services.manage','tickets.view','tickets.manage','ticket_presets.view','ticket_presets.manage',
 'notifications.view','notifications.manage','reports.view','audit.view','migrations.view']
WHERE EXISTS(SELECT 1 FROM users WHERE role='admin');
UPDATE users SET admin_role_id=(SELECT id FROM admin_roles WHERE name='现有运营管理员') WHERE role='admin';
ALTER TABLE users ADD CONSTRAINT users_admin_role CHECK (
 (role='admin' AND admin_role_id IS NOT NULL) OR (role<>'admin' AND admin_role_id IS NULL));
CREATE INDEX users_admin_role ON users(admin_role_id) WHERE admin_role_id IS NOT NULL;
