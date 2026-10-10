# 后台 105 页深度还原台账

来源：`/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md`。

状态定义：`已完成` 表示已有真实页面和 API；`部分完成` 表示页面或交互仍缺一部分；`待实现` 表示没有对应页面/API；`后端阻塞` 表示 UI 可排版但业务契约尚未具备。

所有新版路径保持 `/admin/*` 稳定路由。列表页统一使用 `items/total/limit/offset`，设置页统一使用 `version/expected_version`，写操作必须经过权限、审计、幂等和版本冲突校验。

| # | 旧版页面 / hash | 新版路由 | 状态 | 主要 API | 权限 / 验收 |
|---:|---|---|---|---|---|
| 1 | 控制台概览 `#/login` | `/admin` | 已完成 | `GET /admin/dashboard` | `dashboard.view` / 四尺寸工作台 |
| 2 | 客户列表 `#/customer-list` | `/admin/customers` | 已完成 | `GET /admin/customers/page` | `customers.view` / 搜索分页返回 |
| 3 | 实名认证 `#/customer-authentication` | `/admin/certifications` | 部分完成 | `GET /admin/certifications/page` | `certifications.view` / 审批和历史 |
| 4 | 客户资源池 `#/customer-resources` | `/admin/customer-resources` | 已完成 | `GET /admin/customers/page` + `PUT /admin/customers/{id}/sales-owner` | `@super` / 真实资源池、版本冲突、审计 |
| 5 | 我的业绩 `#/sales-statistics` | `/admin/reports/sales` | 部分完成 | `GET /admin/reports/sales` | `reports.view` / 人员和时间筛选 |
| 6 | 营销推送 `#/marketing-push` | `/admin/marketing` | 待实现 | `GET/POST /admin/marketing` | `notifications.view/manage` / 客户与商品条件 |
| 7 | 推介计划 `#/customer-promotionplan` | `/admin/affiliate` | 部分完成 | `GET /admin/affiliate/*/page` | `affiliates.view/manage` / 归因佣金提现 |
| 8 | 产品订单 `#/order-list` | `/admin/orders` | 已完成 | `GET /admin/orders` | `orders.view/manage` / 状态、批量取消 |
| 9 | 续费订单 `#/renewal-order` | `/admin/orders?kind=renew` | 待实现 | `GET /admin/orders?kind=renew` | `orders.view` / URL 筛选恢复 |
| 10 | 流量包订单 `#/dcim-traffic-log` | `/admin/traffic-orders` | 待实现 | `GET /admin/traffic-orders` | `orders.view` / 资源订单字段 |
| 11 | 业务列表 `#/customer-product` | `/admin/services` | 已完成 | `GET /admin/services/page` | `services.view` / 状态页签 |
| 12 | 产品暂停请求 `#/customer-cancelreq` | `/admin/cancellations` | 已完成 | `GET/POST /admin/cancellations` | `services.view/manage` / 决策审计 |
| 13 | 交易流水 `#/business-statement` | `/admin/transactions` | 部分完成 | `GET /admin/customer-transactions` | `invoices.view` / 金额和渠道筛选 |
| 14 | 账单管理 `#/bill-management` | `/admin/invoices` | 已完成 | `GET /admin/invoices/page` | `invoices.view/manage` / 合并和取消 |
| 15 | 信用额管理 `#/credit-management` | `/admin/credit` | 待实现 | `GET/POST /admin/credit` | `credit.view/manage` / 额度审计 |
| 16 | 提现审核 `#/customer-withdrawal` | `/admin/affiliate?tab=withdrawals` | 部分完成 | `GET/POST /admin/affiliate/withdrawals` | `affiliates.view/manage` / 审核打款 |
| 17 | 发票列表 `#/invoice-audit` | `/admin/invoice-applications` | 已完成 | `GET/POST /admin/invoice-applications` | `invoice_applications.view/manage` |
| 18 | 合同列表 `#/contracts_audit` | `/admin/contracts` | 待实现 | `GET/POST /admin/contracts` | `contracts.view/manage` |
| 19 | 工单列表 `#/support-ticket` | `/admin/tickets` | 已完成 | `GET /admin/tickets` | `tickets.view/manage` / 部门范围 |
| 20 | 工单统计 `#/support-statistics` | `/admin/reports/tickets` | 待实现 | `GET /admin/reports/tickets` | `reports.view` / 状态趋势 |
| 21 | 年度收入统计 `#/annual-statistics` | `/admin/reports?tab=annual` | 部分完成 | `GET /admin/reports/income` | `reports.view` / 年份切换 |
| 22 | 新客户 `#/new-customer` | `/admin/reports?tab=new-clients` | 部分完成 | `GET /admin/reports/new-clients` | `reports.view` / 日期范围 |
| 23 | 产品收入 `#/product-revenue` | `/admin/reports?tab=products` | 部分完成 | `GET /admin/reports/product-income` | `reports.view` / 产品排行 |
| 24 | 收入排名 `#/revenue-ranking` | `/admin/reports?tab=ranking` | 部分完成 | `GET /admin/reports/customer-ranking` | `reports.view` / 范围权限 |
| 25 | 定时任务状态 `#/timing-results` | `/admin/jobs` | 待实现 | `GET /admin/jobs` | `jobs.view` / 最近运行状态 |
| 26 | 任务队列 `#/statistics-taskQueue` | `/admin/jobs/queue` | 待实现 | `GET /admin/jobs/queue` | `jobs.view/manage` / 重试幂等 |
| 27 | 数据库状态 `#/database-message` | `/admin/system/database` | 待实现 | `GET /admin/system/database` | `system.view` / 只读健康 |
| 28 | 自定义客户字段 `#/customer-custom` | `/admin/customer-fields` | 待实现 | `GET/POST/PATCH /admin/customer-fields` | `customers.manage` / 字段规则 |
| 29 | 推介设置 `#/promotion_plan` | `/admin/affiliate/settings` | 部分完成 | `GET/PUT /admin/affiliate/settings` | `affiliates.manage` / 规则版本 |
| 30 | 客户等级 `#/customer-level` | `/admin/customer-levels` | 待实现 | `GET/POST/PATCH /admin/customer-levels` | `customers.manage` |
| 31 | 客户分组与折扣 `#/customer-group` | `/admin/customer-groups` | 部分完成 | `GET/POST/PATCH /admin/customer-groups` | `customer_groups.view/manage` |
| 32 | 实名设置 `#/authentication-setting` | `/admin/certification-settings` | 部分完成 | `GET/PUT /admin/certification-settings` | `certifications.manage` |
| 33 | 货币配置 `#/currency-settings` | `/admin/currencies` | 待实现 | `GET/POST/PATCH /admin/currencies` | `settings.manage` |
| 34 | 支付接口 `#/payment-interface` | `/admin/channels?category=payment` | 已完成 | `GET/POST/PUT/DELETE /admin/channel-instances` | `@super` / 能力、密钥、启停 |
| 35 | 充值与财务 `#/general-settings/finance` | `/admin/finance-settings` | 待实现 | `GET/PUT /admin/finance-settings` | `settings.manage` / 版本冲突 |
| 36 | 优惠码 `#/promo-code` | `/admin/promo-codes` | 已完成 | `GET/POST/PATCH /admin/promo-codes` | `promos.view/manage` |
| 37 | 发票设置 `#/voucher-setting` | `/admin/invoice-settings` | 待实现 | `GET/PUT /admin/invoice-settings` | `settings.manage` |
| 38 | 合同设置 `#/contracts_setting` | `/admin/contract-settings` | 待实现 | `GET/PUT /admin/contract-settings` | `settings.manage` |
| 39 | 信用额设置 `#/credit-setting` | `/admin/credit-settings` | 待实现 | `GET/PUT /admin/credit-settings` | `settings.manage` |
| 40 | 工单部门 `#/work-order-dept` | `/admin/ticket-departments` | 已完成 | `GET/POST/PATCH/DELETE /admin/ticket-departments` | `@super` / 删除保护 |
| 41 | 工单状态 `#/work-order-status` | `/admin/ticket-statuses` | 待实现 | `GET/POST/PATCH /admin/ticket-statuses` | `tickets.manage` / 默认状态保护 |
| 42 | 工单传递 `#/work-order-rules` | `/admin/ticket-rules` | 待实现 | `GET/POST/PATCH/DELETE /admin/ticket-rules` | `tickets.manage` |
| 43 | 常规设置 `#/general-settings/general` | `/admin/settings` | 部分完成 | `GET/PUT /admin/site-settings` | `settings.view/manage` / 分区草稿 |
| 44 | 第三方登录 `#/third-login` | `/admin/channels?category=oauth` | 部分完成 | `GET/POST/PUT /admin/channel-instances` | `@super` / OAuth 能力 |
| 45 | 定时任务 `#/automatic-tasks` | `/admin/job-settings` | 待实现 | `GET/PUT /admin/job-settings` | `jobs.manage` / 到期和提醒 |
| 46 | 注册登录 `#/login-register` | `/admin/auth-settings` | 待实现 | `GET/PUT /admin/auth-settings` | `settings.manage` |
| 47 | 分组权限 `#/permissions-managment` | `/admin/access?tab=roles` | 部分完成 | `GET/POST/PATCH/DELETE /admin/roles` | `@super` / 权限矩阵 |
| 48 | 销售设置 `#/sales-management` | `/admin/sales-settings` | 待实现 | `GET/PUT /admin/sales-settings` | `sales.manage` |
| 49 | 员工管理 `#/admin-management` | `/admin/access?tab=staff` | 部分完成 | `GET/POST/PATCH /admin/staff` | `@super` / 部门范围 |
| 50 | 接口设置 `#/sms-template/sms` | `/admin/channels?category=sms` | 部分完成 | `GET/POST/PUT /admin/channel-instances` | `@super` / 测试发送 |
| 51 | 邮件模板 `#/email-list` | `/admin/notifications?channel=email` | 部分完成 | `GET/POST/PATCH /admin/notification-templates` | `notifications.view/manage` |
| 52 | 短信模板 `#/sms-template-index` | `/admin/notifications?channel=sms` | 部分完成 | `GET/POST/PATCH /admin/notification-templates` | `notifications.view/manage` |
| 53 | 发送设置 `#/sms-send-settings` | `/admin/sms-settings` | 已完成 | `GET/PUT/POST /admin/sms-settings` | `@super` / 测试短信 |
| 54 | 二次验证 `#/twice-confirm` | `/admin/security/mfa` | 待实现 | `GET/PUT /admin/security/mfa` | `settings.manage` |
| 55 | 验证码设置 `#/general-settings/captcha` | `/admin/auth-settings?tab=captcha` | 部分完成 | `GET/PUT /admin/site-settings` | `settings.manage` / 验证码流程 |
| 56 | 黑名单列表 `#/black-list` | `/admin/blacklist` | 待实现 | `GET/POST/DELETE /admin/blacklist` | `security.view/manage` |
| 57 | 数据迁移 `#/data-migration` | `/admin/migrations` | 部分完成 | `GET/POST /admin/migrations` | `migrations.view/manage` |
| 58 | 关于 `#/about` | `/admin/system/about` | 待实现 | `GET /admin/system/about` | `system.view` / 版本信息 |
| 59 | 系统升级 `#/system-message` | `/admin/system/upgrade` | 待实现 | `GET /admin/system/upgrade` | `system.manage` / 只读检查 |
| 60 | php信息 `#/php-message` | `/admin/system/runtime` | 待实现 | `GET /admin/system/runtime` | `system.view` / Go 运行信息 |
| 61 | 短信日志 `#/sms-log` | `/admin/logs/sms` | 待实现 | `GET /admin/sms-outbox` | `notifications.view` |
| 62 | 站内信日志 `#/station-letter-log` | `/admin/logs/inbox` | 待实现 | `GET /admin/inbox-outbox` | `notifications.view` |
| 63 | API日志 `#/api-log` | `/admin/logs/api` | 待实现 | `GET /admin/api-logs` | `audit.view` |
| 64 | 定时任务日志 `#/automatic-task-log` | `/admin/logs/jobs` | 待实现 | `GET /admin/job-logs` | `jobs.view` |
| 65 | 日志清理 `#/log-cleanup` | `/admin/logs/cleanup` | 待实现 | `POST /admin/logs/cleanup` | `audit.manage` / 二次确认 |
| 66 | 系统日志 `#/system-log` | `/admin/audit` | 已完成 | `GET /admin/audit` | `audit.view` / 脱敏载荷 |
| 67 | 管理员登录日志 `#/system-admin-log` | `/admin/logs/admin-login` | 待实现 | `GET /admin/admin-login-logs` | `audit.view` |
| 68 | 邮件日志 `#/email-log` | `/admin/logs/email` | 部分完成 | `GET /admin/email-outbox` | `notifications.view` / 重试 |
| 69 | 商品管理 `#/product-server` | `/admin/products` | 已完成 | `GET/POST/PATCH /admin/products` | `products.view/manage` |
| 70 | 流量包管理 `#/dcim-traffic` | `/admin/traffic-products` | 待实现 | `GET/POST/PATCH /admin/traffic-products` | `products.view/manage` |
| 71 | 通用接口 `#/server-settings` | `/admin/channels?category=provision` | 部分完成 | `GET/POST/PUT /admin/channel-instances` | `@super` / 开通能力 |
| 72 | 魔方DCIM `#/dcim` | `/admin/suppliers?kind=dcim` | 部分完成 | `GET/POST /admin/suppliers` | `products.view/manage` |
| 73 | 魔方云 `#/zjmfcloud` | `/admin/suppliers?kind=cloud` | 部分完成 | `GET/POST /admin/suppliers` | `products.view/manage` |
| 74 | 全局可配置项 `#/configurable-option` | `/admin/configurable-options` | 待实现 | `GET/POST/PATCH /admin/configurable-options` | `products.manage` |
| 75 | 商品订购设置 `#/order-product` | `/admin/order-settings` | 待实现 | `GET/PUT /admin/order-settings` | `settings.manage` |
| 76 | 基础信息 `#/base-info` | `/admin/site-settings?tab=basic` | 部分完成 | `GET/PUT /admin/site-settings` | `settings.view/manage` |
| 77 | 主题模板 `#/theme-template` | `/admin/theme-settings` | 待实现 | `GET/PUT /admin/theme-settings` | `settings.manage` |
| 78 | 官网自定义字段 `#/custom-template-fields` | `/admin/site-fields` | 待实现 | `GET/POST/PATCH /admin/site-fields` | `content.manage` |
| 79 | 导航管理 `#/menu_manage` | `/admin/navigation` | 待实现 | `GET/POST/PATCH/DELETE /admin/navigation` | `content.manage` |
| 80 | 友情链接 `#/friendly_link` | `/admin/friendly-links` | 待实现 | `GET/POST/PATCH/DELETE /admin/friendly-links` | `content.manage` |
| 81 | 文件分组 `#/service-support` | `/admin/file-groups` | 待实现 | `GET/POST/PATCH/DELETE /admin/file-groups` | `downloads.manage` |
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
| 第一批 | 设置、渠道、短信、邮件、验证码、通知 | 进行中 | 待补四尺寸截图和接口契约结果 |
| 第二批 | 客户扩展 | 进行中 | 客户资源池已完成；实名、业绩、营销和等级待补 |
| 第三批 | 业务与订单 | 待开始 | - |
| 第四批 | 财务与合同 | 待开始 | - |
| 第五批 | 工单与通知 | 待开始 | - |
| 第六批 | 报表与任务运维 | 待开始 | - |
| 第七批 | 权限、员工与销售 | 待开始 | - |
| 第八批 | 内容与官网配置 | 待开始 | - |
| 第九批 | 供应商与资源商店 | 待开始 | - |
| 第十批 | 创建与编辑页 | 待开始 | - |
