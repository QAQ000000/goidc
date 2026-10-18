-- Expand business events without changing existing routes, consent or history.
ALTER TABLE notification_events ALTER COLUMN invoice_id DROP NOT NULL;
ALTER TABLE notification_events
 ADD COLUMN order_id bigint REFERENCES orders(id),
 ADD COLUMN service_id bigint REFERENCES services(id);
ALTER TABLE notification_events DROP CONSTRAINT notification_events_kind_check;
ALTER TABLE notification_events ADD CONSTRAINT notification_events_kind_check CHECK(kind IN (
 'invoice.paid','order.created','service.provisioned','service.suspended','service.resumed',
 'service.renewed','service.terminated','service.expiry_reminder','service.expired',
 'order.payment_reminder','invoice.overdue'));
ALTER TABLE notification_events ADD CONSTRAINT notification_events_resource_check CHECK(
 (kind IN ('invoice.paid','order.payment_reminder','invoice.overdue') AND invoice_id IS NOT NULL)
 OR (kind='order.created' AND order_id IS NOT NULL)
 OR (kind LIKE 'service.%' AND service_id IS NOT NULL));
ALTER TABLE notification_message_templates DROP CONSTRAINT notification_message_templates_event_kind_check;
ALTER TABLE notification_message_templates ADD CONSTRAINT notification_message_templates_event_kind_check CHECK(event_kind IN (
 'invoice.paid','order.created','service.provisioned','service.suspended','service.resumed',
 'service.renewed','service.terminated','service.expiry_reminder','service.expired',
 'order.payment_reminder','invoice.overdue'));
ALTER TABLE notification_routes DROP CONSTRAINT notification_routes_event_kind_check;
ALTER TABLE notification_routes ADD CONSTRAINT notification_routes_event_kind_check CHECK(event_kind IN (
 'invoice.paid','order.created','service.provisioned','service.suspended','service.resumed',
 'service.renewed','service.terminated','service.expiry_reminder','service.expired',
 'order.payment_reminder','invoice.overdue'));
-- Previous consent covered payment success only. Broad business notifications
-- require an explicit user update; an old client preserves the previous scope.
ALTER TABLE user_notification_preferences ADD COLUMN event_scope text NOT NULL DEFAULT 'invoice.paid'
 CHECK(event_scope IN ('invoice.paid','business'));

WITH events(kind,slug,subject,body) AS (VALUES
 ('order.created','order-created','下单成功','订单 {{order_no}} 已创建，金额 {{amount}}。'),
 ('service.provisioned','service-provisioned','服务开通','您的服务“{{product_name}}”（{{domain}}）已开通。'),
 ('service.suspended','service-suspended','服务暂停','您的服务“{{product_name}}”（{{domain}}）已暂停。'),
 ('service.resumed','service-resumed','服务恢复','您的服务“{{product_name}}”（{{domain}}）已恢复。'),
 ('service.renewed','service-renewed','服务续费','您的服务“{{product_name}}”（{{domain}}）已续费，到期日期 {{due_date}}。'),
 ('service.terminated','service-terminated','服务终止','您的服务“{{product_name}}”（{{domain}}）已终止。'),
 ('service.expiry_reminder','service-expiry-reminder','服务即将到期','您的服务“{{product_name}}”将在 {{days_left}} 天后到期（{{due_date}}），请及时续费。'),
 ('service.expired','service-expired','服务已到期','您的服务“{{product_name}}”已到期（{{due_date}}），请及时处理。'),
 ('order.payment_reminder','order-payment-reminder','待支付订单','订单 {{order_no}} 尚未支付，账单 {{invoice_no}} 金额 {{amount}}，请在 {{due_date}} 前完成支付。'),
 ('invoice.overdue','invoice-overdue','账单逾期','账单 {{invoice_no}} 已于 {{due_date}} 到期，应付金额 {{amount}}，请尽快完成支付。')
)
INSERT INTO notification_message_templates(id,event_kind,channel,subject,body)
 SELECT e.slug||'-'||c.channel,e.kind,c.channel,e.subject,e.body
 FROM events e CROSS JOIN (VALUES('telegram'),('inbox')) c(channel);
INSERT INTO notification_routes(id,event_kind,channel,template_id)
 SELECT id,event_kind,channel,id FROM notification_message_templates WHERE event_kind<>'invoice.paid';
