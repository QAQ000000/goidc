CREATE TABLE service_navigation_groups (
 id bigserial PRIMARY KEY,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
 icon text NOT NULL DEFAULT 'server' CHECK (icon IN ('server','cloud','database','globe','box')),
 sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order BETWEEN 0 AND 1000000),
 version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE products ADD COLUMN service_group_id bigint REFERENCES service_navigation_groups(id);
CREATE INDEX products_service_group ON products(service_group_id);
-- A marker records initialization even when there are no groups. Groups added
-- after initialization stay hidden until explicitly shown or newly purchased.
CREATE TABLE service_navigation_state (
 user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 initialized_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE service_navigation_preferences (
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 group_id bigint NOT NULL REFERENCES service_navigation_groups(id) ON DELETE CASCADE,
 visible boolean NOT NULL,
 version bigint NOT NULL DEFAULT 1,
 PRIMARY KEY(user_id,group_id)
);
