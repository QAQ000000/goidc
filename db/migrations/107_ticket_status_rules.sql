CREATE TABLE ticket_statuses (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z][a-z0-9_-]{0,63}$'),
 title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
 sort_order int NOT NULL DEFAULT 0,
 builtin boolean NOT NULL DEFAULT false,
 active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO ticket_statuses(slug,title,sort_order,builtin) VALUES
 ('open','开启中',1,true),('pending','等待中',2,true),('resolved','已回复',3,true),('closed','已关闭',4,true),('customer_reply','客户回复',5,true)
ON CONFLICT(slug) DO NOTHING;
CREATE TABLE ticket_delivery_rules (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 department_id bigint REFERENCES ticket_departments(id) ON DELETE RESTRICT,
 product_id bigint REFERENCES products(id) ON DELETE RESTRICT,
 blocked_keywords text[] NOT NULL DEFAULT '{}',
 auto_reply text NOT NULL DEFAULT '', active boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ticket_delivery_rules_match ON ticket_delivery_rules(department_id,product_id,active,id);
ALTER TABLE ticket_statuses ADD COLUMN idempotency_key text NOT NULL DEFAULT '';
ALTER TABLE ticket_delivery_rules ADD COLUMN idempotency_key text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX ticket_statuses_idempotency ON ticket_statuses(idempotency_key) WHERE idempotency_key<>'';
CREATE UNIQUE INDEX ticket_delivery_rules_idempotency ON ticket_delivery_rules(idempotency_key) WHERE idempotency_key<>'';
