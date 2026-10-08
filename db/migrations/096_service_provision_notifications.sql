ALTER TABLE notification_delivery_events DROP CONSTRAINT notification_delivery_events_kind_check;
ALTER TABLE notification_delivery_events DROP CONSTRAINT notification_delivery_events_stage_check;
ALTER TABLE notification_delivery_events ADD CONSTRAINT notification_delivery_events_kind_check
CHECK (kind IN ('service_expiry_reminder','payment_success','service_provision_success'));
ALTER TABLE notification_delivery_events ADD CONSTRAINT notification_delivery_events_stage_check
CHECK ((kind='service_expiry_reminder' AND stage IN (1,7)) OR (kind IN ('payment_success','service_provision_success') AND stage=0));

CREATE UNIQUE INDEX notification_delivery_events_provision
ON notification_delivery_events(service_id, channel, kind)
WHERE invoice_id IS NULL AND kind='service_provision_success';

INSERT INTO notification_templates(channel,kind,name,subject,body,provider_template_id)
VALUES
 ('email','service_provision_success','服务开通成功通知','服务已开通','您的服务“{{product_name}}”已开通，资源标识：{{upstream_id}}。',''),
 ('sms','service_provision_success','服务开通成功通知','短信通知','您的服务{{product_name}}已开通，资源标识：{{upstream_id}}。','')
ON CONFLICT (channel,kind) DO NOTHING;
