# 后台 105 页深度还原台账

来源：`/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md`。

状态定义：`已完成` 表示已有真实页面和 API；`部分完成` 表示页面或交互仍缺一部分；`待实现` 表示没有对应页面/API；`后端阻塞` 表示 UI 可排版但业务契约尚未具备。

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
| 8 | 产品订单 `#/order-list` | `/admin/orders` | 已完成 | `GET /admin/orders` | `orders.view/manage` / 状态、批量取消 |
| 9 | 续费订单 `#/renewal-order` | `/admin/orders?kind=renew` | 已完成 | `GET /admin/renewal-orders` | `orders.view` / 续费账单、客户范围、状态/关键词筛选、分页；四尺寸验收 |
| 10 | 流量包订单 `#/dcim-traffic-log` | `/admin/traffic-orders` | 部分完成 | `GET /api/v1/admin/traffic-orders`, `GET /api/v1/admin/traffic-orders/{id}`, `POST /api/v1/admin/traffic-orders/{id}/cancel`, `POST /api/v1/admin/traffic-orders/{id}/delivery-verification` | `orders.view/manage`、`services.manage` / 真实购买→账单→支付→待人工交付；客户范围、状态/关键词筛选、分页、详情、取消/退款返库存；版本冲突、幂等、审计；PG18专项通过，四尺寸浏览器待验收；自动资源交付未实现 |
| 11 | 业务列表 `#/customer-product` | `/admin/services` | 已完成 | `GET /admin/services/page` | `services.view` / 状态页签 |
| 12 | 产品暂停请求 `#/customer-cancelreq` | `/admin/cancellations` | 已完成 | `GET/POST /admin/cancellations` | `services.view/manage` / 决策审计 |
| 13 | 交易流水 `#/business-statement` | `/admin/transactions` | 部分完成 | `GET /admin/customer-transactions` | `invoices.view` / 金额和渠道筛选 |
| 14 | 账单管理 `#/bill-management` | `/admin/invoices` | 已完成 | `GET /admin/invoices/page` | `invoices.view/manage` / 合并和取消 |
| 15 | 信用额管理 `#/credit-management` | `/admin/credit` | 待实现 | `GET/POST /admin/credit` | `credit.view/manage` / 额度审计 |
| 16 | 提现审核 `#/customer-withdrawal` | `/admin/affiliate?tab=withdrawals` | 部分完成 | `GET/POST /admin/affiliate/withdrawals` | `affiliates.view/manage` / 审核打款 |
| 17 | 发票列表 `#/invoice-audit` | `/admin/invoice-applications` | 已完成 | `GET/POST /admin/invoice-applications` | `invoice_applications.view/manage` |
| 18 | 合同列表 `#/contracts_audit` | `/admin/contracts` | 待实现 | `GET/POST /admin/contracts` | `contracts.view/manage` |
| 19 | 工单列表 `#/support-ticket` | `/admin/tickets` | 已完成 | `GET /admin/tickets` | `tickets.view/manage` / 部门范围 |
| 20 | 工单统计 `#/support-statistics` | `/admin/reports?tab=tickets` | 已完成 | `GET /admin/reports/tickets?year=&month=` | `reports.view` / 上海日期、部门范围、真实空态；四尺寸已验收 |
| 21 | 年度收入统计 `#/annual-statistics` | `/admin/reports?tab=annual` | 部分完成 | `GET /admin/reports/income` | `reports.view` / 年份切换 |
| 22 | 新客户 `#/new-customer` | `/admin/reports?tab=new-clients` | 部分完成 | `GET /admin/reports/new-clients` | `reports.view` / 日期范围 |
| 23 | 产品收入 `#/product-revenue` | `/admin/reports?tab=products` | 部分完成 | `GET /admin/reports/product-income` | `reports.view` / 产品排行 |
| 24 | 收入排名 `#/revenue-ranking` | `/admin/reports?tab=ranking` | 部分完成 | `GET /admin/reports/customer-ranking` | `reports.view` / 范围权限 |
| 25 | 定时任务状态 `#/timing-results` | `/admin/jobs` | 部分完成 | `GET /admin/jobs` | `jobs.view` / 实时队列汇总；定时趋势日志待补 |
| 26 | 任务队列 `#/statistics-taskQueue` | `/admin/jobs/queue` | 部分完成 | `GET /admin/jobs/queue` | `jobs.view` / 客户范围、筛选分页和安全摘要；统一重试待补 |
| 27 | 数据库状态 `#/database-message` | `/admin/system/database` | 已完成 | `GET /admin/system/database` | `settings.view` / PostgreSQL 连接池、大小和迁移只读健康 |
| 28 | 自定义客户字段 `#/customer-custom` | `/admin/customer-fields` | 部分完成 | `GET/POST/PATCH/DELETE /admin/customer-fields` | `customers.manage` / 真实分页、字段规则、版本冲突、幂等和审计；客户资料消费待补 |
| 29 | 推介设置 `#/promotion_plan` | `/admin/affiliate/settings` | 部分完成 | `GET/PUT /admin/affiliate/settings` | `affiliates.manage` / 规则版本 |
| 30 | 客户等级 `#/customer-level` | `/admin/customer-levels` | 已完成 | `GET/POST/PATCH/DELETE /admin/customer-levels` | `customer_levels.view/manage` / 阈值统计、分页、版本冲突、幂等和审计 |
| 31 | 客户分组与折扣 `#/customer-group` | `/admin/customer-groups`、`/admin/customer-groups?tab=products`、`/admin/customer-groups?tab=discounts` | 部分完成 | `GET/POST/PATCH/DELETE /admin/customer-groups`、`GET/POST/PATCH/DELETE /admin/customer-product-groups`、`GET/PUT /admin/customer-product-discounts` | `customer_groups.view/manage` / 客户组、商品分组和客户组折扣真实分页；百分比/固定金额校验、版本冲突、幂等、审计和危险操作确认；商品分组/折扣四尺寸浏览器待验收 |
| 32 | 实名设置 `#/authentication-setting` | `/admin/certification-settings` | 已完成 | `GET/PUT /admin/certification-settings`、`GET /admin/channel-instances?category=certification` | `certifications.view/manage`、版本冲突、幂等和审计 |
| 33 | 货币配置 `#/currency-settings` | `/admin/currencies` | 已完成 | `GET/POST/PATCH/DELETE /admin/currencies`、`POST /admin/currencies/{id}/default` | `settings.view` / `settings.manage`；版本、幂等、审计 |
| 34 | 支付接口 `#/payment-interface` | `/admin/channels?category=payment` | 已完成 | `GET/POST/PUT/DELETE /admin/channel-instances` | `@super` / 能力、密钥、启停 |
| 35 | 充值与财务 `#/general-settings/finance` | `/admin/finance-settings` | 已完成 | `GET/PUT /admin/finance-settings` | `settings.view` / `settings.manage`；版本、幂等、审计 |
| 36 | 优惠码 `#/promo-code` | `/admin/promo-codes` | 已完成 | `GET/POST/PATCH /admin/promo-codes` | `promos.view/manage` |
| 37 | 发票设置 `#/voucher-setting` | `/admin/invoice-settings` | 已完成 | `GET/PUT /admin/invoice-settings` | `settings.view` / `settings.manage`；版本、幂等、审计；快递读取 |
| 38 | 合同设置 `#/contracts_setting` | `/admin/contract-settings` | 已完成 | `GET/PUT /admin/contract-settings` | `settings.view` / `settings.manage`；版本、幂等、审计 |
| 39 | 信用额设置 `#/credit-setting` | `/admin/credit-settings` | 已完成 | `GET/PUT /admin/credit-settings` | `settings.view` / `settings.manage`；版本、幂等、审计 |
| 40 | 工单部门 `#/work-order-dept` | `/admin/ticket-departments` | 已完成 | `GET/POST/PATCH/DELETE /admin/ticket-departments` | `@super` / 删除保护 |
| 41 | 工单状态 `#/work-order-status` | `/admin/ticket-statuses` | 已完成 | `GET/POST/PATCH/DELETE /admin/ticket-statuses` | `tickets.view/manage` / 默认状态保护、创建编辑删除确认、幂等、版本冲突、审计；四尺寸浏览器验收 |
| 42 | 工单传递 `#/work-order-rules` | `/admin/ticket-rules` | 已完成 | `GET/POST/PATCH/DELETE /admin/ticket-rules` | `tickets.view/manage` / 真实空态、创建编辑删除确认、幂等、关联校验、版本冲突、审计；四尺寸浏览器验收 |
| 43 | 常规设置 `#/general-settings/general` | `/admin/settings` | 部分完成 | `GET/PUT /admin/site-settings` | `settings.view/manage` / 分区草稿 |
| 44 | 第三方登录 `#/third-login` | `/admin/channels?category=oauth` | 部分完成 | `GET/POST/PUT /admin/channel-instances` | `@super` / OAuth 能力 |
| 45 | 定时任务 `#/automatic-tasks` | `/admin/job-settings` | 已完成 | `GET/PUT /admin/scheduler-settings` | `@super` / 到期和账单提醒、版本冲突、幂等；四尺寸已验收 |
| 46 | 注册登录 `#/login-register` | `/admin/auth-settings` | 待实现 | `GET/PUT /admin/auth-settings` | `settings.manage` |
| 47 | 分组权限 `#/permissions-managment` | `/admin/access?tab=roles` | 部分完成 | `GET/POST/PATCH/DELETE /admin/roles` | `@super` / 权限矩阵 |
| 48 | 销售设置 `#/sales-management` | `/admin/sales-settings` | 部分完成 | `GET/PUT /admin/sales-settings`、`GET/POST/PATCH/DELETE /admin/sales-commission-groups`、`GET/POST/PATCH/DELETE /admin/sales-commission-tiers`、`GET/PATCH /admin/sales-staff` | `sales.view/manage` / 提成组、阶梯、销售人员绑定、版本冲突、幂等和审计；浏览器四尺寸待验收 |
| 49 | 员工管理 `#/admin-management` | `/admin/access?tab=staff` | 部分完成 | `GET/POST/PATCH /admin/staff` | `@super` / 部门范围 |
| 50 | 接口设置 `#/sms-template/sms` | `/admin/channels?category=sms` | 部分完成 | `GET/POST/PUT /admin/channel-instances` | `@super` / 测试发送 |
| 51 | 邮件模板 `#/email-list` | `/admin/notifications?channel=email` | 部分完成 | `GET/POST/PATCH /admin/notification-templates` | `notifications.view/manage` / 服务端筛选分页 |
| 52 | 短信模板 `#/sms-template-index` | `/admin/notifications?channel=sms` | 部分完成 | `GET/POST/PATCH /admin/notification-templates` | `notifications.view/manage` / 服务端筛选分页 |
| 53 | 发送设置 `#/sms-send-settings` | `/admin/sms-settings` | 已完成 | `GET/PUT/POST /admin/sms-settings` | `@super` / 测试短信 |
| 54 | 二次验证 `#/twice-confirm` | `/admin/security/mfa` | 待实现 | `GET/PUT /admin/security/mfa` | `settings.manage` |
| 55 | 验证码设置 `#/general-settings/captcha` | `/admin/auth-settings?tab=captcha` | 部分完成 | `GET/PUT /admin/site-settings` | `settings.manage` / 验证码流程 |
| 56 | 黑名单列表 `#/black-list` | `/admin/blacklist` | 已完成 | `GET/POST/PATCH/DELETE /admin/blacklist` | `security.view/manage` / 手动用户名/IP 阻断、连续三次管理员登录失败自动封禁、确认解除、幂等、审计、版本冲突；四尺寸验收通过 |
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
| 70 | 流量包管理 `#/dcim-traffic` | `/admin/traffic-products` | 部分完成 | `GET/POST/PATCH/DELETE /api/v1/admin/traffic-products` | `products.view/manage` / 真实目录、容量/整数分价格/启停、库存/已售、适用产品搜索分页、分页；有订单删除保护、版本冲突、并发幂等、审计；PG18专项通过，四尺寸浏览器待验收 |
| 71 | 通用接口 `#/server-settings` | `/admin/channels?category=provision` | 部分完成 | `GET/POST/PUT /admin/channel-instances` | `@super` / 开通能力 |
| 72 | 魔方DCIM `#/dcim` | `/admin/suppliers?kind=dcim` | 部分完成 | `GET/POST /admin/suppliers` | `products.view/manage` |
| 73 | 魔方云 `#/zjmfcloud` | `/admin/suppliers?kind=cloud` | 部分完成 | `GET/POST /admin/suppliers` | `products.view/manage` |
| 74 | 全局可配置项 `#/configurable-option` | `/admin/configurable-options` | 待实现 | `GET/POST/PATCH /admin/configurable-options` | `products.manage` |
| 75 | 商品订购设置 `#/order-product` | `/admin/order-settings` | 待实现 | `GET/PUT /admin/order-settings` | `settings.manage` |
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
| 88 | 商品管理 `#/commodity-list` | `/admin/suppliers/catalog` | 部分完成 | `GET /admin/suppliers/{id}/products` | `products.view` |
| 89 | 产品管理 `#/commodity-product` | `/admin/products` | 部分完成 | `GET/POST/PATCH /admin/products` | `products.view/manage` |
| 90 | 任务队列 `#/commodity-taskQueue` | `/admin/suppliers/jobs` | 待实现 | `GET /admin/supplier-jobs` | `products.view` |
| 91 | 服务器列表 `#/munual-resource` | `/admin/resources` | 待实现 | `GET/POST/PATCH /admin/resources` | `resources.view/manage` |
| 92 | 订单列表 `#/supplier-order-list` | `/admin/suppliers/orders` | 待实现 | `GET /admin/supplier-orders` | `orders.view` |
| 93 | 续费订单 `#/supplier-renewal-order` | `/admin/suppliers/renewals` | 待实现 | `GET /admin/supplier-renewals` | `orders.view` |
| 94 | 任务队列 `#/task-queue` | `/admin/suppliers/tasks` | 待实现 | `GET /admin/supplier-tasks` | `jobs.view` |
| 95 | API设置 `#/api-setup` | `/admin/suppliers/api-settings` | 待实现 | `GET/PUT /admin/supplier-api-settings` | `products.manage` |
| 96 | 我的应用 `#/app-store` | `/admin/apps` | 待实现 | `GET /admin/apps` | `apps.view` |
| 97 | 应用商店 `#/app-store/app-list` | `/admin/apps/store` | 待实现 | `GET /admin/apps/store` | `apps.view` |
| 98 | 添加客户 `#/customer-add` | `/admin/customers/new` | 已完成 | `POST /admin/customers` | `customers.manage` / 校验和返回 |
| 99 | 新建订单 `#/add-order` | `/admin/orders/new` | 待实现 | `GET/POST /admin/orders` | `orders.manage` |
| 100 | 新建工单 `#/add-support-ticket` | `/admin/tickets/new` | 待实现 | `GET/POST /admin/tickets` | `tickets.manage` |
| 101 | 开具发票 `#/invoice` | `/admin/invoices/new` | 已完成 | `POST /admin/invoices` | `invoices.manage` |
| 102 | 添加商品分组 `#/add-product-group` | `/admin/catalog-groups/new` | 部分完成 | `POST /admin/catalog-groups` | `products.manage` |
| 103 | 编辑商品配置 `#/edit-product` | `/admin/products/{id}` | 已完成 | `GET/PATCH /admin/products/{id}` | `products.view/manage` |
| 104 | 编辑管理员 `#/admin-edit` | `/admin/access/staff/{id}` | 部分完成 | `GET/PATCH /admin/staff/{id}` | `@super` |
| 105 | 创建短信模板 `#/sms-create-template` | `/admin/notifications/new?channel=sms` | 部分完成 | `POST /admin/notification-templates` | `notifications.manage` |

## 批次验收记录

| 批次 | 范围 | 状态 | 证据 |
|---|---|---|---|
| 第一批 | 设置、渠道、短信、邮件、验证码、通知 | 进行中 | 设置与营销页面四尺寸验收见 `docs/admin-batch-settings-marketing-acceptance-20261011.md`；渠道和通知深度仍在补齐 |
| 第二批 | 客户扩展 | 进行中 | 客户资源池已完成；实名、业绩、营销和等级待补 |
| 第三批 | 业务与订单 | 待开始 | - |
| 第四批 | 财务与合同 | 待开始 | - |
| 第五批 | 工单与通知 | 待开始 | - |
| 第六批 | 报表与任务运维 | 进行中 | 报表销售/工单统计、任务配置、系统日志和日志清理已接入真实 API；8 个页面在 1440/1024/390/320 完成无横向溢出验收，证据见 `docs/admin-batch-reports-operations-acceptance-20261011.md`；统一重试、投递结果和更多运维报表待补 |
| 第七批 | 权限、员工与销售 | 待开始 | - |
| 第八批 | 内容与官网配置 | 待开始 | - |
| 第九批 | 供应商与资源商店 | 待开始 | - |
| 第十批 | 创建与编辑页 | 待开始 | - |
