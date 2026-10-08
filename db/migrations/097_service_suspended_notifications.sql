ALTER TABLE notification_delivery_events DROP CONSTRAINT notification_delivery_events_kind_check;
ALTER TABLE notification_delivery_events DROP CONSTRAINT notification_delivery_events_stage_check;
ALTER TABLE notification_delivery_events ADD CONSTRAINT notification_delivery_events_kind_check
CHECK (kind IN ('service_expiry_reminder','payment_success','service_provision_success','service_suspended'));
ALTER TABLE notification_delivery_events ADD CONSTRAINT notification_delivery_events_stage_check
CHECK ((kind='service_expiry_reminder' AND stage IN (1,7)) OR (kind IN ('payment_success','service_provision_success','service_suspended') AND stage=0));

INSERT INTO notification_templates(channel,kind,name,subject,body,provider_template_id)
VALUES
 ('email','service_suspended','服务暂停通知','服务已暂停','您的服务“{{product_name}}”已暂停。如需恢复服务，请完成续费或联系管理员。',''),
 ('sms','service_suspended','服务暂停通知','短信通知','您的服务{{product_name}}已暂停，请完成续费或联系管理员。','')
ON CONFLICT (channel,kind) DO NOTHING;
