# 后台 105 页页面契约索引

运行 `node scripts/admin-page-contracts.mjs [台账路径] [旧版规范路径] [浏览器证据JSON路径]` 可复现生成 JSON 和本索引。旧规范默认使用现有本地文档；不读取环境配置或部署密钥。

[完整机器可读契约](./generated/admin-page-contracts.json) 包含旧版字段、筛选、批量动作、弹窗、校验、真实注册 API 与证据、权限登记、存储及迁移候选、状态和四尺寸验收。源码存在不等于完成；本生成器没有执行编译或浏览器，1440/1024/390/320 四尺寸及无浏览器证据的结果为未验证。可选 `docs/admin-restore-browser-evidence.json` 按精确 newRoute 和宽度绑定验收与截图，仍不自动改变整体页面完成状态。旧规范绝对路径仅用于来源引用，并非部署配置。

| # | 模块 / 页面 | 旧 hash | 新路由 | 代码状态 | 浏览器 |
|---:|---|---|---|---|---|
| 1 | [概览 / 控制台概览](#page-1) | #/login | /admin | 有对应路由源码；实现完整性未验证 | 未验证 |
| 2 | [客户 / 客户列表](#page-2) | #/customer-list | /admin/customers | 有对应路由源码；实现完整性未验证 | 未验证 |
| 3 | [客户 / 实名认证](#page-3) | #/customer-authentication | /admin/certifications、/console/certification | 未找到对应独立路由文件；实现状态未验证 | 未验证 |
| 4 | [客户 / 客户资源池](#page-4) | #/customer-resources | /admin/customer-resources | 有对应路由源码；实现完整性未验证 | 未验证 |
| 5 | [客户 / 我的业绩](#page-5) | #/sales-statistics | /admin/reports?tab=sales | 有对应路由源码；实现完整性未验证 | 未验证 |
| 6 | [客户 / 营销推送](#page-6) | #/marketing-push | /admin/marketing | 有对应路由源码；实现完整性未验证 | 未验证 |
| 7 | [客户 / 推介计划](#page-7) | #/customer-promotionplan | /admin/affiliate | 有对应路由源码；实现完整性未验证 | 未验证 |
| 8 | [业务 / 产品订单](#page-8) | #/order-list | /admin/orders | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 9 | [业务 / 续费订单](#page-9) | #/renewal-order | /admin/orders?kind=renew | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 10 | [业务 / 流量包订单](#page-10) | #/dcim-traffic-log | /admin/traffic-orders | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 11 | [业务 / 业务列表](#page-11) | #/customer-product | /admin/services | 有对应路由源码；实现完整性未验证 | 未验证 |
| 12 | [业务 / 产品暂停请求](#page-12) | #/customer-cancelreq | /admin/cancellations | 有对应路由源码；实现完整性未验证 | 未验证 |
| 13 | [财务 / 交易流水](#page-13) | #/business-statement | /admin/transactions | 未找到对应独立路由文件；实现状态未验证 | 未验证 |
| 14 | [财务 / 账单管理](#page-14) | #/bill-management | /admin/invoices | 有对应路由源码；实现完整性未验证 | 未验证 |
| 15 | [财务 / 信用额管理](#page-15) | #/credit-management | /admin/credit | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 16 | [财务 / 提现审核](#page-16) | #/customer-withdrawal | /admin/affiliate?tab=withdrawals | 有对应路由源码；实现完整性未验证 | 未验证 |
| 17 | [财务 / 发票列表](#page-17) | #/invoice-audit | /admin/invoice-applications | 有对应路由源码；实现完整性未验证 | 未验证 |
| 18 | [财务 / 合同列表](#page-18) | #/contracts_audit | /admin/contracts | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 19 | [工单 / 工单列表](#page-19) | #/support-ticket | /admin/tickets | 有对应路由源码；实现完整性未验证 | 未验证 |
| 20 | [工单 / 工单统计](#page-20) | #/support-statistics | /admin/reports?tab=tickets | 有对应路由源码；实现完整性未验证 | 未验证 |
| 21 | [功能 / 年度收入统计](#page-21) | #/annual-statistics | /admin/reports?tab=annual | 有对应路由源码；实现完整性未验证 | 未验证 |
| 22 | [功能 / 新客户](#page-22) | #/new-customer | /admin/reports?tab=new-clients | 有对应路由源码；实现完整性未验证 | 未验证 |
| 23 | [功能 / 产品收入](#page-23) | #/product-revenue | /admin/reports?tab=products | 有对应路由源码；实现完整性未验证 | 未验证 |
| 24 | [功能 / 收入排名](#page-24) | #/revenue-ranking | /admin/reports?tab=ranking | 有对应路由源码；实现完整性未验证 | 未验证 |
| 25 | [功能 / 定时任务状态](#page-25) | #/timing-results | /admin/jobs | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 26 | [功能 / 任务队列](#page-26) | #/statistics-taskQueue | /admin/jobs/queue | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 27 | [功能 / 数据库状态](#page-27) | #/database-message | /admin/system/database | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 28 | [设置 / 自定义客户字段](#page-28) | #/customer-custom | /admin/customer-fields | 有对应路由源码；实现完整性未验证 | 未验证 |
| 29 | [设置 / 推介设置](#page-29) | #/promotion_plan | /admin/affiliate/settings | 未找到对应独立路由文件；实现状态未验证 | 未验证 |
| 30 | [设置 / 客户等级](#page-30) | #/customer-level | /admin/customer-levels | 有对应路由源码；实现完整性未验证 | 未验证 |
| 31 | [设置 / 客户分组与折扣](#page-31) | #/customer-group | /admin/customer-groups、/admin/customer-groups?tab=products、/admin/customer-groups?tab=discounts | 未找到对应独立路由文件；实现状态未验证 | 未验证 |
| 32 | [设置 / 实名设置](#page-32) | #/authentication-setting | /admin/certification-settings | 有对应路由源码；实现完整性未验证 | 未验证 |
| 33 | [设置 / 货币配置](#page-33) | #/currency-settings | /admin/currencies | 有对应路由源码；实现完整性未验证 | 未验证 |
| 34 | [设置 / 支付接口](#page-34) | #/payment-interface | /admin/channels?category=payment | 有对应路由源码；实现完整性未验证 | 未验证 |
| 35 | [设置 / 充值与财务](#page-35) | #/general-settings/finance | /admin/finance-settings | 有对应路由源码；实现完整性未验证 | 未验证 |
| 36 | [设置 / 优惠码](#page-36) | #/promo-code | /admin/promo-codes | 有对应路由源码；实现完整性未验证 | 未验证 |
| 37 | [设置 / 发票设置](#page-37) | #/voucher-setting | /admin/invoice-settings | 有对应路由源码；实现完整性未验证 | 未验证 |
| 38 | [设置 / 合同设置](#page-38) | #/contracts_setting | /admin/contract-settings | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 39 | [设置 / 信用额设置](#page-39) | #/credit-setting | /admin/credit-settings | 有对应路由源码；实现完整性未验证 | 未验证 |
| 40 | [设置 / 工单部门](#page-40) | #/work-order-dept | /admin/ticket-departments | 有对应路由源码；实现完整性未验证 | 未验证 |
| 41 | [设置 / 工单状态](#page-41) | #/work-order-status | /admin/ticket-statuses | 有对应路由源码；实现完整性未验证 | 未验证 |
| 42 | [设置 / 工单传递](#page-42) | #/work-order-rules | /admin/ticket-rules | 有对应路由源码；实现完整性未验证 | 未验证 |
| 43 | [设置 / 常规设置](#page-43) | #/general-settings/general | /admin/settings | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 44 | [设置 / 第三方登录](#page-44) | #/third-login | /admin/channels?category=oauth | 有对应路由源码；实现完整性未验证 | 未验证 |
| 45 | [设置 / 定时任务](#page-45) | #/automatic-tasks | /admin/job-settings | 有对应路由源码；实现完整性未验证 | 未验证 |
| 46 | [设置 / 注册登录](#page-46) | #/login-register | /admin/auth-settings | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 47 | [设置 / 分组权限](#page-47) | #/permissions-managment | /admin/access?tab=roles | 有对应路由源码；实现完整性未验证 | 未验证 |
| 48 | [设置 / 销售设置](#page-48) | #/sales-management | /admin/sales-settings | 有对应路由源码；实现完整性未验证 | 未验证 |
| 49 | [设置 / 员工管理](#page-49) | #/admin-management | /admin/access?tab=staff | 有对应路由源码；实现完整性未验证 | 未验证 |
| 50 | [设置 / 接口设置](#page-50) | #/sms-template/sms | /admin/channels?category=sms | 有对应路由源码；实现完整性未验证 | 未验证 |
| 51 | [设置 / 邮件模板](#page-51) | #/email-list | /admin/notifications?channel=email | 有对应路由源码；实现完整性未验证 | 未验证 |
| 52 | [设置 / 短信模板](#page-52) | #/sms-template-index | /admin/notifications?channel=sms | 有对应路由源码；实现完整性未验证 | 未验证 |
| 53 | [设置 / 发送设置](#page-53) | #/sms-send-settings | /admin/sms-settings | 有对应路由源码；实现完整性未验证 | 未验证 |
| 54 | [设置 / 二次验证](#page-54) | #/twice-confirm | /admin/security/mfa | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 55 | [设置 / 验证码设置](#page-55) | #/general-settings/captcha | /admin/auth-settings?tab=captcha | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 56 | [设置 / 黑名单列表](#page-56) | #/black-list | /admin/blacklist | 有对应路由源码；实现完整性未验证 | 未验证 |
| 57 | [设置 / 数据迁移](#page-57) | #/data-migration | /admin/migrations | 有对应路由源码；实现完整性未验证 | 未验证 |
| 58 | [设置 / 关于](#page-58) | #/about | /admin/system/about | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 59 | [设置 / 系统升级](#page-59) | #/system-message | /admin/system/upgrade | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 60 | [设置 / php信息](#page-60) | #/php-message | /admin/system/runtime | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 61 | [设置 / 短信日志](#page-61) | #/sms-log | /admin/logs/sms | 有对应路由源码；实现完整性未验证 | 未验证 |
| 62 | [设置 / 站内信日志](#page-62) | #/station-letter-log | /admin/logs/inbox | 有对应路由源码；实现完整性未验证 | 未验证 |
| 63 | [设置 / API日志](#page-63) | #/api-log | /admin/logs/api | 有对应路由源码；实现完整性未验证 | 未验证 |
| 64 | [设置 / 定时任务日志](#page-64) | #/automatic-task-log | /admin/logs/jobs | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 65 | [设置 / 日志清理](#page-65) | #/log-cleanup | /admin/logs/cleanup | 有对应路由源码；实现完整性未验证 | 未验证 |
| 66 | [设置 / 系统日志](#page-66) | #/system-log | /admin/logs/system | 未找到对应独立路由文件；实现状态未验证 | 未验证 |
| 67 | [设置 / 管理员登录日志](#page-67) | #/system-admin-log | /admin/logs/admin-login | 有对应路由源码；实现完整性未验证 | 未验证 |
| 68 | [设置 / 邮件日志](#page-68) | #/email-log | /admin/logs/email | 有对应路由源码；实现完整性未验证 | 未验证 |
| 69 | [设置 / 商品管理](#page-69) | #/product-server | /admin/products | 有对应路由源码；实现完整性未验证 | 未验证 |
| 70 | [设置 / 流量包管理](#page-70) | #/dcim-traffic | /admin/traffic-products | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 71 | [设置 / 通用接口](#page-71) | #/server-settings | /admin/channels?category=provision | 有对应路由源码；实现完整性未验证 | 未验证 |
| 72 | [设置 / 魔方DCIM](#page-72) | #/dcim | /admin/suppliers?kind=dcim | 有对应路由源码；实现完整性未验证 | 未验证 |
| 73 | [设置 / 魔方云](#page-73) | #/zjmfcloud | /admin/suppliers?kind=cloud | 有对应路由源码；实现完整性未验证 | 未验证 |
| 74 | [设置 / 全局可配置项](#page-74) | #/configurable-option | /admin/configurable-options | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 75 | [设置 / 商品订购设置](#page-75) | #/order-product | /admin/order-settings | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 76 | [设置 / 基础信息](#page-76) | #/base-info | /admin/site-settings?tab=basic | 有对应路由源码；实现完整性未验证 | 未验证 |
| 77 | [设置 / 主题模板](#page-77) | #/theme-template | /admin/theme-settings | 有对应路由源码；实现完整性未验证 | 未验证 |
| 78 | [设置 / 官网自定义字段](#page-78) | #/custom-template-fields | /admin/site-fields | 有对应路由源码；实现完整性未验证 | 未验证 |
| 79 | [设置 / 导航管理](#page-79) | #/menu_manage | /admin/navigation | 有对应路由源码；实现完整性未验证 | 未验证 |
| 80 | [设置 / 友情链接](#page-80) | #/friendly_link | /admin/friendly-links | 有对应路由源码；实现完整性未验证 | 未验证 |
| 81 | [设置 / 文件分组](#page-81) | #/service-support | /admin/file-groups | 有对应路由源码；实现完整性未验证 | 未验证 |
| 82 | [设置 / 新闻列表](#page-82) | #/news-list | /admin/content?kind=news | 有对应路由源码；实现完整性未验证 | 未验证 |
| 83 | [设置 / 新闻分类](#page-83) | #/news-category | /admin/content?tab=categories | 有对应路由源码；实现完整性未验证 | 未验证 |
| 84 | [设置 / 帮助列表](#page-84) | #/help-list | /admin/content?kind=help | 有对应路由源码；实现完整性未验证 | 未验证 |
| 85 | [设置 / 帮助分类](#page-85) | #/help-category | /admin/content?tab=categories&kind=help | 有对应路由源码；实现完整性未验证 | 未验证 |
| 86 | [设置 / 基础设置](#page-86) | #/set | /admin/site-settings?tab=display | 有对应路由源码；实现完整性未验证 | 未验证 |
| 87 | [资源与商店 / 供应商管理](#page-87) | #/zjmf-api | /admin/suppliers | 有对应路由源码；实现完整性未验证 | 未验证 |
| 88 | [资源与商店 / 商品管理](#page-88) | #/commodity-list | /admin/suppliers/catalog | 未找到对应独立路由文件；实现状态未验证 | 未验证 |
| 89 | [资源与商店 / 产品管理](#page-89) | #/commodity-product | /admin/products | 有对应路由源码；实现完整性未验证 | 未验证 |
| 90 | [资源与商店 / 任务队列](#page-90) | #/commodity-taskQueue | /admin/suppliers/jobs | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 91 | [资源与商店 / 服务器列表](#page-91) | #/munual-resource | /admin/resources | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 92 | [资源与商店 / 订单列表](#page-92) | #/supplier-order-list | /admin/suppliers/orders | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 93 | [资源与商店 / 续费订单](#page-93) | #/supplier-renewal-order | /admin/suppliers/renewals | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 94 | [资源与商店 / 任务队列](#page-94) | #/task-queue | /admin/suppliers/tasks | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 95 | [资源与商店 / API设置](#page-95) | #/api-setup | /admin/suppliers/api-settings | 未找到对应独立路由文件；实现状态未验证 | 未验证 |
| 96 | [资源与商店 / 我的应用](#page-96) | #/app-store | /admin/apps | 未找到对应独立路由文件；实现状态未验证 | 未验证 |
| 97 | [资源与商店 / 应用商店](#page-97) | #/app-store/app-list | /admin/apps/store | 未找到对应独立路由文件；实现状态未验证 | 未验证 |
| 98 | [客户管理 / 添加客户](#page-98) | #/customer-add | /admin/customers/new | 有对应路由源码；实现完整性未验证 | 未验证 |
| 99 | [订单管理 / 新建订单](#page-99) | #/add-order | /admin/orders/new | 有对应路由源码；实现完整性未验证 | PASS（四尺寸） |
| 100 | [工单管理 / 新建工单](#page-100) | #/add-support-ticket | /admin/tickets/new | 有对应路由源码；实现完整性未验证 | 未验证 |
| 101 | [发票管理 / 开具发票](#page-101) | #/invoice | /admin/invoices/new | 有对应路由源码；实现完整性未验证 | 未验证 |
| 102 | [商品设置 / 添加商品分组](#page-102) | #/add-product-group | /admin/catalog-groups/new | 未找到对应独立路由文件；实现状态未验证 | 未验证 |
| 103 | [商品设置 / 编辑商品配置](#page-103) | #/edit-product | /admin/products/{id} | 有对应路由源码；实现完整性未验证 | 未验证 |
| 104 | [人员管理 / 编辑管理员](#page-104) | #/admin-edit | /admin/access/staff/{id} | 未找到对应独立路由文件；实现状态未验证 | 未验证 |
| 105 | [短信邮件 / 创建短信模板](#page-105) | #/sms-create-template | /admin/notifications/new?channel=sms | 未找到对应独立路由文件；实现状态未验证 | 未验证 |

<a id="page-1"></a>
## 1. 控制台概览

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:56)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:11)；页面：[源码](../web/src/app/admin/page.tsx:1)。JSON 中按唯一旧 hash #/login 检索。

<a id="page-2"></a>
## 2. 客户列表

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:73)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:12)；页面：[源码](../web/src/app/admin/customers/page.tsx:1)。JSON 中按唯一旧 hash #/customer-list 检索。

<a id="page-3"></a>
## 3. 实名认证

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:87)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:13)。JSON 中按唯一旧 hash #/customer-authentication 检索。

<a id="page-4"></a>
## 4. 客户资源池

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:107)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:14)；页面：[源码](../web/src/app/admin/customer-resources/page.tsx:1)。JSON 中按唯一旧 hash #/customer-resources 检索。

<a id="page-5"></a>
## 5. 我的业绩

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:128)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:15)；页面：[源码](../web/src/app/admin/reports/page.tsx:1)。JSON 中按唯一旧 hash #/sales-statistics 检索。

<a id="page-6"></a>
## 6. 营销推送

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:146)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:16)；页面：[源码](../web/src/app/admin/marketing/page.tsx:1)。JSON 中按唯一旧 hash #/marketing-push 检索。

<a id="page-7"></a>
## 7. 推介计划

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:176)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:17)；页面：[源码](../web/src/app/admin/affiliate/page.tsx:1)。JSON 中按唯一旧 hash #/customer-promotionplan 检索。

<a id="page-8"></a>
## 8. 产品订单

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:195)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:18)；页面：[源码](../web/src/app/admin/orders/page.tsx:1)。JSON 中按唯一旧 hash #/order-list 检索。

<a id="page-9"></a>
## 9. 续费订单

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:212)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:19)；页面：[源码](../web/src/app/admin/orders/page.tsx:1)。JSON 中按唯一旧 hash #/renewal-order 检索。

<a id="page-10"></a>
## 10. 流量包订单

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:228)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:20)；页面：[源码](../web/src/app/admin/traffic-orders/page.tsx:1)。JSON 中按唯一旧 hash #/dcim-traffic-log 检索。

<a id="page-11"></a>
## 11. 业务列表

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:246)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:21)；页面：[源码](../web/src/app/admin/services/page.tsx:1)。JSON 中按唯一旧 hash #/customer-product 检索。

<a id="page-12"></a>
## 12. 产品暂停请求

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:264)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:22)；页面：[源码](../web/src/app/admin/cancellations/page.tsx:1)。JSON 中按唯一旧 hash #/customer-cancelreq 检索。

<a id="page-13"></a>
## 13. 交易流水

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:287)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:23)。JSON 中按唯一旧 hash #/business-statement 检索。

<a id="page-14"></a>
## 14. 账单管理

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:310)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:24)；页面：[源码](../web/src/app/admin/invoices/page.tsx:1)。JSON 中按唯一旧 hash #/bill-management 检索。

<a id="page-15"></a>
## 15. 信用额管理

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:330)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:25)；页面：[源码](../web/src/app/admin/credit/page.tsx:1)。JSON 中按唯一旧 hash #/credit-management 检索。

<a id="page-16"></a>
## 16. 提现审核

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:355)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:26)；页面：[源码](../web/src/app/admin/affiliate/page.tsx:1)。JSON 中按唯一旧 hash #/customer-withdrawal 检索。

<a id="page-17"></a>
## 17. 发票列表

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:380)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:27)；页面：[源码](../web/src/app/admin/invoice-applications/page.tsx:1)。JSON 中按唯一旧 hash #/invoice-audit 检索。

<a id="page-18"></a>
## 18. 合同列表

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:403)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:28)；页面：[源码](../web/src/app/admin/contracts/page.tsx:1)。JSON 中按唯一旧 hash #/contracts_audit 检索。

<a id="page-19"></a>
## 19. 工单列表

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:426)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:29)；页面：[源码](../web/src/app/admin/tickets/page.tsx:1)。JSON 中按唯一旧 hash #/support-ticket 检索。

<a id="page-20"></a>
## 20. 工单统计

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:443)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:30)；页面：[源码](../web/src/app/admin/reports/page.tsx:1)。JSON 中按唯一旧 hash #/support-statistics 检索。

<a id="page-21"></a>
## 21. 年度收入统计

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:464)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:31)；页面：[源码](../web/src/app/admin/reports/page.tsx:1)。JSON 中按唯一旧 hash #/annual-statistics 检索。

<a id="page-22"></a>
## 22. 新客户

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:481)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:32)；页面：[源码](../web/src/app/admin/reports/page.tsx:1)。JSON 中按唯一旧 hash #/new-customer 检索。

<a id="page-23"></a>
## 23. 产品收入

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:497)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:33)；页面：[源码](../web/src/app/admin/reports/page.tsx:1)。JSON 中按唯一旧 hash #/product-revenue 检索。

<a id="page-24"></a>
## 24. 收入排名

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:515)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:34)；页面：[源码](../web/src/app/admin/reports/page.tsx:1)。JSON 中按唯一旧 hash #/revenue-ranking 检索。

<a id="page-25"></a>
## 25. 定时任务状态

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:530)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:35)；页面：[源码](../web/src/app/admin/jobs/page.tsx:1)。JSON 中按唯一旧 hash #/timing-results 检索。

<a id="page-26"></a>
## 26. 任务队列

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:545)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:36)；页面：[源码](../web/src/app/admin/jobs/queue/page.tsx:1)。JSON 中按唯一旧 hash #/statistics-taskQueue 检索。

<a id="page-27"></a>
## 27. 数据库状态

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:563)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:37)；页面：[源码](../web/src/app/admin/system/database/page.tsx:1)。JSON 中按唯一旧 hash #/database-message 检索。

<a id="page-28"></a>
## 28. 自定义客户字段

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:579)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:38)；页面：[源码](../web/src/app/admin/customer-fields/page.tsx:1)。JSON 中按唯一旧 hash #/customer-custom 检索。

<a id="page-29"></a>
## 29. 推介设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:605)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:39)。JSON 中按唯一旧 hash #/promotion_plan 检索。

<a id="page-30"></a>
## 30. 客户等级

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:635)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:40)；页面：[源码](../web/src/app/admin/customer-levels/page.tsx:1)。JSON 中按唯一旧 hash #/customer-level 检索。

<a id="page-31"></a>
## 31. 客户分组与折扣

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:656)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:41)。JSON 中按唯一旧 hash #/customer-group 检索。

<a id="page-32"></a>
## 32. 实名设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:683)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:42)；页面：[源码](../web/src/app/admin/certification-settings/page.tsx:1)。JSON 中按唯一旧 hash #/authentication-setting 检索。

<a id="page-33"></a>
## 33. 货币配置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:705)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:43)；页面：[源码](../web/src/app/admin/currencies/page.tsx:1)。JSON 中按唯一旧 hash #/currency-settings 检索。

<a id="page-34"></a>
## 34. 支付接口

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:725)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:44)；页面：[源码](../web/src/app/admin/channels/page.tsx:1)。JSON 中按唯一旧 hash #/payment-interface 检索。

<a id="page-35"></a>
## 35. 充值与财务

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:740)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:45)；页面：[源码](../web/src/app/admin/finance-settings/page.tsx:1)。JSON 中按唯一旧 hash #/general-settings/finance 检索。

<a id="page-36"></a>
## 36. 优惠码

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:765)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:46)；页面：[源码](../web/src/app/admin/promo-codes/page.tsx:1)。JSON 中按唯一旧 hash #/promo-code 检索。

<a id="page-37"></a>
## 37. 发票设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:783)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:47)；页面：[源码](../web/src/app/admin/invoice-settings/page.tsx:1)。JSON 中按唯一旧 hash #/voucher-setting 检索。

<a id="page-38"></a>
## 38. 合同设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:806)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:48)；页面：[源码](../web/src/app/admin/contract-settings/page.tsx:1)。JSON 中按唯一旧 hash #/contracts_setting 检索。

<a id="page-39"></a>
## 39. 信用额设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:824)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:49)；页面：[源码](../web/src/app/admin/credit-settings/page.tsx:1)。JSON 中按唯一旧 hash #/credit-setting 检索。

<a id="page-40"></a>
## 40. 工单部门

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:841)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:50)；页面：[源码](../web/src/app/admin/ticket-departments/page.tsx:1)。JSON 中按唯一旧 hash #/work-order-dept 检索。

<a id="page-41"></a>
## 41. 工单状态

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:858)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:51)；页面：[源码](../web/src/app/admin/ticket-statuses/page.tsx:1)。JSON 中按唯一旧 hash #/work-order-status 检索。

<a id="page-42"></a>
## 42. 工单传递

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:878)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:52)；页面：[源码](../web/src/app/admin/ticket-rules/page.tsx:1)。JSON 中按唯一旧 hash #/work-order-rules 检索。

<a id="page-43"></a>
## 43. 常规设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:897)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:53)；页面：[源码](../web/src/app/admin/settings/page.tsx:1)。JSON 中按唯一旧 hash #/general-settings/general 检索。

<a id="page-44"></a>
## 44. 第三方登录

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:930)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:54)；页面：[源码](../web/src/app/admin/channels/page.tsx:1)。JSON 中按唯一旧 hash #/third-login 检索。

<a id="page-45"></a>
## 45. 定时任务

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:950)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:55)；页面：[源码](../web/src/app/admin/job-settings/page.tsx:1)。JSON 中按唯一旧 hash #/automatic-tasks 检索。

<a id="page-46"></a>
## 46. 注册登录

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:980)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:56)；页面：[源码](../web/src/app/admin/auth-settings/page.tsx:1)。JSON 中按唯一旧 hash #/login-register 检索。

<a id="page-47"></a>
## 47. 分组权限

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1001)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:57)；页面：[源码](../web/src/app/admin/access/page.tsx:1)。JSON 中按唯一旧 hash #/permissions-managment 检索。

<a id="page-48"></a>
## 48. 销售设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1021)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:58)；页面：[源码](../web/src/app/admin/sales-settings/page.tsx:1)。JSON 中按唯一旧 hash #/sales-management 检索。

<a id="page-49"></a>
## 49. 员工管理

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1053)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:59)；页面：[源码](../web/src/app/admin/access/page.tsx:1)。JSON 中按唯一旧 hash #/admin-management 检索。

<a id="page-50"></a>
## 50. 接口设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1072)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:60)；页面：[源码](../web/src/app/admin/channels/page.tsx:1)。JSON 中按唯一旧 hash #/sms-template/sms 检索。

<a id="page-51"></a>
## 51. 邮件模板

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1092)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:61)；页面：[源码](../web/src/app/admin/notifications/page.tsx:1)。JSON 中按唯一旧 hash #/email-list 检索。

<a id="page-52"></a>
## 52. 短信模板

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1135)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:62)；页面：[源码](../web/src/app/admin/notifications/page.tsx:1)。JSON 中按唯一旧 hash #/sms-template-index 检索。

<a id="page-53"></a>
## 53. 发送设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1158)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:63)；页面：[源码](../web/src/app/admin/sms-settings/page.tsx:1)。JSON 中按唯一旧 hash #/sms-send-settings 检索。

<a id="page-54"></a>
## 54. 二次验证

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1194)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:64)；页面：[源码](../web/src/app/admin/security/mfa/page.tsx:1)。JSON 中按唯一旧 hash #/twice-confirm 检索。

<a id="page-55"></a>
## 55. 验证码设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1216)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:65)；页面：[源码](../web/src/app/admin/auth-settings/page.tsx:1)。JSON 中按唯一旧 hash #/general-settings/captcha 检索。

<a id="page-56"></a>
## 56. 黑名单列表

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1237)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:66)；页面：[源码](../web/src/app/admin/blacklist/page.tsx:1)。JSON 中按唯一旧 hash #/black-list 检索。

<a id="page-57"></a>
## 57. 数据迁移

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1252)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:67)；页面：[源码](../web/src/app/admin/migrations/page.tsx:1)。JSON 中按唯一旧 hash #/data-migration 检索。

<a id="page-58"></a>
## 58. 关于

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1267)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:68)；页面：[源码](../web/src/app/admin/system/about/page.tsx:1)。JSON 中按唯一旧 hash #/about 检索。

<a id="page-59"></a>
## 59. 系统升级

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1280)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:69)；页面：[源码](../web/src/app/admin/system/upgrade/page.tsx:1)。JSON 中按唯一旧 hash #/system-message 检索。

<a id="page-60"></a>
## 60. php信息

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1313)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:70)；页面：[源码](../web/src/app/admin/system/runtime/page.tsx:1)。JSON 中按唯一旧 hash #/php-message 检索。

<a id="page-61"></a>
## 61. 短信日志

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1326)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:71)；页面：[源码](../web/src/app/admin/logs/sms/page.tsx:1)。JSON 中按唯一旧 hash #/sms-log 检索。

<a id="page-62"></a>
## 62. 站内信日志

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1344)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:72)；页面：[源码](../web/src/app/admin/logs/inbox/page.tsx:1)。JSON 中按唯一旧 hash #/station-letter-log 检索。

<a id="page-63"></a>
## 63. API日志

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1362)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:73)；页面：[源码](../web/src/app/admin/logs/api/page.tsx:1)。JSON 中按唯一旧 hash #/api-log 检索。

<a id="page-64"></a>
## 64. 定时任务日志

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1380)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:74)；页面：[源码](../web/src/app/admin/logs/jobs/page.tsx:1)。JSON 中按唯一旧 hash #/automatic-task-log 检索。

<a id="page-65"></a>
## 65. 日志清理

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1398)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:75)；页面：[源码](../web/src/app/admin/logs/cleanup/page.tsx:1)。JSON 中按唯一旧 hash #/log-cleanup 检索。

<a id="page-66"></a>
## 66. 系统日志

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1419)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:76)。JSON 中按唯一旧 hash #/system-log 检索。

<a id="page-67"></a>
## 67. 管理员登录日志

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1437)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:77)；页面：[源码](../web/src/app/admin/logs/admin-login/page.tsx:1)。JSON 中按唯一旧 hash #/system-admin-log 检索。

<a id="page-68"></a>
## 68. 邮件日志

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1455)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:78)；页面：[源码](../web/src/app/admin/logs/email/page.tsx:1)。JSON 中按唯一旧 hash #/email-log 检索。

<a id="page-69"></a>
## 69. 商品管理

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1473)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:79)；页面：[源码](../web/src/app/admin/products/page.tsx:1)。JSON 中按唯一旧 hash #/product-server 检索。

<a id="page-70"></a>
## 70. 流量包管理

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1497)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:80)；页面：[源码](../web/src/app/admin/traffic-products/page.tsx:1)。JSON 中按唯一旧 hash #/dcim-traffic 检索。

<a id="page-71"></a>
## 71. 通用接口

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1521)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:81)；页面：[源码](../web/src/app/admin/channels/page.tsx:1)。JSON 中按唯一旧 hash #/server-settings 检索。

<a id="page-72"></a>
## 72. 魔方DCIM

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1544)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:82)；页面：[源码](../web/src/app/admin/suppliers/page.tsx:1)。JSON 中按唯一旧 hash #/dcim 检索。

<a id="page-73"></a>
## 73. 魔方云

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1566)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:83)；页面：[源码](../web/src/app/admin/suppliers/page.tsx:1)。JSON 中按唯一旧 hash #/zjmfcloud 检索。

<a id="page-74"></a>
## 74. 全局可配置项

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1588)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:84)；页面：[源码](../web/src/app/admin/configurable-options/page.tsx:1)。JSON 中按唯一旧 hash #/configurable-option 检索。

<a id="page-75"></a>
## 75. 商品订购设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1608)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:85)；页面：[源码](../web/src/app/admin/order-settings/page.tsx:1)。JSON 中按唯一旧 hash #/order-product 检索。

<a id="page-76"></a>
## 76. 基础信息

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1626)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:86)；页面：[源码](../web/src/app/admin/site-settings/page.tsx:1)。JSON 中按唯一旧 hash #/base-info 检索。

<a id="page-77"></a>
## 77. 主题模板

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1659)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:87)；页面：[源码](../web/src/app/admin/theme-settings/page.tsx:1)。JSON 中按唯一旧 hash #/theme-template 检索。

<a id="page-78"></a>
## 78. 官网自定义字段

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1678)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:88)；页面：[源码](../web/src/app/admin/site-fields/page.tsx:1)。JSON 中按唯一旧 hash #/custom-template-fields 检索。

<a id="page-79"></a>
## 79. 导航管理

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1696)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:89)；页面：[源码](../web/src/app/admin/navigation/page.tsx:1)。JSON 中按唯一旧 hash #/menu_manage 检索。

<a id="page-80"></a>
## 80. 友情链接

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1723)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:90)；页面：[源码](../web/src/app/admin/friendly-links/page.tsx:1)。JSON 中按唯一旧 hash #/friendly_link 检索。

<a id="page-81"></a>
## 81. 文件分组

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1743)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:91)；页面：[源码](../web/src/app/admin/file-groups/page.tsx:1)。JSON 中按唯一旧 hash #/service-support 检索。

<a id="page-82"></a>
## 82. 新闻列表

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1762)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:92)；页面：[源码](../web/src/app/admin/content/page.tsx:1)。JSON 中按唯一旧 hash #/news-list 检索。

<a id="page-83"></a>
## 83. 新闻分类

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1780)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:93)；页面：[源码](../web/src/app/admin/content/page.tsx:1)。JSON 中按唯一旧 hash #/news-category 检索。

<a id="page-84"></a>
## 84. 帮助列表

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1801)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:94)；页面：[源码](../web/src/app/admin/content/page.tsx:1)。JSON 中按唯一旧 hash #/help-list 检索。

<a id="page-85"></a>
## 85. 帮助分类

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1817)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:95)；页面：[源码](../web/src/app/admin/content/page.tsx:1)。JSON 中按唯一旧 hash #/help-category 检索。

<a id="page-86"></a>
## 86. 基础设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1838)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:96)；页面：[源码](../web/src/app/admin/site-settings/page.tsx:1)。JSON 中按唯一旧 hash #/set 检索。

<a id="page-87"></a>
## 87. 供应商管理

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1866)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:97)；页面：[源码](../web/src/app/admin/suppliers/page.tsx:1)。JSON 中按唯一旧 hash #/zjmf-api 检索。

<a id="page-88"></a>
## 88. 商品管理

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1886)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:98)。JSON 中按唯一旧 hash #/commodity-list 检索。

<a id="page-89"></a>
## 89. 产品管理

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1906)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:99)；页面：[源码](../web/src/app/admin/products/page.tsx:1)。JSON 中按唯一旧 hash #/commodity-product 检索。

<a id="page-90"></a>
## 90. 任务队列

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1926)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:100)；页面：[源码](../web/src/app/admin/suppliers/jobs/page.tsx:1)。JSON 中按唯一旧 hash #/commodity-taskQueue 检索。

<a id="page-91"></a>
## 91. 服务器列表

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1944)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:101)；页面：[源码](../web/src/app/admin/resources/page.tsx:1)。JSON 中按唯一旧 hash #/munual-resource 检索。

<a id="page-92"></a>
## 92. 订单列表

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1967)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:102)；页面：[源码](../web/src/app/admin/suppliers/orders/page.tsx:1)。JSON 中按唯一旧 hash #/supplier-order-list 检索。

<a id="page-93"></a>
## 93. 续费订单

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:1984)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:103)；页面：[源码](../web/src/app/admin/suppliers/renewals/page.tsx:1)。JSON 中按唯一旧 hash #/supplier-renewal-order 检索。

<a id="page-94"></a>
## 94. 任务队列

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2000)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:104)；页面：[源码](../web/src/app/admin/suppliers/tasks/page.tsx:1)。JSON 中按唯一旧 hash #/task-queue 检索。

<a id="page-95"></a>
## 95. API设置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2018)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:105)。JSON 中按唯一旧 hash #/api-setup 检索。

<a id="page-96"></a>
## 96. 我的应用

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2035)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:106)。JSON 中按唯一旧 hash #/app-store 检索。

<a id="page-97"></a>
## 97. 应用商店

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2048)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:107)。JSON 中按唯一旧 hash #/app-store/app-list 检索。

<a id="page-98"></a>
## 98. 添加客户

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2058)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:108)；页面：[源码](../web/src/app/admin/customers/new/page.tsx:1)。JSON 中按唯一旧 hash #/customer-add 检索。

<a id="page-99"></a>
## 99. 新建订单

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2092)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:109)；页面：[源码](../web/src/app/admin/orders/new/page.tsx:1)。JSON 中按唯一旧 hash #/add-order 检索。

<a id="page-100"></a>
## 100. 新建工单

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2125)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:110)；页面：[源码](../web/src/app/admin/tickets/new/page.tsx:1)。JSON 中按唯一旧 hash #/add-support-ticket 检索。

<a id="page-101"></a>
## 101. 开具发票

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2150)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:111)；页面：[源码](../web/src/app/admin/invoices/new/page.tsx:1)。JSON 中按唯一旧 hash #/invoice 检索。

<a id="page-102"></a>
## 102. 添加商品分组

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2158)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:112)。JSON 中按唯一旧 hash #/add-product-group 检索。

<a id="page-103"></a>
## 103. 编辑商品配置

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2182)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:113)；页面：[源码](../web/src/app/admin/products/[id]/page.tsx:1)。JSON 中按唯一旧 hash #/edit-product 检索。

<a id="page-104"></a>
## 104. 编辑管理员

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2212)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:114)。JSON 中按唯一旧 hash #/admin-edit 检索。

<a id="page-105"></a>
## 105. 创建短信模板

旧规范：[源文档](/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2245)；台账：[记录](../docs/admin-full-ui-restore-ledger.md:115)。JSON 中按唯一旧 hash #/sms-create-template 检索。
