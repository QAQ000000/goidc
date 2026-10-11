ALTER TABLE orders
 ADD COLUMN created_by bigint REFERENCES users(id),
 ADD COLUMN payment_provider text NOT NULL DEFAULT '',
 ADD COLUMN payment_channel_instance_id text REFERENCES channel_instances(id),
 ADD COLUMN invoice_deferred boolean NOT NULL DEFAULT false,
 ADD COLUMN send_order_email boolean NOT NULL DEFAULT false,
 ADD COLUMN email_status text NOT NULL DEFAULT 'not_requested' CHECK(email_status IN ('not_requested','queued','skipped_unverified','skipped_channel','skipped_template'));
ALTER TABLE order_items
 ADD COLUMN first_price_override_cents bigint CHECK(first_price_override_cents BETWEEN 0 AND 100000000),
 ADD COLUMN renewal_price_override_cents bigint CHECK(renewal_price_override_cents BETWEEN 0 AND 100000000);
CREATE TABLE admin_order_creation_receipts (
 actor_id bigint NOT NULL REFERENCES users(id),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 8 AND 128),
 customer_id bigint NOT NULL REFERENCES users(id),
 order_id bigint NOT NULL REFERENCES orders(id),
 request_hash text NOT NULL,
 result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor_id,idempotency_key)
);
INSERT INTO notification_templates(channel,kind,name,subject,body)
VALUES('email','order_created','新建订单通知','订单已创建','您的订单 {{order_no}} 已创建，应付金额 {{amount}}。{{invoice_message}}')
ON CONFLICT(channel,kind) DO NOTHING;
