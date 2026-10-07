# 管理员、角色与 API 权限

## 源系统依据

旧 PHP 副本的 `app/admin/controller/RbacController.php` 管理角色名称、描述、启用状态和 `auth_access`，有关联管理员的角色不能删除。`UserController.php` 为普通管理员指定单个角色，并保护自身停用；`AdminBaseController.php::checkAccess` 用控制器动作查找 `auth_rule`，超级管理员有独立豁免。`docs/frontend/14-admin-permission-matrix.md` 还记录了工单部门、销售范围和部分二次验证规则。

新系统使用独立能力名称，不复用 PHP 规则 ID，也不承诺旧接口兼容。超级管理员是受保护的账户身份，不属于可编辑角色；新建和编辑普通角色不能授予这个身份。普通管理员每次认证都从 PostgreSQL 读取当前启用角色的权限。角色撤权或停用在提交后的新请求上生效，通常不取消已进入处理的请求。schema061 的支付查询在等待网关后、补结算提交前再次验证账户/权限及客户归属，失去授权时不会补结算。

## 当前能力

`GET /api/v1/me` 返回当前身份、`permissions` 字符串数组、工单范围元数据 `ticket_scope/ticket_unassigned` 和客户范围 `customer_scope/customer_ownerless/sales_enabled`。客户权限数组为空；超级管理员返回全部业务能力，角色、管理员和销售归属管理还要求超级管理员浏览器会话。API 密钥不支持这些管理接口，也不支持提交迁移复核决定。

| 领域 | 读取 | 操作 |
| --- | --- | --- |
| 工作台 | `dashboard.view` | 无 |
| 客户/联系人 | `customers.view` | `customers.manage` |
| 客户分组/分配 | `customer_groups.view` | `customer_groups.manage` |
| 客户销售归属 | 超级管理员会话 | 超级管理员会话 |
| 优惠码 | `promos.view` | `promos.manage` |
| 产品 | `products.view` | `products.manage` |
| 内容发布 | 公开内容读取 | `content.manage` |
| 订单 | `orders.view` | `orders.manage` |
| 财务 | `invoices.view` | `invoices.manage`（人工账单）、`invoices.refund`、`balance.manage`（充值）、`payments.reconcile`（查单补结算） |
| 服务/取消申请 | `services.view` | `services.manage` |
| 工单/附件/内部备注 | `tickets.view` | `tickets.manage` |
| 预设回复 | `ticket_presets.view` | `ticket_presets.manage` |
| 通知 | `notifications.view` | `notifications.manage` |
| 报表 | `reports.view` | 无 |
| 审计 | `audit.view` | 无 |
| 迁移复核 | `migrations.view` | 受保护的超级管理员会话 |

schema063 的 35 项业务能力覆盖当前 147 条 `/api/v1/admin/*` 路由。读取和操作独立校验，操作能力必须同时包含对应读取能力（充值使用独立财务能力）。新闻与知识库的 `content.view/manage` 对应读取和修改，完整契约见 [内容管理](content-management.md)；购买目录分组、服务导航分组/商品归属、商品编辑、价格、配置及购买字段维护使用 `products.view/manage`，见 [商品维护](catalog-management.md)、[购买字段](product-purchase-fields.md) 与 [服务导航](service-navigation.md)。新管理路由没有登记权限时拒绝访问，包括超级管理员。站点设置读写同角色、管理员及销售转交一样要求超级管理员浏览器会话，API 密钥不能访问。

共用的 `/api/v1/tickets/{id}`、回复、状态和附件接口：读取跨客户工单需要 `tickets.view` 及对应部门范围，跨客户写入需要 `tickets.manage` 及对应部门范围；缺少能力时仍保留账户本人作为客户的所有权访问，越权对象返回 404。内部备注仅对授权管理员返回。工单处理人目录和分派校验仅接受已启用且具有工单操作能力的管理员或超级管理员。

其他客户接口继续检查当前账户所有权；服务、账单和订单的后台访问使用明确的管理路由。侧栏按 `/me` 权限筛选，普通管理员登录后进入首个可访问页面，没有业务权限则进入个人账户。主要写操作按能力隐藏或禁用；页面显示不是 API 授权依据。

## 管理接口

入口 `/admin/access`，包含管理员和角色权限两个视图。以下接口全部要求超级管理员浏览器会话及写请求来源校验：

| 方法和路径 | 契约 |
| --- | --- |
| `GET /api/v1/admin/permissions` | 可分配能力目录：`code/group/label` |
| `GET /api/v1/admin/roles` | 角色资料、权限、版本和关联管理员数量 |
| `POST /api/v1/admin/roles` | `name/description/active/permissions` |
| `PATCH /api/v1/admin/roles/{id}` | 同上，另需 `expected_version` |
| `DELETE /api/v1/admin/roles/{id}` | JSON `expected_version`；有关联账号时拒绝 |
| `GET /api/v1/admin/staff` | 支持 `q/limit/offset`；返回资料、角色、状态、版本和 MFA 启用状态 |
| `POST /api/v1/admin/staff` | `username/email/display_name/admin_role_id/status/password`；可选工单和客户范围字段见下文 |
| `PATCH /api/v1/admin/staff/{id}` | 同上及 `expected_version`；空密码保留原密码 |

角色名称最多 80 字符，描述最多 500 字符。普通管理员用户名为 3 到 48 位小写字母、数字、下划线或横线；密码为 8 到 72 字节，bcrypt 存储。新管理员必须选择已启用角色。已分配停用角色的管理员仍可被停用。

角色复制在前端预填新建表单，仍提交独立的新角色。普通管理员不提供物理删除，保留审计关联并通过停用阻止登录。超级管理员和客户账户不能通过 staff 接口改写，超级管理员个人密码使用账户设置维护。

管理员账号状态、角色、工单范围、客户范围、销售资格、登录名、邮箱或密码变更会撤销全部会话、API 密钥、未完成的 MFA 登录挑战和找回密码令牌；只修改显示姓名不会撤销。管理员资料版本和角色版本防止旧表单覆盖新修改，成功修改与审计在同一事务提交，审计不含密码及其哈希。

## 迁移和验收

### 客户销售范围

旧 `GetUserController.php::initialize/check/check1` 使用 `is_sale/sale_is_use/only_mine/cat_ownerless` 和客户 `sale_id`，但列表与详情对无归属客户的处理不同，`check1` 无条件放行无归属客户。新系统采用一致规则，普通管理员同时需要路由能力与客户范围；范围外对象返回 404。

staff 创建和更新接受 `customer_scope`、`customer_ownerless` 和 `sales_enabled`。`owned` 仅本人名下客户；`assigned` 包含所有非空归属客户；两者可独立授权无归属客户。`all` 包含全部客户，存储时归零额外无归属标记。新建默认 `owned/false/false`，省略更新字段保留旧值。销售资格和访问范围相互独立：取消资格不撤掉历史客户，但不再接收新分配；账号停用保留历史归属。`033_customer_sales_scope.sql` 为迁移前已有管理员保留 `all`，不自动新建销售员或分配客户。

客户列表、联系人、客户资料/密码/分组分配、订单列表/详情/备注/审核/取消及批量取消、账单列表/充值/退款、服务列表/详情/动作/人工交付、取消申请/审核统一检查客户范围，包括已完成操作的幂等重放。客户端继续按账户自身所有权授权。工作台客户/订单/应收及报表经营收入、客户排名、订单和取消数量使用查询时的客户归属，历史收入随当前归属展示，不是销售佣金历史归属账本。

工单仍使用独立部门范围，工单客户不在销售范围不影响授权部门处理。产品、优惠码、客户分组定义及其全局成员计数、通知模板/邮件队列、审计和迁移记录是显式全局能力；配置、通知和审计权限可能包含其他客户信息，不能当作受销售范围隔离的岗位权限授予。客户分组成员分配本身必须通过客户范围校验。

| 方法和路径 | 契约 |
| --- | --- |
| `GET /api/v1/admin/customers/{id}` | 需要 `customers.view` 和客户范围；返回客户摘要（账号、邮箱、状态、余额、分组、销售归属）；范围外或不存在统一返回 404 |
| `GET /api/v1/admin/customer-transactions?customer_id={id}` | 需要 `invoices.view` 和客户范围；返回该客户的支付与余额流水时间线，不返回支付凭据或原始网关响应 |
| `GET /api/v1/admin/services?customer_id={id}` | 管理端可选客户筛选；仍执行 `services.view` 和客户范围；服务列表只返回该客户资源 |
| `GET /api/v1/admin/orders?customer_id={id}` | 管理端可选客户筛选；仍执行 `orders.view` 和客户范围；订单列表只返回该客户订单 |
| `GET /api/v1/admin/invoices?customer_id={id}` | 管理端可选客户筛选；仍执行 `invoices.view` 和客户范围；账单列表只返回该客户账单 |
| `GET /api/v1/admin/tickets?customer_id={id}` | 管理端可选客户筛选；仍执行 `tickets.view` 和工单部门范围；工单列表只返回该客户工单 |
| `GET /api/v1/admin/sales-owners` | 超级管理员会话；仅启用的普通管理员、启用角色、`customers.view` 且 `sales_enabled`；返回 ID、登录名、姓名 |
| `PUT /api/v1/admin/customers/{id}/sales-owner` | 超级管理员会话；必须显式传 `sales_owner_id`（正整数或 `null`）与 `expected_version`；错误版本 409；成功返回递增 `sales_version` |

转交和审计在同一事务提交。业务事务持有按客户编号的共享 advisory 锁，转交持有对应独占锁；获得锁后用新语句复核范围，保留原有财务行锁顺序。转交后发起或排队等待中的旧归属请求不能继续修改客户；已获得共享锁的操作先完成，再转交。该协议适用于本 API，直接数据库更新不属于受保证的转交流程。转交不会撤销客户自己的登录。前端客户管理增加归属列与超级管理员调整窗口，保存后刷新版本；无法再分配的历史销售员保留显示。

2026-10-05 本批 `_test` 隔离 schema 覆盖五种客户范围组合、商业资源列表/详情/写操作拒绝、联系人无效编号及授权先于邮箱查重、批量取消逐项边界、报表金额与计数、部门独立性、销售员资格/停用/会话与 API 密钥边界、归属版本竞争、更新凭据撤销、字段省略保留、已有账号迁移、转交后的充值/退款/订单审核/服务交付/取消决定/服务动作重放，以及等待中的充值重新检查归属。完整 `go test ./...`（强制真实旧库只读演练）、`go vet ./...`、前端 lint/typecheck/build 通过。

Playwright 验证归属转交和清空、范围修改后旧会话失效及重新登录后的客户列表、普通管理员无转交控件、资料与余额保存后刷新；桌面 1440×1000、手机 390×844 弹窗截图及边界检查通过。客户列表资料、密码和余额操作改为独立弹窗。所有浏览器 fixture、随机 schema 和 3089 预览均已清理；详细证据见忽略目录 `output/playwright/sales-acceptance-20261005.md`。

### 工单部门范围

旧 `TicketDepartmentAdminModel::getAllow/check` 按管理员账号关联部门，独立于角色能力；管理员 ID 1 有特殊访问。新系统以受保护的 `super_admin` 身份实现豁免，普通管理员同时满足角色能力和账户部门范围。

staff 创建和更新接受 `ticket_scope`（`all` 或 `departments`）、`ticket_unassigned`（布尔）、`ticket_department_ids`（部门 ID 数组）。新建默认指定部门模式、空部门、无未指定工单访问；更新省略范围字段保留原值。`all` 包含所有部门和未指定工单，存储时清空显式部门列表及未指定标记。指定部门模式允许独立授予未指定工单权限，停用部门可继续授权读取历史。范围更新与成员关联、凭据撤销和审计原子提交。

`032_ticket_department_scope.sql` 只为迁移前已有管理员显式保留 `all` 范围；之后新建账号没有这一回退。部署后应复核已有普通管理员岗位。不会自动导入旧 PHP 的部门成员关系。

| 方法和路径 | 契约 |
| --- | --- |
| `GET /api/v1/admin/ticket-departments` | `tickets.view`；仅返回授权部门，包含停用部门、版本、工单数及显式关联管理员数 |
| `POST /api/v1/admin/ticket-departments` | 超级管理员会话；`name/slug/sort_order`，初始启用 |
| `PATCH /api/v1/admin/ticket-departments/{id}` | 超级管理员会话；`name/active/sort_order/expected_version`，标识不可改 |
| `DELETE /api/v1/admin/ticket-departments/{id}` | 超级管理员会话；`expected_version`，有关联工单、成员或转移历史时拒绝 |
| `GET /api/v1/admin/tickets` | `department_id=all/0/{id}`，省略表示全部授权范围；`q` 搜索主题/工单号；显式筛选未授权部门返回 403 |
| `GET /api/v1/admin/ticket-assignees` | `department_id={id}`，省略或 `0` 表示未指定；仅返回对目标部门有范围和操作能力的有效管理员 |

分派、接单、转移、内部备注及合并锁定工单并复核来源范围；分派和转移同时校验目标部门及处理人的目标范围。合并允许同一客户跨部门工单，但操作者必须有全部参与部门的访问权；任何源工单未授权则整体回滚。工单转移期间等待行锁的写请求会重新检查更新后的部门。

共用详情返回 `admin_access`；管理员本人在未授权部门提交的客户工单仍按所有权可读写，但不返回内部备注、不显示后台处理控件，其回复保持客户回复状态。工作台待处理工单和月度报表的工单/回复数量同时要求 `tickets.view` 和当前范围；报表其他经营字段使用客户销售范围，具有 `audit.view` 的操作日志是全局能力。月度工单统计保留历史合并来源，范围使用查询时的当前部门。

部门停用阻止新工单选择和向该部门分派/转移，不撤销历史工单阅读、回复或接单。有关联历史的部门应停用保留，不能删除。前端管理入口 `/admin/ticket-departments` 仅超级管理员可进入；工单列表过滤、内部备注和处理人目录使用实时 API 授权结果。

2026-10-05 本批隔离测试覆盖角色与范围组合、未指定队列、附件/备注读写、本人客户工单、目标处理人、合并回滚、范围撤销凭据、迁移默认值、部门版本与删除关联，以及确定性的转移/回复锁竞争。完整 Go 集成测试、真实旧库只读导入演练、vet、前端 lint/typecheck/build 通过。Playwright 隔离浏览器验证部门创建/停用/空部门删除、手机范围编辑、拒绝未授权详情/转移、授权后旧会话失效、重新登录后实际分派转移和普通管理员管理页重定向。390×844 与桌面截图在 `output/playwright/scope-*`；标识 HTML 正则问题已修正并重验。临时服务和随机 schema 已清理。

`031_admin_rbac.sql` 只为迁移前已有普通管理员建立“现有运营管理员”角色并显式分配原有业务能力，不给之后的新账号自动授权。数据库约束要求普通管理员关联角色，超级管理员和客户不能关联普通管理员角色。这个过渡角色需要按岗位复核；它不赋予受保护的超级管理员能力。

数据库验收仅在 `_test` 数据库随机 schema 中进行，涵盖全部管理路由拒绝无权限账号、普通角色禁止管理权限、API 密钥会话边界、角色即时撤权、共享工单读写越权、账号凭据撤销、版本并发、受保护账户和现有管理员迁移。完整 Go 集成测试、真实旧库只读隔离导入演练、`go vet`、前端 lint/typecheck/build 通过。

浏览器验收使用可清理的隔离测试 API 和 3089 临时前端；检查角色新建/更新/删除、管理员创建/修改、只读导航与工单，以及桌面和 390×844 手机弹窗。证据存于忽略目录 `output/playwright/`。开发数据库不导入这些测试管理员或业务数据。

2026-10-05 前一批本地部署已应用 `031_admin_rbac.sql`，API、worker 和 Next.js 服务均运行。`root` 登录为超级管理员，权限目录返回 26 项，认证后的 `/admin/access` 返回 200；该批验收不向开发库写入测试账号，没有旧数据导入批次。浏览器夹具首次运行触发 Go 默认测试超时，已清理该 schema，并加入早于测试总超时的清理期限；重新验收正常结束，临时端口和随机测试 schema 均已释放。

## 仍需迁移

旧管理员/角色/权限、部门授权与销售客户归属数据的导入、旧二次验证设置，以及更细的退款/开通等动作级能力仍未迁移。当前 `tickets.view` 需要同时满足账户部门范围，不再隐含全量工单访问。已有授权管理员被撤去工单权限后，其历史分派保留，可由有权限的管理员重新分派。

新接口支持 MFA，但本批管理操作未添加独立的密码/MFA 再确认步骤。供应商、真实支付、SMTP/SMS、站点设置和完整切换验收仍在重构台账中。

2026-10-05 本地开发实例已应用 `032_ticket_department_scope.sql`。API、worker、Next.js 服务运行正常，健康/就绪、root 登录、范围元数据、staff/部门/工单目录、工作台和两个管理页面均通过运行校验。部署前数据库及二进制备份位于 `bin/previous/20261005T003329Z/`。开发库保留原有超级管理员和此前已存在的一个客户账户，没有本批 `scope-*` 验收账号，也没有旧数据导入批次；旧 PHP 和 MySQL 未修改。

2026-10-05 销售范围批次已部署 `033_customer_sales_scope.sql`。API、worker 和 Next.js 正常运行；健康/就绪、root 登录和范围元数据、26 项权限目录、81 条管理路由的测试覆盖、商业列表/报表及 `/admin/customers`、`/admin/access` 通过验证。生产构建浏览器确认真实客户列表、归属窗口和新管理员范围默认值。备份在 `bin/previous/20261005T012314Z/`，包含迁移前数据库 custom dump 和三个旧二进制；已验证 dump 目录可读取，本批未演练恢复。开发库仅保留原有 root 和 acceptance-customer，两者无销售归属变更；导入批次仍为零，验收会话已注销。

后续内容、公共下载和客户文件批次新增独立能力，目前迁移042的运行目录为31项权限、129条后台路由。已有角色不会自动取得新增授权；031历史迁移保留26项原权限。客户文件还必须满足客户销售范围，转移须同时授权原/新客户，详情与验收见 [客户文件](customer-files.md)。
