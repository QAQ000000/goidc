# 业务契约

- 商品与购物：[目录](../catalog-management.md)、[商品配置](../product-configuration.md)、[购买资料](../product-purchase-fields.md)、[客户分组定价](customer-group-pricing.md)。
- 订单与资金：[交易](../transactions.md)、[支付](../payments.md)、[支付操作](../payment-operations-runbook.md)、[发票](../invoice-applications.md)、[报表口径](../credit-cash-reporting.md)。
- 经营报表：[恢复契约](../report-restoration-contract.md)、[商品/人员排名API](../admin/api.md#商品与人员业务收款排名)，新增排名UI仍待接；API/页面、独立PG回归与模拟API浏览器分别有证据，真实Go API浏览器全链路、旧全量迁移和外部凭证未验收；明确预计/历史确认、消息原身份评分、真实现金/费用及分类件数边界。
- 服务生命周期：[管理员服务管理](../admin/service-management-api.md)、[配置变更](../service-configuration-changes.md)、[商品变更](../service-product-changes.md)、[续费优惠](../upgrade-promotions.md)、[服务下载](../service-downloads.md)、[独立人工库存与交付绑定](manual-resources.md)。
- 外部集成：[供应商协议](../supplier-protocol.md)、[NOKVM 交付](../nokvm-delivery.md)、[返佣](../affiliate-core.md)、[统一通知](../notification-architecture.md)、[工单通知](../ticket-notification-contract.md)。
- 通知渠道扩展：[机器人渠道开发指南](../architecture/bot-channel-development.md)，用于新增平台适配器、目标授权、路由模板及投递验收。
- 实名认证：[提供方实例与任务契约](certification.md)，定义实例选择、申请快照、提交/查询恢复、人工审核与下单/API 密钥资格。
- 已退役业务：[信用额度](../customer-credit-portal-contract.md)、[合同及设置](contract-settings.md)。全部旧API鉴权后410、页面404，不恢复独立历史入口；结构保留不代表功能可用。旧批次原文只作历史背景。
- 数据与安全：[敏感购买字段](../sensitive-purchase-fields.md)、[认证验证码](../auth-captcha.md)、[原生配置规则](../native-configuration-rules.md)。

业务契约使用稳定名称；当前差距和验收范围以 [当前差距审计](../replacement-gap-audit.md) 及对应快照为准。
