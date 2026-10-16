# 后台 105 页深度还原台账

来源：`/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md`。

状态定义：`已完成` 表示已有真实页面和 API，且没有已登记的旧版关键能力缺口；`部分完成` 表示页面或交互仍缺一部分；`待实现` 表示没有对应页面/API；`后端阻塞` 表示 UI 可排版但业务契约尚未具备。登记状态不是全量浏览器或生产验收结论。

2026-10-16 当前登记：105 项中 **44 项已完成、61 项部分完成、0 项待实现、0 项后端阻塞**。本次按已知能力缺口将 36/38/103 三项降为部分完成；下方各批次数字保留为当时的历史快照。历史浏览器 PASS 的范围仍以原检查项为准；截图摘要复核发现 143 项匹配、6 项失配需重验、8 项摘要未建立，详见[本次证据范围验收](restore-evidence-scope-acceptance-20261016.md)。

所有新版路径保持 `/admin/*` 稳定路由。列表页统一使用 `items/total/limit/offset`，设置页统一使用 `version/expected_version`，写操作必须经过权限、审计、幂等和版本冲突校验。

| # | 旧版页面 / hash | 新版路由 | 状态 | 主要 API | 权限 / 验收 |
|---:|---|---|---|---|---|
| 1 | 控制台概览 `#/login` | `/admin` | 已完成 | `GET /admin/dashboard` | `dashboard.view` / 四尺寸工作台 |
| 2 | 客户列表 `#/customer-list` | `/admin/customers` | 已完成 | `GET /admin/customers/page` | `customers.view` / 搜索分页返回 |
| 3 | 实名认证 `#/customer-authentication` | `/admin/certifications`、`/console/certification` | 已完成 | `GET /admin/certifications/page`、`GET /admin/certifications/{id}`、`GET /admin/certifications/{id}/history`、`POST /admin/certifications/{id}/review`、`POST /account/certification/convert-company` | `certifications.view/manage` / 状态、类型、接口筛选；审批历史；个人转企业事务、幂等和版本冲突 |
| 4 | 客户资源池 `#/customer-resources` | `/admin/customer-resources` | 已完成 | `GET /admin/customers/page` + `PUT /admin/customers/{id}/sales-owner` | `@super` / 真实资源池、版本冲突、审计 |
| 5 | 我的业绩 `#/sales-statistics` | `/admin/reports?tab=sales` | 已完成 | `GET /admin/reports/sales?year=` | `reports.view` / 年份、URL 标签恢复、真实空态；四尺寸已验收 |
| 6 | 营销推送 `#/marketing-push` | `/admin/marketing` | 部分完成 | `GET/POST/PATCH /admin/marketing`, `POST /admin/marketing/audience-count`, `POST /admin/marketing/{id}/send` | `notifications.view/manage` / 真实客户与商品受众、邮件短信站内信 outbox；四尺寸验收已通过 |
| 7 | 推介计划 `#/customer-promotionplan` | `/admin/affiliate` | 部分完成 | `GET /admin/affiliate/*/page` | `affiliates.view/manage` / 归因佣金提现 |
| 8 | 产品订单 `#/order-list` | `/admin/orders` | 部分完成 | `GET /admin/orders/page` + `POST /admin/orders/commission` + `POST /admin/orders/{id}/notes` + `POST /admin/orders/{id}/cancel` + `POST /admin/orders/{id}/review` + `DELETE /admin/orders/{id}` + `POST /admin/orders/bulk-*` | `orders.view/manage`，提成另需 `sales.view` / 真实分页、客户/日期/付款筛选、IP、URL恢复、创建入口、确认和逐项批量结果；客户资料备注与订单内部备注分列；当前提成组和本月阶梯只读预估，不写结算；四尺寸页面无横向溢出，320px 可滚动访问提成与操作列；[验收](admin-batch-admin-orders-acceptance-20261011.md) |
| 9 | 续费订单 `#/renewal-order` | `/admin/orders?kind=renew` | 已完成 | `GET /admin/renewal-orders` | `orders.view` / 续费账单、客户范围/客户ID、状态/关键词筛选、总数/页容量、详情返回上下文；四尺寸验收 |
| 10 | 流量包订单 `#/dcim-traffic-log` | `/admin/traffic-orders` | 部分完成 | `GET /admin/traffic-orders` + `GET /admin/traffic-orders/{id}` + `POST /admin/traffic-orders/{id}/cancel` + `POST /admin/traffic-orders/{id}/delivery-verification` | `orders.view/manage`、`services.manage` / 真实购买→账单→支付→待人工交付；客户范围、状态/关键词筛选、分页、详情、取消/退款返库存；版本冲突、幂等、审计；PG18专项通过，四尺寸浏览器通过，目录CRUD/取消/删除确认/筛选URL恢复已验收；[验收](admin-batch-traffic-acceptance-20261011.md)；自动资源交付未实现 |
| 11 | 业务列表 `#/customer-product` | `/admin/services` | 已完成 | `GET /admin/services/page` | `services.view` / 状态页签 |
| 12 | 产品暂停请求 `#/customer-cancelreq` | `/admin/cancellations` | 已完成 | `GET/POST /admin/cancellations` | `services.view/manage` / 决策审计 |
| 13 | 交易流水 `#/business-statement` | `/admin/transactions` | 部分完成 | `GET /admin/transactions/page` | `invoices.view` / 成功支付、余额与已付账单事件分页；整数分金额/渠道/方向/上海日期筛选、客户范围、URL与详情回跳；不提供混合现金汇总；[本批验收](admin-entry-pages-acceptance-20261013.md) |
| 14 | 账单管理 `#/bill-management` | `/admin/invoices` | 已完成 | `GET /admin/invoices/page` | `invoices.view/manage` / 合并和取消 |
| 15 | 信用额管理 `#/credit-management` | `/admin/credit` | 部分完成 | `GET /admin/credit` + `PUT /admin/credit/{id}/limit` + `GET /admin/credit/consumptions` + `GET /admin/credit/repayments` + `POST /admin/credit/consumptions/{id}/repay` + `PUT /admin/credit/{id}/freeze` + `POST /invoices/{id}/pay-credit` | `credit.view/manage` / 独立账户、信用消费/真实线下还款/冻结、客户/日期/债务状态分页、余额隔离、权限/范围/版本/幂等/审计；三标签四尺寸与未知结果恢复；线上还款、已还款现金退付、授信审批仍待补；[验收](admin-credit-supplier-acceptance-20261012.md) |
| 16 | 提现审核 `#/customer-withdrawal` | `/admin/affiliate?tab=withdrawals` | 部分完成 | `GET/POST /admin/affiliate/withdrawals` | `affiliates.view/manage` / 审核打款 |
| 17 | 发票列表 `#/invoice-audit` | `/admin/invoice-applications` | 已完成 | `GET/POST /admin/invoice-applications` | `invoice_applications.view/manage` |
| 18 | 合同列表 `#/contracts_audit` | `/admin/contracts` | 部分完成 | `GET/POST /admin/contracts` + `GET /admin/contracts/{id}` + `POST /admin/contracts/{id}/review` + `PUT /admin/contracts/{id}/shipping` + `POST /admin/contracts/bulk-void` | `contracts.view/manage` / 真实服务合同快照、客户确认→审核→邮寄→作废、分页筛选、权限范围、版本幂等和审计；列表及邮寄弹窗四尺寸通过；PDF/第三方电子签章、取消邮寄、模板强制签订策略仍待补；[验收](admin-contract-lifecycle-acceptance-20261012.md) |
| 19 | 工单列表 `#/support-ticket` | `/admin/tickets` | 已完成 | `GET /admin/tickets` | `tickets.view/manage` / 部门范围 |
| 20 | 工单统计 `#/support-statistics` | `/admin/reports?tab=tickets` | 已完成 | `GET /admin/reports/tickets?year=&month=` | `reports.view` / 上海日期、部门范围、真实空态；四尺寸已验收 |
| 21 | 年度收入统计 `#/annual-statistics` | `/admin/reports?tab=annual` | 部分完成 | `GET /admin/reports/income` | `reports.view` / 年份切换 |
| 22 | 新客户 `#/new-customer` | `/admin/reports?tab=new-clients` | 部分完成 | `GET /admin/reports/new-clients` | `reports.view` / 日期范围 |
| 23 | 产品收入 `#/product-revenue` | `/admin/reports?tab=products` | 部分完成 | `GET /admin/reports/product-income` | `reports.view` / 产品排行 |
| 24 | 收入排名 `#/revenue-ranking` | `/admin/reports?tab=ranking` | 部分完成 | `GET /admin/reports/customer-ranking` | `reports.view` / 范围权限 |
| 25 | 定时任务状态 `#/timing-results` | `/admin/jobs` | 部分完成 | `GET /admin/jobs` | `jobs.view` / 实时队列汇总；定时趋势日志待补 |
| 26 | 任务队列 `#/statistics-taskQueue` | `/admin/jobs/queue` | 部分完成 | `GET /admin/jobs/queue` | `jobs.view` / 客户范围、筛选分页和安全摘要；统一重试待补 |
| 27 | 数据库状态 `#/database-message` | `/admin/system/database` | 已完成 | `GET /admin/system/database` | `settings.view` / PostgreSQL 连接池、大小和迁移只读健康 |
| 28 | 自定义客户字段 `#/customer-custom` | `/admin/customer-fields` | 部分完成 | `GET/POST/PATCH/DELETE /admin/customer-fields` | `customers.manage` / 真实分页、字段规则、版本冲突、幂等和审计；客户创建/详情已接入类型化值读写；完整注册表单联动和四尺寸待验收 |
| 29 | 推介设置 `#/promotion_plan` | `/admin/affiliate/settings` | 部分完成 | `GET/PUT /admin/affiliate/policy`、`GET /admin/affiliate/rules/schema`、`GET/PUT /admin/affiliate/rules` | `affiliates.view/manage` / 默认计划、归因冻结周期、默认四类佣金、提现门槛；商品专属规则（继承/停用/比例/固定额/上限）与月营业额阶梯已接入真实 API、版本快照、幂等、草稿恢复和409冲突；[本批验收](admin-batch-affiliate-sales-acceptance-20261013.md) |
| 30 | 客户等级 `#/customer-level` | `/admin/customer-levels` | 已完成 | `GET/POST/PATCH/DELETE /admin/customer-levels` | `customer_levels.view/manage` / 阈值统计、分页、版本冲突、幂等和审计 |
| 31 | 客户分组与折扣 `#/customer-group` | `/admin/customer-groups`、`/admin/customer-groups?tab=products`、`/admin/customer-groups?tab=discounts` | 部分完成 | `GET/POST/PATCH/DELETE /admin/customer-groups`、`GET/POST/PATCH/DELETE /admin/customer-product-groups`、`GET/PUT /admin/customer-product-discounts` | `customer_groups.view/manage` / 客户组、商品分组和客户组折扣真实分页；百分比/固定金额校验、版本冲突、幂等、审计和危险操作确认；商品分组/折扣四尺寸浏览器待验收 |
| 32 | 实名设置 `#/authentication-setting` | `/admin/certification-settings` | 已完成 | `GET/PUT /admin/certification-settings`、`GET /admin/channel-instances?category=certification` | `certifications.view/manage`、版本冲突、幂等和审计 |
| 33 | 货币配置 `#/currency-settings` | `/admin/currencies` | 已完成 | `GET/POST/PATCH/DELETE /admin/currencies`、`POST /admin/currencies/{id}/default` | `settings.view` / `settings.manage`；版本、幂等、审计 |
| 34 | 支付接口 `#/payment-interface` | `/admin/channels?category=payment` | 已完成 | `GET/POST/PUT/DELETE /admin/channel-instances` | `@super` / 能力、密钥、启停 |
| 35 | 充值与财务 `#/general-settings/finance` | `/admin/finance-settings` | 已完成 | `GET/PUT /admin/finance-settings` | `settings.view` / `settings.manage`；版本、幂等、审计 |
| 36 | 优惠码 `#/promo-code` | `/admin/promo-codes` | 部分完成 | `GET/POST/PATCH /admin/promo-codes` | `promos.view/manage` / 已有新增、启停、循环/升级优惠与本页状态过滤；主页面未接起止时间编辑和真实分页。建单弹窗已有 `/admin/promo-codes/page` 目录分页，不等于主页面补齐；[能力映射](restore-evidence-scope-acceptance-20261016.md) |
| 37 | 发票设置 `#/voucher-setting` | `/admin/invoice-settings` | 已完成 | `GET/PUT /admin/invoice-settings` | `settings.view` / `settings.manage`；版本、幂等、审计；快递读取 |
| 38 | 合同设置 `#/contracts_setting` | `/admin/contract-settings` | 部分完成 | `GET/PUT /admin/contract-settings` + `GET/POST /admin/contract-templates` + `PUT /admin/contract-templates/{id}` | `settings.view` / `settings.manage`；总开关与纯文本模板管理、草稿和URL页签分页、版本幂等审计、事务会话锁；两个页签历史四尺寸通过；强制签订/商品模板绑定策略未实现；[验收](admin-contract-lifecycle-acceptance-20261012.md)、[能力映射](restore-evidence-scope-acceptance-20261016.md) |
| 39 | 信用额设置 `#/credit-setting` | `/admin/credit-settings` | 已完成 | `GET/PUT /admin/credit-settings` | `settings.view` / `settings.manage`；版本、幂等、审计 |
| 40 | 工单部门 `#/work-order-dept` | `/admin/ticket-departments` | 已完成 | `GET/POST/PATCH/DELETE /admin/ticket-departments` | `@super` / 删除保护 |
| 41 | 工单状态 `#/work-order-status` | `/admin/ticket-statuses` | 已完成 | `GET/POST/PATCH/DELETE /admin/ticket-statuses` | `tickets.view/manage` / 默认状态保护、创建编辑删除确认、幂等、版本冲突、审计；四尺寸浏览器验收 |
| 42 | 工单传递 `#/work-order-rules` | `/admin/ticket-rules` | 已完成 | `GET/POST/PATCH/DELETE /admin/ticket-rules` | `tickets.view/manage` / 真实空态、创建编辑删除确认、幂等、关联校验、版本冲突、审计；四尺寸浏览器验收 |
| 43 | 常规设置 `#/general-settings/general` | `/admin/settings` | 部分完成 | `GET/PUT /admin/site-settings` | `settings.view/manage` / 分区草稿 |
| 44 | 第三方登录 `#/third-login` | `/admin/channels?category=oauth` | 部分完成 | `GET/POST/PUT /admin/channel-instances` | `@super` / OAuth 能力 |
| 45 | 定时任务 `#/automatic-tasks` | `/admin/job-settings` | 已完成 | `GET/PUT /admin/scheduler-settings` | `@super` / 到期和账单提醒、版本冲突、幂等；四尺寸已验收 |
| 46 | 注册登录 `#/login-register` | `/admin/auth-settings` | 部分完成 | `GET/PUT /admin/auth-settings` | `settings.view/manage` / 真实客户登录/注册、登录方式和验证码渠道策略；版本、幂等、审计；保存/取消/409草稿、未知结果恢复和四尺寸通过；独立手机注册、国际电话和注册字段联动待补 |
| 47 | 分组权限 `#/permissions-managment` | `/admin/access?tab=roles` | 部分完成 | `GET/POST/PATCH/DELETE /admin/roles` | `@super` / 权限矩阵 |
| 48 | 销售设置 `#/sales-management` | `/admin/sales-settings`、`/admin/sales-settlements` | 部分完成 | `GET/PUT /admin/sales-settings`、`GET/POST/PATCH/DELETE /admin/sales-commission-groups`、`GET/POST/PATCH/DELETE /admin/sales-commission-tiers`、`GET/PATCH /admin/sales-staff`、`POST /admin/sales-settlements/preview`、`POST/GET /admin/sales-settlements`、`GET /admin/sales-settlements/{id}` | `sales.view/manage` / 提成组、阶梯、销售人员绑定和按月新购佣金结算；不可变规则/销售归属快照、退款原子冲正、幂等、预览摘要409、审计和全部客户范围校验；浏览器四尺寸及冲突/未知结果恢复已验收；真实浏览器退款独立链路未形成证据；[本批验收](admin-batch-affiliate-sales-acceptance-20261013.md) |
| 49 | 员工管理 `#/admin-management` | `/admin/access?tab=staff` | 部分完成 | `GET/POST/PATCH /admin/staff` | `@super` / 部门范围 |
| 50 | 接口设置 `#/sms-template/sms` | `/admin/channels?category=sms` | 部分完成 | `GET/POST/PUT /admin/channel-instances` | `@super` / 测试发送 |
| 51 | 邮件模板 `#/email-list` | `/admin/notifications?channel=email` | 部分完成 | `GET/POST/PATCH /admin/notification-templates` | `notifications.view/manage` / 服务端筛选分页 |
| 52 | 短信模板 `#/sms-template-index` | `/admin/notifications?channel=sms` | 部分完成 | `GET/POST/PATCH /admin/notification-templates` | `notifications.view/manage` / 服务端筛选分页 |
| 53 | 发送设置 `#/sms-send-settings` | `/admin/sms-settings` | 已完成 | `GET/PUT/POST /admin/sms-settings` | `@super` / 测试短信 |
| 54 | 二次验证 `#/twice-confirm` | `/admin/security/mfa` | 部分完成 | `GET/PUT /admin/security/mfa` | `settings.view/manage` / 客户/管理员个人MFA开通策略；已有MFA不能绕过；版本、幂等、审计；启停确认、未知请求恢复和四尺寸通过；旧版按动作二次确认策略及前后台标签深度待补 |
| 55 | 验证码设置 `#/general-settings/captcha` | `/admin/auth-settings?tab=captcha` | 部分完成 | `GET/PUT /admin/auth-settings` | `settings.view/manage` / 验证码渠道、图形开关和管理员失败策略；真实保存与四尺寸通过，自动开启策略待补 |
| 56 | 黑名单列表 `#/black-list` | `/admin/blacklist` | 已完成 | `GET/POST/PATCH/DELETE /admin/blacklist` | `security.view/manage` / 手动用户名/IP 阻断、管理员登录失败按认证策略自动封禁、确认解除、幂等、审计、版本冲突；四尺寸验收通过 |
| 57 | 数据迁移 `#/data-migration` | `/admin/migrations` | 部分完成 | `GET/POST /admin/migrations` | `migrations.view/manage` |
| 58 | 关于 `#/about` | `/admin/system/about` | 已完成 | `GET /admin/system/about` | `settings.view` / 真实技术栈和构建信息 |
| 59 | 系统升级 `#/system-message` | `/admin/system/upgrade` | 已完成 | `GET /admin/system/upgrade` | `settings.view` / 编译迁移清单与数据库已应用版本差异检查 |
| 60 | php信息 `#/php-message` | `/admin/system/runtime` | 已完成 | `GET /admin/system/runtime` | `settings.view` / Go、PG18、迁移和运行信息 |
| 61 | 短信日志 `#/sms-log` | `/admin/logs/sms` | 已完成 | `GET /admin/logs/sms` | `notifications.view` / 真实出站状态、用户、电话、日期、筛选、分页、正文脱敏；四尺寸浏览器验收 |
| 62 | 站内信日志 `#/station-letter-log` | `/admin/logs/inbox` | 已完成 | `GET /admin/logs/inbox` | `notifications.view` / 真实表为空态、主题、用户、状态、日期、筛选、分页；四尺寸浏览器验收 |
| 63 | API日志 `#/api-log` | `/admin/logs/api` | 已完成 | `GET /admin/logs/api` | `audit.view` / 认证请求真实记录、方法、状态码、用户、IP、日期、关键词筛选、分页；四尺寸浏览器验收 |
| 64 | 定时任务日志 `#/automatic-task-log` | `/admin/logs/jobs` | 已完成 | `GET /admin/logs/jobs`, `GET /admin/logs/jobs/trend` | `@super` / 真实执行记录、上海日期筛选、分页和31日趋势；四尺寸浏览器验收 |
| 65 | 日志清理 `#/log-cleanup` | `/admin/logs/cleanup` | 已完成 | `POST /admin/logs/cleanup` | `@super` / 二次确认、24小时保留窗、幂等、审计、移动端表单验收 |
| 66 | 系统日志 `#/system-log` | `/admin/logs/system` | 已完成 | `GET /admin/logs?kind=system` | `audit.view` / 安全元数据、搜索分页、真实空态；四尺寸已验收 |
| 67 | 管理员登录日志 `#/system-admin-log` | `/admin/logs/admin-login` | 部分完成 | `GET /admin/logs/admin-login` | `audit.view` / 登录、最后访问、注销、用户名、IP、日期、分页；四尺寸待验收 |
| 68 | 邮件日志 `#/email-log` | `/admin/logs/email` | 部分完成 | `GET /admin/logs/email` | `notifications.view` / 主题、收件人、成功状态、失败原因、用户、日期、分页 |
| 69 | 商品管理 `#/product-server` | `/admin/products` | 已完成 | `GET/POST/PATCH /admin/products` | `products.view/manage` |
| 70 | 流量包管理 `#/dcim-traffic` | `/admin/traffic-products` | 部分完成 | `GET/POST /admin/traffic-products` + `PATCH/DELETE /admin/traffic-products/{id}` | `products.view/manage` / 真实目录、容量/整数分价格/启停、库存/已售、适用产品搜索分页、分页；有订单删除保护、版本冲突、并发幂等、审计；PG18专项通过，四尺寸浏览器通过，目录CRUD/取消/删除确认/筛选URL恢复已验收；[验收](admin-batch-traffic-acceptance-20261011.md) |
| 71 | 通用接口 `#/server-settings` | `/admin/channels?category=provision` | 部分完成 | `GET/POST/PUT /admin/channel-instances` | `@super` / 开通能力 |
| 72 | 魔方DCIM `#/dcim` | `/admin/suppliers?kind=dcim` | 部分完成 | `GET/POST /admin/suppliers` | `products.view/manage` |
| 73 | 魔方云 `#/zjmfcloud` | `/admin/suppliers?kind=cloud` | 部分完成 | `GET/POST /admin/suppliers` | `products.view/manage` |
| 74 | 全局可配置项 `#/configurable-option` | `/admin/configurable-options` | 部分完成 | `GET/POST /admin/global-configuration-groups` + `GET/PUT/DELETE /admin/global-configuration-groups/{id}` + `POST /admin/global-configuration-groups/copy` + `PUT /admin/global-configuration-groups/{id}/assignments` | `products.view/manage` / 130全局定义、分配与定价投影 |
| 75 | 商品订购设置 `#/order-product` | `/admin/order-settings` | 部分完成 | `GET/PUT /admin/order-settings` | `settings.view/manage` / 131全局与商品准入、持久幂等 |
| 76 | 基础信息 `#/base-info` | `/admin/site-settings?tab=basic` | 部分完成 | `GET/PUT /admin/site-settings` | `settings.view/manage` / 版本、审计和四尺寸验收沿用站点设置批次 |
| 77 | 主题模板 `#/theme-template` | `/admin/theme-settings` | 已完成 | `GET/PUT /admin/theme-settings` | `settings.view/manage` / 120 迁移、幂等、版本冲突、审计；四尺寸验收 |
| 78 | 官网自定义字段 `#/custom-template-fields` | `/admin/site-fields` | 已完成 | `GET/POST/PATCH/DELETE /admin/site-fields` | `content.view/manage` / 真实分页 CRUD、幂等、版本冲突、审计；四尺寸验收 |
| 79 | 导航管理 `#/menu_manage` | `/admin/navigation` | 已完成 | `GET/POST/PATCH/DELETE /admin/navigation` | `content.view/manage` / 父子循环保护、真实 CRUD、幂等、版本冲突、审计；四尺寸验收 |
| 80 | 友情链接 `#/friendly_link` | `/admin/friendly-links` | 已完成 | `GET/POST/PATCH/DELETE /admin/friendly-links` | `content.view/manage` / HTTPS 校验、真实 CRUD、幂等、版本冲突、审计；四尺寸验收 |
| 81 | 文件分组 `#/service-support` | `/admin/file-groups` | 已完成 | `GET/POST/PUT/DELETE /admin/file-groups` | `downloads.view/manage` / 复用下载分类、真实分页 CRUD、版本冲突、审计；四尺寸验收 |
| 82 | 新闻列表 `#/news-list` | `/admin/content?kind=news` | 部分完成 | `GET/POST/PUT /admin/content` | `content.view/manage` |
| 83 | 新闻分类 `#/news-category` | `/admin/content?tab=categories` | 部分完成 | `GET/POST/PUT /admin/content-categories` | `content.manage` |
| 84 | 帮助列表 `#/help-list` | `/admin/content?kind=help` | 部分完成 | `GET/POST/PUT /admin/content` | `content.view/manage` |
| 85 | 帮助分类 `#/help-category` | `/admin/content?tab=categories&kind=help` | 部分完成 | `GET/POST/PUT /admin/content-categories` | `content.manage` |
| 86 | 基础设置 `#/set` | `/admin/site-settings?tab=display` | 部分完成 | `GET/PUT /admin/site-settings` | `settings.view/manage` |
| 87 | 供应商管理 `#/zjmf-api` | `/admin/suppliers` | 已完成 | `GET/POST/PUT /admin/suppliers` | `products.view/manage` |
| 88 | 商品管理 `#/commodity-list` | `/admin/suppliers/catalog` | 部分完成 | `GET /admin/suppliers` + `POST /admin/suppliers/{id}/catalog` + `GET /admin/suppliers/{id}/products/{product_id}` + `POST /admin/suppliers/{id}/import` | `products.view/manage` / 独立目录、账户选择、真实空态；拉取、详情与导入保持manage；目录快照本地筛选分页，URL恢复与真实账户新入口导入浏览器验收待补；[本批验收](admin-entry-pages-acceptance-20261013.md) |
| 89 | 产品管理 `#/commodity-product` | `/admin/products` | 部分完成 | `GET/POST/PATCH /admin/products` | `products.view/manage` |
| 90 | 任务队列 `#/commodity-taskQueue` | `/admin/suppliers/jobs` | 部分完成 | `GET /admin/supplier-jobs` + `POST /admin/supplier-jobs/{id}/retry` | `products.view`；重试另需 `services.manage/view` / 真实供应商交付任务、8列、客户/供应商/动作/状态/日期/关键词分页、URL恢复与四尺寸；只重试未外调失败同步，未知结果原请求恢复；危险写任务须核验；[验收](admin-credit-supplier-acceptance-20261012.md) |
| 91 | 服务器列表 `#/munual-resource` | `/admin/resources` | 部分完成 | `GET /admin/resources` | `resources.view` / 真实服务资源分页、IP/供应商/状态筛选、URL恢复；客户范围、脱敏白名单、交付快照绑定；四尺寸、真实筛选/空态/URL恢复/错误恢复已验收；独立资源增改、成本/配置/备注和资源动作弹窗待补 |
| 92 | 订单列表 `#/supplier-order-list` | `/admin/suppliers/orders` | 部分完成 | `GET /admin/supplier-orders` | `orders.view/manage` / 交付快照供应商归属、客户范围与真实分页、完整订单金额/已证明成本、筛选及详情/创建取消返回上下文、四尺寸；上游订单操作和完整旧版成本结算仍待补；[验收](admin-credit-supplier-acceptance-20261012.md) |
| 93 | 续费订单 `#/supplier-renewal-order` | `/admin/suppliers/renewals` | 部分完成 | `GET /admin/supplier-renewals` | `orders.view`；账单详情另需 `invoices.view` / 真实本地供应商服务续费记录、客户/供应商/日期/付款状态分页与返回上下文、四尺寸；成本未知为null，上游续费调用未支持；[验收](admin-credit-supplier-acceptance-20261012.md) |
| 94 | 任务队列 `#/task-queue` | `/admin/suppliers/tasks` | 部分完成 | `GET /admin/supplier-tasks` + `POST /admin/supplier-jobs/{id}/retry` | `jobs.view`；重试另需 `services.manage/view` / 与90页使用同一真实队列，安全字段/客户范围、筛选/分页/URL/错误恢复和四尺寸；仅安全同步重排、危险任务人工核验；[验收](admin-credit-supplier-acceptance-20261012.md) |
| 95 | API设置 `#/api-setup` | `/admin/suppliers/api-settings` | 部分完成 | `GET/PUT /admin/supplier-api-settings` + `GET /admin/supplier-providers` + `POST /admin/suppliers/{id}/test-connection` | `products.view/manage` / 资源 API 总开关、实名/手机门禁、编译供应商能力/schema、账户脱敏摘要、只读连接测试；版本、幂等、审计和权限；保存/取消/连接测试及四尺寸布局已验收，完整状态矩阵待补；[验收](admin-supplier-api-settings-acceptance-20261012.md) |
| 96 | 我的应用 `#/app-store` | `/admin/apps` | 部分完成 | `GET /admin/apps?view=configured` | `@super` / 编译模块真实配置聚合、启停/凭据计数、配置入口、筛选分页和 URL 恢复；完整旧版应用管理动作待补 |
| 97 | 应用商店 `#/app-store/app-list` | `/admin/apps/store` | 部分完成 | `GET /admin/apps?view=catalogue` | `@super` / 编译模块目录、能力/schema、工厂状态、配置入口、筛选分页和空态错误；在线安装/升级不属于编译期模型 |
| 98 | 添加客户 `#/customer-add` | `/admin/customers/new` | 已完成 | `POST /admin/customers` | `customers.manage` / 校验和返回 |
| 99 | 新建订单 `#/add-order` | `/admin/orders/new` | 部分完成 | `GET /admin/orders/create-options` + `GET /admin/promo-codes/page` + `POST /admin/promo-codes` + `POST /admin/orders/quote` + `POST /admin/orders` + `POST /admin/orders/{id}/invoice` | `orders.manage`；优惠码目录另需`promos.view`，新建另需`promos.manage` / 客户/商品搜索分页、目录选择/新增弹窗、报价校验、多商品/配置/数量/资料、价格覆盖、付款/账单/邮件、草稿恢复、版本/幂等/权限/审计；创建流程与新增优惠码弹窗四尺寸均已通过；真实邮件投递和复杂多级商品待验；[验收](admin-batch-admin-orders-acceptance-20261011.md) |
| 100 | 新建工单 `#/add-support-ticket` | `/admin/tickets/new` | 部分完成 | `GET /admin/tickets/create-options` + `POST /admin/tickets` + `POST /tickets/{id}/attachments` | `tickets.manage` / 真实客户、部门、服务、处理人搜索分页；优先级、抄送、邮件入队、附件上传、草稿恢复、取消确认、幂等与结果不确定保护；四尺寸浏览器验收通过；附件失败后的人工复核与完整旧版字段仍待补 |
| 101 | 开具发票 `#/invoice` | `/admin/invoices/new` | 已完成 | `POST /admin/invoices` | `invoices.manage` |
| 102 | 添加商品分组 `#/add-product-group` | `/admin/catalog-groups/new` | 部分完成 | `GET/POST /admin/catalog-groups/{kind}` | `products.manage` / 一级/二级独立创建、真实父分组、共用校验、payload绑定幂等、审计及权限复核；返回原分组层级；[本批验收](admin-entry-pages-acceptance-20261013.md) |
| 103 | 编辑商品配置 `#/edit-product` | `/admin/products/{id}` | 部分完成 | `GET/PATCH /admin/products/{id}` | `products.view/manage` / 已有基本信息、定价、服务归属、配置项/规则、购买字段及供应来源；旧版十分区未完整接入。推介商品规则、下载商品权限、商品/配置升级 API 与组件已有能力，商品编辑入口及旧版逐项语义仍待补齐；试用、链接等缺口见[十分区映射](restore-evidence-scope-acceptance-20261016.md) |
| 104 | 编辑管理员 `#/admin-edit` | `/admin/access/staff/{id}` | 部分完成 | `GET/PATCH /admin/staff/{id}` | `@super` / 独立详情、共用账号/角色/范围字段、密码确认、提交重置确认、会话复核/版本/幂等/审计、保护超级管理员；旧版语言、工单签名与业务邮件偏好待补；[本批验收](admin-entry-pages-acceptance-20261013.md) |
| 105 | 创建短信模板 `#/sms-create-template` | `/admin/notifications/new?channel=sms` | 部分完成 | `POST /admin/notification-templates` | `notifications.manage` / 独立创建复用模板编辑器、真实列表回跳、变量/供应商模板ID、payload绑定幂等、权限凭据重查、审计version1；真实供应商模板发送本批未测；[本批验收](admin-entry-pages-acceptance-20261013.md) |

## 批次验收记录

| 批次 | 范围 | 状态 | 证据 |
|---|---|---|---|
| 第一批 | 设置、渠道、短信、邮件、验证码、通知 | 进行中 | 设置与营销页面四尺寸验收见 `docs/admin-batch-settings-marketing-acceptance-20261011.md`；渠道和通知深度仍在补齐 |
| 第二批 | 客户扩展 | 进行中 | 客户资源池、实名审批、业绩与等级已交付；营销、返佣和注册字段联动继续补齐 |
| 第三批 | 业务与订单 | 进行中 | 普通/续费分页、流量包付款→待人工交付、后台报价创建/延期出账、订单版本化操作/批量/审计删除、客户资料备注、订单提成只读预估已交付；自动流量交付待补；[订单验收](admin-batch-admin-orders-acceptance-20261011.md)、[流量包验收](admin-batch-traffic-acceptance-20261011.md) |
| 第四批 | 财务与合同 | 进行中 | 独立授信、信用消费、线下还款和冻结已补；线上还款/已还款退付、授信审批继续补齐；合同基础生命周期已补，PDF/电子签章和高级模板策略待补 |
| 第五批 | 工单与通知 | 进行中 | 新建工单页面和真实创建 API 已接入并完成四尺寸浏览器验收；工单统计/设置已有部分能力；通知队列、模板和发送日志继续补齐；[验收](admin-batch-admin-tickets-acceptance-20261011.md) |
| 第六批 | 报表与任务运维 | 进行中 | 报表销售/工单统计、任务配置、系统日志和日志清理已接入真实 API；8 个页面在 1440/1024/390/320 完成无横向溢出验收，证据见 `docs/admin-batch-reports-operations-acceptance-20261011.md`；统一重试、投递结果和更多运维报表待补 |
| 第七批 | 权限、员工与销售 | 进行中 | 注册登录/MFA设置本批收口；销售设置、商品/阶梯规则和销售结算已补并完成隔离 PG18、四尺寸及冲突/未知结果验收；完整权限页面与真实浏览器退款展示仍待补；[本批验收](admin-batch-affiliate-sales-acceptance-20261013.md) |
| 第八批 | 内容与官网配置 | 进行中 | 主题/字段/导航/友情链接/文件分组已有真实CRUD及四尺寸；新闻/帮助完整还原继续补齐 |
| 第九批 | 供应商与资源商店 | 进行中 | 资源只读清单、供应商订单/本地续费记录及真实任务队列已补；第95页资源 API 设置、供应商目录/账户摘要及只读连通性测试已验收；独立资源CRUD、上游续费/复杂配置和应用目录待补 |
| 第十批 | 创建与编辑页 | 进行中 | 新建订单优惠码目录/新增弹窗与真实分页 API 已补；订单创建/延期出账及新增弹窗均完成四尺寸浏览器验收；其余创建编辑页继续补齐；[验收](admin-batch-admin-orders-acceptance-20261011.md) |

2026-10-12 独立分支资源/信用/认证批次已集成并检验；台账仍为47项已完成、48项部分完成、10项待实现，表示登记状态而非功能完成率。[本批验收与边界](admin-parallel-acceptance-20261012.md)。

2026-10-12 信用业务/供应商批次：原待实现90/92/93/94页新增真实API和页面，按完整旧版剩余边界登记为部分完成。台账现为47项已完成、52项部分完成、6项待实现；登记状态不是功能完成率。[本批验收](admin-credit-supplier-acceptance-20261012.md)。

2026-10-12 全局配置/订购批次：74/75新增真实配置、跨商品持续投影、复制/分配、商品级和全局验证要求、报价/结账准入、审计及版本幂等。两页和配置编辑弹窗有四尺寸证据；完整状态矩阵、多级全局联动和全部弹窗四尺寸仍未收口，因此登记部分完成。台账现为47完成、54部分、4待实现。[本批验收](admin-options-order-acceptance-20261012.md)。

2026-10-12 合同批次：第18页从待实现改为部分完成，38页补合同模板页签。真实客户确认、审核、邮寄和作废通过；HTML打印不作为PDF或电子签章验收。现为47完成、55部分、3待实现；[验收](admin-contract-lifecycle-acceptance-20261012.md)。

2026-10-12 编译期模块基础批次：公共渠道目录增加类型化工厂解析；支付、短信、SMTP、实名认证和供应商注册迁移到编译进二进制的注册契约，保留 `channel_instances`/`supplier_accounts` 的配置与密钥边界。供应商 API 设置、我的应用和应用商店随后按独立批次接入。设计与扩展约束见 [编译期模块与渠道扩展契约](compiled-module-architecture.md)。

2026-10-13 供应商 API 设置：第95页接入资源 API 全局设置、密钥签发实名/手机门禁、客户 Bearer 总开关、编译供应商描述目录、脱敏账户摘要和只读上游目录连通测试。设置保存使用版本冲突、幂等回执与审计；测试复核权限并在外部调用期间不持有数据库事务。隔离 PG18 专项、浏览器保存/取消/连接测试及 1440/1024/390/320 验收通过；完整浏览器状态矩阵未收口，登记部分完成。台账现为47完成、56部分、2待实现；[验收记录](admin-supplier-api-settings-acceptance-20261012.md)。

2026-10-13 编译模块应用目录：第96/97页接入 `GET /admin/apps` 真实编译目录 API，聚合 `channel_instances` 与 `supplier_accounts` 的实例/启用/凭据计数，响应不读取或回显配置和密钥；支持目录/已配置视图、搜索、类别/状态筛选、分页、URL恢复、权限拒绝、配置入口和真实构建元数据。隔离 PG18 HTTP契约/RBAC/脱敏测试、Webpack生产构建和两页浏览器验收通过；四尺寸截图覆盖“我的应用”共享卡片布局，应用商店页面已在1440px操作验收。完整旧版应用安装、在线升级和第三方发布不实现。台账现为47完成、58部分、0待实现；[验收记录](admin-compiled-apps-acceptance-20261013.md)。

2026-10-13 推介规则与销售结算批次：第29页接入商品专属规则和月营业额阶梯，第48页新增销售结算页面。商品覆盖优先于阶梯，规则和销售归属在结算时固化；信用消费须全部还清才进入营业额，退款/取消/发票退款通过数据库触发器原子冲正。结算仅保存不可变快照，不打款、不改余额；写入要求 `sales.view + sales.manage` 及全部客户范围，使用预览摘要、幂等键、409版本冲突和审计。迁移134/135在隔离 PG18 回归通过；全量 Go test、vet、race、Web typecheck/lint/build 通过；推介设置与销售结算在1440/1024/390/320完成真实 API 浏览器验收，包含保存/取消、分页、冲突、未知结果保留和权限恢复提示。服务端并发退款专项通过，但本批没有形成独立干净的浏览器退款截图；未部署，迁移未应用运行数据库。[验收记录](admin-batch-affiliate-sales-acceptance-20261013.md)。

2026-10-16 核心替代增量：普通人工账单外部退款登记/复核及客户在线充值已通过隔离PG18、资金race与四尺寸；工单三种公开事件已集成事务邮件/SMS队列、本人偏好、收件身份/去重与通知摘要，最终邮件渲染边界全量已通过。见[外部退款](external-refund-acceptance-20261016.md)、[在线充值](customer-online-topup-acceptance-20261016.md)、[工单通知](ticket-notification-acceptance-20261016.md)。这批没有关闭完整工单签名、部门自定义字段、上游协同、真实用户inbox及第三方收件验收，仍保持44完成/61部分；新增局部截图不替换历史失配记录。
