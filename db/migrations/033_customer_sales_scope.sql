ALTER TABLE users ADD COLUMN customer_scope text NOT NULL DEFAULT 'owned' CHECK (customer_scope IN ('all','owned','assigned')),
 ADD COLUMN customer_ownerless boolean NOT NULL DEFAULT false,
 ADD COLUMN sales_enabled boolean NOT NULL DEFAULT false,
 ADD COLUMN sales_owner_id bigint REFERENCES users(id) ON DELETE RESTRICT,
 ADD COLUMN sales_version bigint NOT NULL DEFAULT 1,
 ADD CONSTRAINT users_sales_owner_customer CHECK (role='customer' OR sales_owner_id IS NULL),
 ADD CONSTRAINT users_sales_enabled_staff CHECK (NOT sales_enabled OR role='admin');
CREATE INDEX users_sales_owner_idx ON users(sales_owner_id) WHERE role='customer';

-- Preserve pre-migration operators; new staff receive an explicit customer scope.
UPDATE users SET customer_scope='all' WHERE role IN ('admin','super_admin');

CREATE FUNCTION admin_customer_owner_allowed(actor bigint, owner bigint) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM users a WHERE a.id=actor AND a.status='active' AND
  (a.role='super_admin' OR (a.role='admin' AND
   (a.customer_scope='all' OR (owner IS NULL AND a.customer_ownerless) OR
    (owner IS NOT NULL AND (a.customer_scope='assigned' OR owner=actor))))))
$$;

CREATE FUNCTION admin_customer_allowed(actor bigint, customer bigint) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM users c WHERE c.id=customer AND c.role='customer'
  AND admin_customer_owner_allowed(actor,c.sales_owner_id))
$$;
