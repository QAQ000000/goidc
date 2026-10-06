ALTER TABLE users ADD COLUMN ticket_scope text NOT NULL DEFAULT 'departments' CHECK (ticket_scope IN ('all','departments')),
 ADD COLUMN ticket_unassigned boolean NOT NULL DEFAULT false;
ALTER TABLE ticket_departments ADD COLUMN version bigint NOT NULL DEFAULT 1;
CREATE TABLE admin_ticket_departments (
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 department_id bigint NOT NULL REFERENCES ticket_departments(id) ON DELETE RESTRICT,
 PRIMARY KEY(user_id,department_id)
);

-- Existing staff retain their explicit pre-migration scope; future staff start with no departments.
UPDATE users SET ticket_scope='all' WHERE role IN ('admin','super_admin');

CREATE FUNCTION admin_ticket_department_allowed(actor bigint, department bigint) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=actor AND u.status='active' AND
  (u.role='super_admin' OR (u.role='admin' AND
   (u.ticket_scope='all' OR (department IS NULL AND u.ticket_unassigned) OR
    EXISTS(SELECT 1 FROM admin_ticket_departments a WHERE a.user_id=u.id AND a.department_id=department)))))
$$;
