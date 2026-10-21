# 后台 API 契约

本文是后台前端接入入口。全域需求与旧版取舍见[需求清单](api-requirements.md)，受控范围见 [`admin-api-requirements.json`](../../audit/admin-api-requirements.json)。接口路径、HTTP 方法和权限的完整目录由 `scripts/admin-api-inventory.mjs` 生成；本页说明共同协议、客户详情所需的确定接口及领域契约入口。**注册目录不等于全部接口的字段契约或验收完成**；尚未确定的业务不得由前端根据旧页面自行补写。

## 生成接口目录

```bash
node scripts/admin-api-inventory.mjs --legacy-root "$GOIDC_LEGACY_ROOT"
node scripts/admin-api-consumers.mjs
```

`GOIDC_LEGACY_ROOT` 由本机配置，指向只读旧源码。结果写入 `output/audit/`：

| 产物 | 用途 |
| --- | --- |
| `admin-api-catalog.md` | 当前全部后台操作的 method/path、入口包装、权限、handler/位置；旧后台显式及动态声明 |
| `admin-api-inventory.json` | 当前/旧源码路由、SHA-256、重复声明、ANY/resource/controller 与未解析 include |
| `admin-api-consumers.md` / `.json` | 当前页面、文件依赖、请求候选、未知表达式及需求归属 |

完整目录按当前工作树生成，不写死后台路由总数。旧路由是声明数；动态控制器未展开、缺失 include 未执行，不能把旧新数量相减当作待实现接口数。消费者只证明文件级候选，不证明某个 export、运行分支或页面实际调用。

## 共同协议

实际 HTTP 前缀为 `/api/v1`；下文表格省略该前缀。浏览器调用项目的 `api('/admin/...')`，不能再次加 `/api/v1`。`api<T>()` 已解开 `data`，页面获得的是 `T`。

```json
{"data":{"items":[],"total":0,"limit":25,"offset":0}}
```

失败使用 HTTP 状态和统一错误对象，不以空数组代替错误：

```json
{"error":{"code":"permission_denied","message":"没有执行该操作的权限","correlation_id":"0123456789abcdef0123456789abcdef"}}
```

`correlation_id` 可选；同时读取响应头 `X-Request-ID`，向用户显示编号便于定位，不能显示原始 SQL、凭据、供应商响应或私有存储地址。

| 约束 | 前端实现要求 |
| --- | --- |
| 认证入口 | 后台只接受管理员身份；浏览器使用 `zjmfgo_admin_session`。客户使用独立 `zjmfgo_session`，不能互换或回退 |
| 共享本人接口 | `/me`、账户安全/通知等共享 API 使用 `X-Auth-Portal: admin`；固定后台路由不能靠该头变成客户身份 |
| 登录 | `POST /auth/login` 明确提交 `portal: "admin"`；验证码/MFA 按[认证契约](../admin-rbac.md#前后台身份边界)处理 |
| CSRF | 使用现有同源代理、Cookie、`X-Requested-With: zjmfgo` 和 Origin；`web/src/lib/api.ts` 已封装，不手工复制 Cookie |
| 角色与范围 | `GET /me` 获取权限、客户与工单范围；普通管理员每个新请求重读当前授权。页面隐藏按钮不替代后端判断 |
| 401/403/404 | 分别处理登录失效、无操作权限、对象不存在或不在范围。范围外对象可能返回404，不能向用户断言记录已被删除 |
| 金额 | `_cents` 为整数分；本轮接口币种 CNY，不把展示币种配置等同多币种结算 |
| 时间 | 时间戳为 RFC3339；本页客户日志与统一交易 `from/to` 为上海自然日，`to` 包含当天。其他领域期间参数按对应契约，不能统一猜测 |
| 通用分页 | `limit` 默认25，有效1–100；越界/非法退回25。`offset` 默认0，负数退回0。响应 `{items,total,limit,offset}`；旧数组接口保留兼容，新分页页面优先用 `/page` |
| 版本冲突 | 使用本资源响应中的版本；`expected_version` 不取另一个资源的版本。409时保留草稿、重新读取并让用户核对，不静默覆盖 |
| 幂等 | 要求 `Idempotency-Key` 的操作按一次用户意图生成8–128字符的稳定 key；超时、断网和同次重试保留 key 与原请求体。变更请求体后才开始新意图 |
| 外部未知结果 | `unknown`、等待核验、人工处置不是失败后可任意重试。按对应查询/核验接口恢复，不把新 key 当作安全重试 |
| 分区状态 | 分区分别加载、错误和重试。`null`/无权限/尚未加载与真实零值分开；不得写固定0、固定绿色认证标记或假成功 |
| 文件 | 数据导出不恢复；附件/发票等业务文件；合同已退役，其他文件仍使用授权上传下载接口，不用报表 JSON 拼下载地址 |

权限完整定义见[RBAC](../admin-rbac.md)。目录中 `@super` 表示超级管理员；`sessionAuth` 等包装可能另要求浏览器会话，不能仅根据权限字符串推断允许 API 密钥。

## 客户、商品与服务的逐动作契约

- [客户管理 API](customer-api.md)：旧客户路由按批准动作映射；包含高级筛选、新建/资料/启停、字段目录、联系人、分组/销售、未付充值代建、推荐人调整及受控代登录。资料手机与登录手机号分开，客户等级筛选复用现有规则；独立 CRM、旧资料扩展暂缓，旧 API 三态不恢复。
- [服务管理 API](service-management-api.md)：第一批单服务报价、代建未付续费单及客户余额付款；第二批同客户最多20项的批量报价、清单登记、进度查询与显式执行；第三批人工价格/期限、第四批受限本地状态纠错、第五批可信人工收款和第六批服务转移均有独立协议。保持独立权限、服务/账单版本、报价窗口、统一回执及供应商pending；转移冻结双方范围及归属代次，历史财务与客户隐私独立保留；API整组已实现，UI与运行验收分别记录。
- [商品管理 API](catalog-api.md)：旧十页签逐项映射商品、定价、交付、配置、升级、购买字段、推广、业务文件和快捷链接，列出实际输入/输出、权限、版本、错误及未知结果恢复。创建/复制可选回执和其他 CAS writer 的恢复保证不同，前端不能统一猜测。

两组本轮新增实现已补齐，需求声明的 API 状态为 `implemented`；该字段只表示批准范围的接口已实现，运行验收仍以独立门禁结果为准。前端据领域契约接线，接口实现状态、页面状态、数据库/浏览器/外部及发布证据各自维护。完成必要 API 不等于所有后台 UI 已完成。

## 客户摘要

`GET /admin/customers/{id}/summary`，无请求体，成功200。入口要求 `customers.view` 且目标为 `users` 中的真实客户，并属于当前客户范围；无登录401、缺入口权限403、客户不存在或范围外404、非法路径编号400。该接口使用单个只读 Repeatable Read 事务，`as_of` 为该快照时间，不做充值、续费、通知或上游拉取。

返回：

```json
{
  "data": {
    "currency": "CNY",
    "as_of": "2026-10-20T08:00:00Z",
    "customer": {
      "id": 123,
      "username": "customer123",
      "email": "customer@example.test",
      "display_name": "客户",
      "status": "active",
      "created_at": "2026-10-01T08:00:00Z",
      "email_verified": true,
      "login_phone": "",
      "phone_verified": false,
      "profile_version": 3,
      "sales_owner_id": null,
      "sales_owner_name": null,
      "sales_version": 1,
      "group_id": null,
      "group_name": null,
      "last_login_at": null
    },
    "sections": {
      "services": {
        "available": true,
        "reason": null,
        "data": {"total": 0, "active": 0, "suspended": 0, "overdue": 0, "expiring_30_days": 0, "groups": []}
      },
      "cash": {"available": false, "reason": "permission_denied", "data": null}
    }
  }
}
```

示例省略其余 section；实际总是返回下表全部7个 section。`available=false` 时 `reason="permission_denied"`、`data=null`；有权但无业务事实返回真实零值/空数组，资料关系不存在时字段可为null。`last_login_at` 来自该客户成功认证审计的最近时间，缺记录为null，不包含IP或凭据；`login_phone/phone_verified` 对应登录账号的手机号，不能给联系人或资料中的另一个号码加验证标记。`profile_version` 用于客户修改，`sales_version` 用于销售归属，不相互替代。

| section | 读取权限 | `data` 字段与口径 |
| --- | --- | --- |
| `services` | `services.view` | `total/active/suspended/overdue/expiring_30_days` 均为服务件数；`groups:[{group_id,group_name,total,active}]` 按真实服务导航组分组，未分组的id/name为null；不归类到固定“魔方云” |
| `invoices` | `invoices.view` | `unpaid_count/unpaid_cents/overdue_count/overdue_cents`；仅status=unpaid，逾期为 `due_at<as_of`；不把历史信用债务当作未付账单，信用额度/债务分区已移除 |
| `tickets` | `tickets.view` | `total/merged_sources/open/pending/resolved/closed`；同时执行工单部门范围；total保留合并来源，四种当前状态数排除来源工单，所以四种状态数之和可小于total |
| `wallet` | `invoices.view` 或 `balance.manage` | `balance_cents/credits_cents/debits_cents`；后两者是全历史余额账本正/负变动，含赠款、退款和余额消费，不能称外部收入/支出 |
| `cash` | `invoices.view` | `gateway_receipts_cents/credit_repayment_cents/external_refunds_cents/receipts_cents/net_receipts_cents`；支付成功＋已登记信用还款为收款，已确认外部退款为退款；净收款=收款－外部退款；不含余额消费或赠款，不是利润 |
| `certification` | `certifications.view` | `profile_id/status/certification_type/version/updated_at`；无档案status=unverified，其余可空；不返回证件原文或提供方结果 |
| `referral` | `affiliates.view` | `attributed/status/referrer_customer_id/referrer_name/registered_at`；推荐人不在当前客户范围时，其id/name为null，不能通过归因跨范围查看另一个客户 |

服务逾期/即将到期仅包含active、suspended，前者 `next_due_at<as_of`，后者 `[as_of,as_of+30天)`；一次性或无到期时间不进入期限计数。所有财务累计均为快照中已经登记的全历史事实，受权限和客户归属约束，未记录的线下现金/费用不会被推断补齐。

原 `GET /admin/customers/{id}` 继续提供编辑资料；其中旧 `income_cents/expense_cents` 是余额账本汇总。新详情页使用 summary 的wallet/cash区分展示，不能再把旧字段标为实收/退款。编辑协议见[客户管理 API](customer-api.md)，字段定义规则见[客户字段契约](../customer-registration-fields-contract.md)。

## 客户详情的关联接口与写操作

客户详情的固定身份来自URL中的客户id；切页签、刷新和重试均保留它。嵌入详情的文件、账单、订单、工单创建不提供可清空的客户归属选择器。全局管理页继续允许授权范围内筛选，服务端始终重新校验。

本节的关联列表及创建客户选项统一接收精确 `customer_id`：省略保留全局范围；显式提供必须为单个纯十进制正整数，空值、重复、符号、非数字或溢出返回400 `invalid_customer`。真实客户不存在或不属于当前客户范围返回404 `not_found`，不降级为全局查询；范围内客户没有匹配记录返回空页。最终数据查询仍保留客户范围条件。客户门户沿用本人身份，不能用这些参数切换客户。

| 用途 | 方法与路径 | 权限/关键输入 |
| --- | --- | --- |
| 客户列表 | `GET /admin/customers/page` | `customers.view`；基础筛选及新增 `company/phone/qq/certification_status/level_id/custom_fields`；完整类型、字面匹配及范围规则见[客户管理 API](customer-api.md#客户列表和高级筛选) |
| 创建客户 | `POST /admin/customers` | `customers.manage`；必填稳定key、email/password；资料及额外分组/销售权限见[客户管理 API](customer-api.md#新建编辑启停和字段定义) |
| 编辑资料读取 | `GET /admin/customers/{id}` | `customers.view`；读取profile_version及原资料，缺失值保留null |
| 编辑/启停客户 | `PATCH /admin/customers/{id}` | `customers.manage`；稳定key、`expected_version`，以及至少一个 `display_name/email/status/profile` |
| 联系人 | `GET/POST /admin/customers/{id}/contacts`、`PATCH/DELETE .../{contact_id}` | 读取 `customers.view`、写 `customers.manage`；原客户端可选key/版本兼容，新UI总是显式发送；详见[客户管理 API](customer-api.md#分组销售归属和联系人) |
| 充值到钱包 | `POST /admin/customers/{id}/balance` | `balance.manage`；稳定key；`amount_cents`为正整数、`note`≤240；受财务充值开关/上下限限制 |
| 查询充值结果 | `GET /admin/customers/{id}/balance-operation` | `balance.manage`；原 `Idempotency-Key` 放请求头；按客户及key查询人工充值账本事实，不触发充值 |
| 代建未付充值账单 | `GET /admin/customers/{id}/topup-options`、`POST .../topups` | `invoices.view`与`invoices.manage`；设置version、amount_cents、必填note/key；创建时不入钱包，见[客户管理 API](customer-api.md#代建未付充值账单) |
| 推荐人调整 | `GET/PUT /admin/customers/{id}/referrer`、`GET .../referrer/options`、`GET .../referrer/history` | `customers.manage`与`affiliates.manage`；原因、关系版本、稳定key及当前范围；仅后续新建账单生效 |
| 读取客户分组成员版本 | `GET /admin/customers/{id}/group` | `customer_groups.manage`；返回客户、当前组及独立 `group_membership_version`，私有且不缓存 |
| 修改客户分组 | `PUT /admin/customers/{id}/group` | `customer_groups.manage`；显式 `group_id`、`expected_version`及稳定key，null仅表示主动移出分组 |
| 分配销售 | `PUT /admin/customers/{id}/sales-owner` | 超级管理员浏览器会话；显式 `sales_owner_id`可空，`expected_version`使用sales_version；新增稳定key重放，旧无key CAS兼容 |
| 重置密码 | `POST /admin/customers/{id}/password` | `customers.manage`；仅提交新 `password`，不读取/显示旧密码；撤销客户旧会话/API密钥及未完成MFA挑战 |
| 账单/订单/服务 | `GET /admin/invoices/page`、`/admin/orders/page`、`/admin/services/page` | 分别要求invoices/orders/services.view；均使用精确customer_id和分页，不按姓名搜索代替归属 |
| 工单 | `GET /admin/tickets/page` | `tickets.view`；customer_id、分页及部门范围，见[工单契约](../legacy-tickets.md) |
| 客户文件 | `GET /admin/customer-files` | `customer_files.view`；customer_id、q、分页；上传/编辑/删除/下载见[客户文件](../customer-files.md) |
| 创建订单 | `POST /admin/orders/quote` → `POST /admin/orders` | `orders.manage`；真实管理员actor、customer_id、报价及稳定key，见[代下单契约](../transactions.md#管理员代下单与回执)；不借客户Cookie下单 |
| 人工账单 | `/admin/invoices` 创建/编辑 | `invoices.manage`；固定客户、项目、due_at、版本和key，见[人工账单](../manual-invoices.md) |

后台订单、账单、服务、工单的原数组入口同样支持上述精确筛选；续费订单 `/admin/renewal-orders` 和供应商订单/续费 `/admin/supplier-orders`、`/admin/supplier-renewals` 也遵循该规则。工单仍另受部门范围限制；省略客户筛选时保留旧全局工单契约。

创建前读取预选客户，无请求体，成功200，返回通用分页结构；`q`继续限制结果，`limit/offset`继续有效。以下接口只返回操作所需的精简身份字段，不额外要求 `customers.view`，也不返回余额、内部备注、凭据或客户档案。

| 方法与路径 | 权限 | `items` 字段及规则 |
| --- | --- | --- |
| `GET /admin/orders/create-options?kind=customers&customer_id=123` | `orders.manage` | `id,name,email,status`；仅active客户 |
| `GET /admin/invoice-customers?customer_id=123` | `invoices.manage`及`invoices.view` | `id,username,display_name,email,status`；保留历史行为，允许读取disabled客户选项 |
| `GET /admin/tickets/create-options?kind=customers&customer_id=123` | `tickets.manage` | `id,name,email`；仅active客户，保留工单客户范围 |

合法但停用客户在订单/工单创建选项中返回空页；该读取不表示后续创建一定可提交，写入仍重新校验当前客户、部门、商品和价格。

客户更新最小请求与响应：

```http
PATCH /api/v1/admin/customers/123
Idempotency-Key: 9a03c801-8204-4fde-bae4-c9a1c981f042
Content-Type: application/json

{"status":"disabled","expected_version":3}
```

```json
{"data":{"user_id":123,"profile_version":4,"replayed":false}}
```

首次更新和重放均200；同key异请求409。未传版本为 `409 customer_version_required`，过期版本为 `409 stale_customer_profile`；key无效400。修改资料中的custom_fields还必须传正确的字段目录版本，字段定义变化返回 `stale_customer_fields`。`profile`只合并明确提交的字段；不要把未知字段、null资料对象或全页状态直接提交。

充值首次201、重放200，数据均为 `{user_id,balance_cents,replayed}`；同一客户/key的金额或去除首尾空白后的备注变化均409 `idempotency_conflict`，包括历史账本重放。原幂等范围仍是客户/key，不改成管理员/key；另一位当前有充值权限且可访问该客户的管理员可以核验同一充值事实，不能把它当作新的充值。事务在资金、设置及请求锁等待后重新验证当前管理员、角色、客户范围及实际会话/API密钥，成功写入与审计同事务提交。

### 钱包充值结果恢复

断网或5xx后，使用 `GET /admin/customers/{id}/balance-operation` 查询原key；不传请求体，不把key放URL。结果查询及其错误响应使用 `Cache-Control: private, no-store`。入口与事务均要求当前 `balance.manage`，客户不存在、不是普通客户或范围外404；无登录401、无权限403、key缺失/重复/长度非法400。查询不读取或变更当前充值开关、金额限额和余额，不产生充值、审计或回执。

查到人工充值账本返回200：

```json
{"data":{"state":"confirmed","user_id":123,"amount_cents":2000,"balance_cents":5000,"ledger_id":456,"created_at":"2026-10-20T08:00:00Z"}}
```

`balance_cents` 为该次入账保存的余额，不是当前余额，也不是充值金额。只确认 `reference_type=admin_topup` 的账本事实，在线充值不冒充人工充值。返回不含备注、原始key、支付凭据或推断出的原操作人；查询身份是当前获授权管理员，不表示其就是历史操作者。

没有已提交的匹配事实时返回200 `{"state":"unconfirmed","user_id":123}`。查询与同客户/key的在途人工充值串行，仍不能把未确认解释为“此前从未执行”或允许换key：原请求可能尚未到达或仍在其他阶段。前端核对confirmed结果的客户和金额，保留原body/key及未确认状态；核验后如需重放仍使用原key及原请求体，成功后刷新wallet与交易。同一用户主动再次充值是新的意图，才用新key。批量动作、退款、支付核验各自遵循领域契约，不把本表模式套到未要求幂等key的所有PUT/PATCH。

### 信用业务入口已退役

`/admin/credit` 及消费/还款列表等信用专属 API 已全部退出，不再提供客户详情的历史查询、额度管理、还款或原key恢复。通过原入口鉴权后统一410；具体边界见[下线契约](../customer-credit-portal-contract.md)及下文退役说明。客户摘要或统一交易保留的财务兼容字段不能用于重建信用门户；summary不再返回`credit` section及额度/债务信息，新界面不再提供信用分区或付款入口。

### 客户分组的版本与结果恢复

`GET /admin/customers/{id}/group` 返回 `{customer_id,group_id,group_membership_version}`。读取与写入都要求 `customer_groups.manage` 和当前客户范围，不额外要求 `customers.view`；读取的认证错误也不缓存。`PUT` 必须明确提交 `group_id`（正整数或null）、`expected_version`及单个8–128字符的 `Idempotency-Key`。漏传group_id为400 `invalid_group`，漏传成员版本为409 `group_membership_version_required`，过期版本409 `version_conflict`，无效/重复key为400。成功200，返回上述三个字段及 `replayed`。

成员版本独立于客户资料、销售归属和分组规则版本，清空后仍保留。迁移183为既有客户初始化1，不推断历史修改次数；此后成员新增、移出或换组由数据库触发器递增，同组无变化不递增。每个新意图仍只记一次审计和统一回执。`expected_version`取成员读取结果，不取分组规则的version或sales_version。

回执按当前管理员、客户和key隔离，绑定原group_id及expected_version。相同body/key可恢复未知提交，返回当时结果，即使当前已被后续操作改组；不能拿新的GET结果代替原请求，不能因当前组恰好相同就认定原请求成功。同key异body为409 `idempotency_conflict`。重放仍校验当前权限、客户范围和原实际凭据，审计、成员变化和回执同事务；不改历史订单和服务合同价。分组写锁在成员、规则及版本行锁前取得，版本元数据独立于users，避免与下单的客户行共享锁互相等待。先停旧成员writer，再配套部署迁移、API和Web，旧无版本写接口不再接受。

### 销售归属的会话与重放

销售分配继续仅允许真实、启用的超级管理员后台浏览器会话；普通管理员、API密钥及未绑定真实会话的内部调用均不能借用该能力。取得归属、客户、人员或回执锁后重新验证实际管理员和会话，审计/回执后再次核对自然过期；管理员账号先于凭据加锁，与换密顺序一致。目标销售仍须为启用、有客户读取权限且可销售的普通管理员。

`Idempotency-Key`可选，提供时必须为单值8–128字符，不能含首尾空白；新前端始终提供。新请求200 `{customer_id,sales_owner_id,sales_version,replayed:false}`，原body/key重放返回原版本及 `replayed:true`，不重复分配或审计；同一管理员和客户下，同key异归属或异原版本冲突。回执按管理员及客户隔离，不创建平行回执表。旧无key请求保持原CAS响应和冲突行为，只能读取当前状态人工核对，不具备历史结果恢复能力。未知结果保留原body/key，不自动换键或更新expected_version。

客户列表与资源池共用销售归属组件，待确认原body/key按实际管理员、客户和动作保存在当前浏览器会话，可跨这两个入口恢复。资料与分组各自保存请求；恢复期间不能改为另一意图。确认响应必须匹配客户、原目标、版本及replayed标记，缺失或不匹配继续保留原请求；401/403/404不表示旧请求必然未执行。切换管理员时重挂载读取与动作子树，旧异步响应不能覆盖新身份界面。这些组件行为另以测试核验，不替代真实浏览器。

### 推广列表的客户上下文

`GET /admin/affiliate/accounts/page`、`/attributions/page`、`/withdrawals/page` 仍要求 `affiliates.view`，新增严格 `customer_id`，不额外要求customers.view。非法查询编码不允许被丢弃后退回全局；客户非法400，不存在、非客户或范围外404。原search/status、items/total/pending_count及分页行为保留。账户和提现的customer_id指推广账户所属客户；提现pending_count忽略search/status，保留原徽标含义，但受精确客户及当前范围限制。

归因可在指定customer_id时另传单个 `customer_role=promoter|referred|either`，默认either，分别表示推荐人、被推荐人、任一端；其他列表或未指定客户时提供该参数返回400。被推荐客户仍须在当前范围；推荐人越界时account_id、affiliate_user_id、affiliate_username及link_id均为null，搜索也不能匹配其姓名。可见推荐人不能据此读取范围外被推荐客户的记录。

客户详情另提供 `GET /admin/customers/{id}/affiliate/page`，保持原 `customers.view` 权限，返回 `{items,total,limit,offset}`；旧 `/affiliate` 继续返回活动数组。两者包含客户作为推广人或被推荐人的既有活动，佣金自身金额保留，但关联账单客户越界时invoice_no为空。两种入口权限互不隐含。权限、客户核验、总数及数据均在同一只读快照，最终查询保留客户范围，空页不扩大查询。

推荐人写入使用专属 `/admin/customers/{id}/referrer`，不是 generic 客户 PATCH。它要求 `customers.manage` 与 `affiliates.manage`，显式目标客户ID或null、关系版本、原因及稳定key；候选和历史按当前范围脱敏。`effective_scope=future_invoices_only`，不回改旧未付/已付账单、佣金、提现或退款。迁移187冻结账单归因，已有资金证据优先、冲突阻断；先停旧 API、worker、导入等 writer 后成套升级数据库与配套程序，不能混跑。完整请求、回执/no-op及历史协议见[客户管理 API](customer-api.md#推荐人调整和历史归因)。

## 统一交易分页

`GET /admin/transactions/page` 与 `GET /admin/transactions/summary`，均要求 `invoices.view`，限制客户范围。新详情页替代旧数组 `/admin/customer-transactions`；旧数组继续兼容，不据此假定它含全部新增信用事件。

共同筛选：`customer_id` 正整数、`q`≤120、`kind=topup|product|renew|other`、`direction=income|expense|neutral`、`channel`≤100、`from/to=YYYY-MM-DD`（上海、包含to当天）、`min_cents/max_cents`非负整数且最小≤最大；分页另传limit/offset。筛选无效400 `invalid_transaction_filter`。不指定customer_id时查询授权范围；范围外精确筛选返回空页，不暴露外部客户。summary使用同样过滤，不能把分页中当前一页求和当全部合计。

分页data为 `{items,total,limit,offset}`。每行字段：`event_key/source/id/user_id/invoice_id/invoice_no/occurred_at/channel/amount_cents/direction/transaction_no/description/kind/balance_after/customer_name/customer_username/sales_owner_name/currency`。`event_key`是跨来源稳定唯一键，不能单用id当React key；金额始终非负，方向决定展示符号；balance_after可空。

| source | 方向与渠道 | 含义 |
| --- | --- | --- |
| `payment` | income，具体支付提供方 | 已成功外部支付，含在线充值；统计实收一次 |
| `manual_receipt` | income，manual_offline | 已核实线下全额收款，显示原流水号；与网关收入分列，现金只计一次 |
| `external_refund` | expense，退款提供方 | 已确认实际外部退款；非钱包退款 |
| `ledger` | income/expense/neutral，balance/admin_topup/gateway_topup | 钱包正负变动；gateway_topup为neutral，避免把同次外部收款再记收入 |
| `credit_consumption` | neutral，credit | 信用支付账单事实；非新现金，按invoice_id稳定标识，不再重复出现invoice fallback |
| `credit_repayment` | income，credit_repayment | 已登记的真实信用还款，时间为received_at；多次还款分别保留；不返回私密收据/备注 |
| `invoice` | neutral，unrecorded等 | 历史已付但无上述支付/账本/信用消费证据的兼容项，不能推断为外部收款 |

summary返回整数分字段 `gateway_income_cents/manual_income_cents/manual_refund_cents/net_manual_cents/external_refund_cents/net_gateway_cents/credit_repayment_cents/cash_income_cents/net_cash_cents` 及currency。`net_gateway_cents=gateway_income_cents-(external_refund_cents-manual_refund_cents)`，保持既有网关口径并排除人工退款；`net_manual_cents=manual_income_cents-manual_refund_cents`。`cash_income_cents=gateway_income_cents+manual_income_cents+credit_repayment_cents`，`net_cash_cents=cash_income_cents-external_refund_cents`。人工线下收退款单列，不冒充网关收入或支出；钱包、消费和信用支付均不加入该现金合计。

## 客户操作日志与通知历史

以下既有接口新增精确 `customer_id`，不能用q、邮箱或手机号模糊匹配代替：

| 接口 | 权限 | 筛选与返回 |
| --- | --- | --- |
| `GET /admin/logs` | `audit.view` | customer_id、q、kind（省略或system）、from/to、limit/offset；返回操作元数据 `{id,created_at,action,entity_type,entity_id,actor_id,actor_name}`，不返回任意payload |
| `GET /admin/logs/email` | `notifications.view` | customer_id、status、recipient、subject、q、username、ticket_id、from/to、limit/offset |
| `GET /admin/logs/sms` | `notifications.view` | customer_id、status、kind、phone、q、username、ticket_id、from/to、limit/offset |
| `GET /admin/logs/inbox` | `notifications.view` | customer_id、status、subject、q、username、from/to、limit/offset |

传customer_id时必须为**单个纯十进制正整数**；空值、重复、负数、非数字或溢出400 `invalid_customer`；不存在或越权404。省略时保留既有全局契约；前端固定客户页不得省略。审计按业务实体的客户归属关联，actor可能是管理员，不能仅按actor=customer过滤。工单/回复/附件等记录另要求tickets.view和部门范围。未定义实体归属的历史审计不会猜测匹配；列表为空不代表客户从未发生任何活动。

邮件/短信按不可变投递事件或工单关系定位，改过邮箱/手机号的客户仍能读到已归属自己的历史；仅地址相同、未建立客户关联的旧记录不会误归属。通知中的工单记录继续执行tickets.view/部门限制。所有结果均为 `{items,total,limit,offset}`；邮件/短信行包括id、created_at、recipient、status、attempts、max_attempts、last_error、sent_at、dead_letter_at、username及ticket_id/event_key/event_kind/recipient_kind等可空来源；邮件另有subject，短信另有kind/phone/content，content只给加密标记。站内信行是id、created_at、subject、status、user_id、username、read_at；这些日志不返回解密正文、原始提供方响应或客户凭据。

邮件/短信status为pending/sending/sent/expired/dead_letter；站内信为sent/read/failed。日期按上海自然日过滤，反向日期和非法日期返回400 `invalid_log_date`。来源不可关联、无权限与真实空页分别处理，不以日志为空将通知写成“从未发送”。统一新渠道投递状态、未知结果与人工核验见[通知架构](../notification-architecture.md)。

### 统一渠道业务通知历史

`GET /admin/notification-deliveries/page`，要求 `notifications.view` 和管理员浏览器会话；这是独立读取接口，客户详情无需加载整套通知规则/模板配置。查询参数如下：

| 参数 | 约束 |
| --- | --- |
| `customer_id` | 可选；出现时为单个纯十进制正整数，严格校验及404范围语义同上；固定客户页必须传 |
| `event_kind` | 当前通知事件目录中的完整kind，可从 `/admin/notification-center` 的events获取；当前目录覆盖11类下单/付款/服务转换/期限事件；未知非空kind为400 `invalid_notification_event` |
| `channel` | email/sms/inbox/telegram/web_push；对外email对应内部mail；非法值（含mail）为400 `invalid_notification_filter` |
| `status` | pending/sending/accepted/failed/unknown/skipped/archived；这是投递状态，accepted不等于客户已读；非法值（含sent/dead_letter）为400 `invalid_notification_filter` |
| `from/to` | 按投递created_at的上海自然日，to含当天；反向/非法日期为400 `invalid_notification_filter` |
| `limit/offset` | HTTP通用分页：limit默认25、最大100，超界回退25；offset默认0、负数回退0；总数与items在同一只读快照计算 |

`event_kind/channel/status/from/to` 均去除首尾空白，空值表示不筛选；这些参数重复为400 `invalid_notification_filter`，`customer_id` 重复仍为400 `invalid_customer`。无有效后台浏览器会话为401 `unauthorized`，携带Bearer凭据为403 `session_required`，缺少权限为403 `permission_denied`。

成功200，data是 `{items,total,limit,offset}`，items按投递id倒序。行字段为 `id/event_id/user_id/event_kind/channel/instance_id/status/code/attempts/provider_message_id/created_at/expires_at`。只返回投递元数据和安全错误code，不返回target、subject、body、payload、原始收据或密钥。

邮件/短信按关联旧outbox的当前状态投影：sent映射accepted，pending/sending保留，failed/dead_letter映射failed，其他旧状态映射skipped；旧记录已清理为archived，code=legacy_log_removed。筛选在投影后执行，避免显示状态与筛选状态不一致。未知投递不是重发许可，不能为该只读列表擅自添加“重新发送”。

本接口仅查询统一业务事件目录，覆盖其Telegram、Web Push和兼容邮件/短信/站内信投递。工单、营销及未关联的历史记录继续读各自日志，不将此页空结果解释为客户所有渠道从未发送。权限、客户归属、count/items在同一Repeatable Read快照内核对，不修改偏好、队列或渠道配置。

## 管理员设置服务自动续费

`PATCH /admin/services/{id}/auto-renew`，权限 `services.manage` 和客户范围，浏览器会话或受支持管理员API密钥均须仍有效。请求 `{"enabled":true}`；enabled必需且必须是布尔值，缺失/null为400 `invalid_auto_renew`，错误类型由JSON解码拒绝。成功200 `{"data":{"service_id":456,"auto_renew":true}}`。这是设置目标布尔值的同步操作，当前协议不要求expected_version或幂等key；请求重复可重复记录审计，不宣称资金副作用恰好一次。

该接口只修改续费意图，不立即扣款或调用供应商。一次性服务拒绝 `nonrenewable_service`，非active/suspended拒绝 `invalid_auto_renew_state`，本地续费退款hold、未复核资源或不支持续费的供应商按领域错误拒绝。服务等待锁期间撤权、会话/密钥撤销、客户范围变化必须拒绝，不能复用HTTP进入时的缓存授权；服务归属并发变化可返回 `service_owner_changed`，应重新读取。

前端成功后刷新服务详情，后续自动续费/上游回读仍按[交易与自动续费](../transactions.md)和[供应商协议](../supplier-protocol.md)执行。管理员代续费或批量立即续费不能通过改Cookie调用客户接口。单服务与同客户批量代续费、第三批价格/期限及后续状态/收款/归属批次见[服务管理契约](service-management-api.md)，不在本自动续费开关接口范围。

批量入口为 `POST /admin/service-renewal-batch-quotes`、`POST /admin/service-renewal-batches`、`GET /admin/service-renewal-batches/{batch_id}`、`POST /admin/service-renewal-batches/{batch_id}/execute`。报价只读，登记和查询均不建单或扣款；执行按冻结清单顺序处理明确子集，单项成功回执跨attempt复用。认证/范围变化及未知提交停止后续项目；未确认使用原execute key恢复，确定失败经管理员核对后用新execute key显式重试。只原actor可访问，create与pay_balance分别要求 `services.renew`、`services.renew.pay_balance`，并同时满足服务/账单读取与客户范围。完整字段、状态及协调会话容量见[批量契约](service-management-api.md#第二批同客户批量代续费)；UI与运行验收另记。

第三批入口及字段见[人工价格与客户期限](service-management-api.md#第三批人工价格与客户期限)：`GET /admin/services/{id}/management`、`POST /renewal-pricing/preview`、`PUT /renewal-pricing`、`POST /fixed-renewal-quotes`、`POST /fixed-renewals`、`POST /term-adjustment-quotes`、`POST /term-adjustments`（后六个均续接同一服务路径）。长期定价、单次最终应付与客户期限分权、预览、幂等及审计；原优惠计数保留，供应商实际期限只观测，不把本地调整当作远端执行。188/189须停旧writer成套发布，UI及本批运行证据另记。

第四批 `POST /admin/services/{id}/status-correction-quotes` 与 `POST /admin/services/{id}/status-corrections` 要求独立 `services.status.correct` 及读取权限。仅依据最新已完成暂停/恢复任务及原审计纠正manual/simulation的active↔suspended，不开放远端模式或终态复活；报价、原凭据、版本、统一回执及副作用边界见[受限本地状态纠错](service-management-api.md#第四批受限本地状态纠错)。

## 代登录与长期凭据

显式代登录沿用[身份边界](../admin-rbac.md#前后台身份边界)，真实actor与被代登录客户分开保留。代登录会话调用 `POST /account/api-keys` 一律403 `impersonation_credential_forbidden`，不创建key、不增加签发审计；绑定查询故障停止签发。判断只针对本次选中的客户会话绑定，不能因为浏览器同时有后台Cookie就禁用普通客户的独立登录。客户自身及管理员个人的正常密钥管理继续复用各自portal。

## 资源与商品读取

商品写入与旧页签的完整字段、目录事务授权及创建/复制恢复见[商品管理 API](catalog-api.md)。下文只保留资源读取、分组分页及配置备注的关联入口，不能代替该领域契约。

`GET /admin/resources` 要求 `resources.view`，返回 `{items,total,limit,offset,suppliers}`。除原有 `q/ip/supplier_id/status`，新增精确 `customer_id/product_id`。customer_id遵循本页共同规则；product_id显式空、重复、非纯正十进制或溢出400 `invalid_product`，合法但无匹配商品返回空页。分页数据和计数处于同一快照。

资源项新增 `product_id,product_name,next_due_at`，原有 `id,name,domain,status,username,upstream_id,resource_info,user_id,customer_name,supplier_id,supplier_name` 保留；resource_info仅返回已有白名单。到期时间可为null。suppliers仅包含当前客户范围及所选客户/商品所涉及的供应商，不根据搜索、IP或状态进一步缩减。该资源模型是已登记服务，不能当作独立库存池或完整成本台账。

`GET /admin/catalog-groups/{kind}/page` 要求 `products.view`，kind为 `section/category`；返回通用分页结构，按sort_order、id升序。支持 `q`（名称/标识/描述，最多120字符）、`active=true|false`、`limit/offset`；category还支持精确 `section_id`。重复筛选或无效active返回400 `invalid_catalog_filter`，无效/空/重复section_id或在section查询中使用section_id返回400 `invalid_section`。分组类型无效400 `invalid_catalog_group`。不传筛选包含停用分组；合法父分组无结果返回空页。

items保留原数组接口的分组字段（含id、name、slug、description、active、sort_order、version、member_count）；category另含section_id、section_name、section_active。原 `GET /admin/catalog-groups/{kind}` 继续返回完整数组。

商品配置组创建/修改输入新增可选 `notes`，最多5000字符；省略或null保留原备注，显式空字符串清空，新建省略为空。仅后台商品投影返回notes，公开商品和订单创建商品选项不返回。全局配置定义也保存备注，更新省略时按slug保留；分配同步、复制和脱离规则见[商品配置](../product-configuration.md)。仍需原expected_revision/版本及写权限，备注不能绕过共享组/供应商配置保护。

## 独立人工库存

原 `/admin/resources` 保留已购服务视图。独立库存以[人工库存契约](../domain/manual-resources.md)为准，以下路径均省略 `/api/v1`：

| 操作 | 方法与路径 | 权限与输入 |
| --- | --- | --- |
| 库存分页 | `GET /admin/manual-resources` | `resources.view`；`q/status/binding/limit/offset` |
| 库存详情 | `GET /admin/manual-resources/{id}` | 同上，范围外404 |
| 新建库存 | `POST /admin/manual-resources` | `resources.view/manage`、全客户范围、必填稳定key |
| 修改/停用/退役 | `PATCH /admin/manual-resources/{id}` | 同上管理权限、资源范围、`expected_version`、稳定key |
| 显式释放 | `POST /admin/manual-resources/{id}/release` | 同上管理权限、资源范围、`expected_version,note`、稳定key；仅已终止人工服务 |
| 开通绑定 | `POST /admin/services/{id}/delivery` | 原服务交付权限及资源管理权限，原任务与服务/库存版本，见下文 |

分页默认25、offset0；该新接口严格拒绝非法分页、未知或重复参数，不沿用旧列表的静默回退。`q`最多120字符，按名称、地址、上游标识和提供方匹配；`status`为available/disabled/retired，`binding`为bound/unbound；省略包含全部有权记录。items与total使用同一只读快照，按ID倒序。未分配库存仅全客户范围管理员可见；已分配项按实际客户范围可见。

创建字段：`name`必填，`resource_type`默认server，另可提供`provider,region,address,upstream_id,cost_cents,upstream_expires_at,status,notes,username,password`。name最多120字符、resource_type40、provider/region各120、address/upstream_id各256、notes4000；username最多256字节、password4096字节。成本为CNY整数分，默认0，有效0–9000000000000000；上游期限为2000–2200年的RFC3339或空，独立于客户服务期限。status默认available，retired不可恢复；address为单个IP/主机名文本、无内嵌空白，不能据其推断上游能力。修改为局部PATCH，省略或null字段保留，空字符串清除可空文本或上游期限；`expected_version`必填。已绑定时不能修改resource_type/address/upstream_id/username/password或库存状态，先完成原服务终止及显式释放。

列表和详情包含上述公开元数据及`id,currency,version,created_at,updated_at,has_credentials,binding_id,service_id,customer_id,bound_at`；无绑定的关联字段为null。账号和密码不返回。创建成功201，原请求重放200；修改与释放成功200，返回`id,version,replayed`等本次结果。首次创建、修改与释放均要求单值8–128字符的`Idempotency-Key`，重放仍核对当前权限、范围和真实凭据；同key异请求返回`idempotency_conflict`，不新增平行回执表。审计失败不留下库存、占用或回执。

交付请求为`{job_id,manual_resource_id,resource_expected_version,expected_version,note}`；`expected_version`取服务版本，另一个取库存版本。库存负责提供实际资源标识与账号，选库存时不同时传`upstream_id/username`。成功后原子写占用、开通服务、任务、通知及审计，密码按服务重新加密，不写入任务或审计。未选择库存的旧手工交付继续兼容。服务终止不自动释放库存，release的note为3–2000字符，释放不改服务状态或抹除交付历史。服务本人凭据读取继续使用已有授权接口，不提供库存密码GET。

主要错误：400 `invalid_manual_resource`/`invalid_manual_resource_query`/`invalid_manual_resource_filter`/`invalid_manual_resource_release`/`invalid_idempotency_key`；401凭据失效；403权限不足；404对象不存在或范围外；409 `manual_resource_version_required`/`manual_resource_version_conflict`/`manual_resource_binding_changed`/`manual_resource_bound_identity`/`manual_resource_bound_status`/`manual_resource_retired`/`manual_resource_unavailable`/`manual_resource_identity_required`/`manual_resource_credentials_invalid`/`manual_resource_not_bound`/`manual_resource_release_requires_termination`/`manual_resource_release_pending_job`，以及绑定的`stale_service`/`order_not_deliverable`及原交付状态错误；503 `manual_resource_encryption_unconfigured`。未知结果保持原key/body，冲突不静默更新版本。新增库存UI及人工交付选择器尚未接入。

## 工单附件上传恢复

`POST /tickets/{id}/attachments` 使用multipart字段 `file`（兼容 `attachment`），保留类型和10 MiB限制。新前端应在首次上传前保存8–128字符的 `Idempotency-Key`，并保存文件名、类型、大小和SHA-256对应的原文件意图。后台共享工单接口带 `X-Auth-Portal: admin`；上传仍要求既有工单写权限、部门及当前凭据复核。

带key上传先持久化意图，再写文件；最终附件、审计和成功回执同事务提交。同actor/key的文件或工单不同返回409 `idempotency_conflict`；首次成功201，成功重放200并返回原附件，不再次Put、插入或审计。成功响应保留 `id,ticket_id,uploader_id,filename,mime_type,size_bytes,sha256,created_at`，另含 `state:"confirmed",replayed`。没有key的旧客户端继续兼容，但不具备本协议的恢复保证。管理员有key/无key上传统一复核工单部门及真实客户范围；保留历史管理员归属工单的部门处理规则。

结果查询使用 `GET /tickets/{id}/attachment-upload`，同一个 `Idempotency-Key` 放请求头，不进入URL，返回private/no-store：

```json
{"data":{"state":"confirmed","attachment":{"id":456,"ticket_id":123,"filename":"evidence.txt","mime_type":"text/plain","size_bytes":12,"sha256":"...","uploader_id":7,"created_at":"2026-10-20T08:00:00Z"}}}
```

仅有意图而没有已提交业务回执时，返回 `{state:"unconfirmed"}`；此时同key上传409 `attachment_upload_unconfirmed`，不得换key或自动重传。未知key、其他actor或其他工单的key返回404；查询仍验证当前工单可读权限和凭据，关单后可查已确认结果。确认成功表示业务上传曾提交，不证明附件仍未被删除、移动或能从外部存储下载，后续访问继续走原授权下载。

断网、5xx和明确的上传未知错误都先查原key；查不到回执不证明可以安全重试。413/415等预约前的确定输入错误可修正文件后重新提交；预约后的审计回滚、外部Put超时、进程退出或提交确认丢失保守保留未确认意图，人工核验原存储及journal，不能用SHA相同推定是此次上传。对象路径、存储profile和原始外部错误不返回。工单/回复创建成功与附件未确认必须分别展示；当前UI尚未接入此恢复协议。

## 商品与人员业务收款排名

`GET /admin/reports/product-ranking`、`GET /admin/reports/staff-ranking` 要求 `reports.view`，按当前管理员客户范围统计；事务使用只读 Repeatable Read 同快照复核授权和结果。控制台原有排名组件仍为占位，API 完成不表示 UI 已接入。

只接受下列查询参数，每项最多出现一次；未知参数、重复值或编码异常返回 `400 invalid_ranking_query`。

| 参数 | 规则 |
| --- | --- |
| `month` | 默认当前上海月份；严格 `YYYY-MM`，年份2000至当前上海年+1；非法为 `400 invalid_report_period` |
| `limit` | 默认5，严格1–20；空值、非法或越界为 `400 invalid_ranking_limit` |
| `format` | 省略或 `json`；其他为 `400 invalid_report_format`，不提供导出 |

响应为 `{items,total,month,limit,currency:"CNY",timezone:"Asia/Shanghai",amount_basis,activity_basis,refund_basis,attribution_basis?}`。`total` 是截断前全部候选组数，`items` 只有前 `limit`，无 offset。金额均为整数分。

共同 `amount_basis=business_receipts_including_balance_and_actual_credit_repayments`：非信用业务付款（包含余额消费）与历史信用实际回款，排除充值；不能标为外部现金、净销售额、提成或利润。`refund_basis=confirmed_external_refunds_not_deducted_balance_refunded_invoices_excluded`：已确认外部退款不从本成交指标扣减，纯余额退款账单排除，与现有产品/销售报表一致；真实退款另外进入年度现金支出。

| 接口 | `items` 字段 | 口径及排序 |
| --- | --- | --- |
| 商品 | `product_id,name,new_order_amount_cents,new_order_count,renewal_amount_cents,renewal_order_count,total_amount_cents,new_quantity,renewal_quantity` | 复用完整订单行折后分摊后汇总；非信用新购按paid_at、信用按实际received_at，归属新购/续费，不囊括全部手工/混合账单或升级。件数与订单数、续费项目数与账单数独立；免费成交可保留零金额候选。按total_amount_cents降序、product_id升序 |
| 人员 | `sales_owner_id,sales_owner_name,amount_cents,invoice_count,customer_count,purchase_amount_cents,renewal_amount_cents,upgrade_amount_cents,other_amount_cents` | 期间按实际业务收款；四分类之和等于amount_cents，账单与客户distinct。按查询时客户sales_owner_id归属，变更归属会移动历史排名；未分配及无正收款人员排除。按amount_cents降序、sales_owner_id升序；不估算或确认提成 |

商品 `activity_basis=new_purchase_and_renewal_separate_units_and_distinct_orders`；人员 `activity_basis=purchase_renewal_upgrade_and_other_business`、`attribution_basis=current_customer_sales_owner_unassigned_excluded`。客户原 `/admin/reports/customer-ranking` 仍是独立的全历史余额流水前十名，不能将其期间或指标套用到这两个接口。

验证入口：`TestDashboardRankingQueryValidation`、`TestDashboardRankingAmountsScopeAndShanghaiPeriod`、`TestDashboardRankingCreditReceiptsCurrentOwnerAndStableOrder`，纳入后台严格数据库 runner。新增排名不自动关闭控制台接线、浏览器和旧数据对账。

## 信用额度与合同退役

信用13条、合同23条旧注册全部保留原认证、权限、会话及CSRF包装，通过入口鉴权后分别返回 `410 credit_retired`、`410 contracts_retired`。退休处理器不读 Store/对象/文件、不重放历史回执、不登记还款或写入资金、合同、审计；无登录/错误入口/无权限/跨站写入先执行原安全错误。包括旧查询、设置、状态与合同下载，不保留独立历史业务入口。

相关前后台页面404、菜单与信用付款移除。`/admin/permissions` 及超级管理员 `/me` 使用活动权限目录，隐藏 `credit.*`、`contracts.*`；旧权限ID仍兼容存量角色保存，不代表模块可用。迁移184关闭信用、合同及前台信用显示开关；旧设置值不能恢复退役模块。用户确认无业务历史，本批仍不物理清库或重写既有财务SQL；通用资金投影保留历史口径。先停旧writer，成套升级迁移/API/worker/Web，发现真实历史先人工核查。完整规则见[信用](../customer-credit-portal-contract.md)与[合同](../domain/contract-settings.md)退役边界。

## 其余领域的权威字段和状态机

以下文档与生成目录配套阅读；目录负责覆盖注册操作，领域文档负责业务输入、状态、冲突和错误。需求声明中 `partial/unverified/deferred` 的项目仍需补充逐动作契约或验收，不视为前端可无条件交付。

| 领域 | 契约入口 |
| --- | --- |
| 商品、配置、购买字段 | [后台逐动作 API](catalog-api.md)、[目录管理](../catalog-management.md)、[商品配置](../product-configuration.md)、[购买字段](../product-purchase-fields.md)、[配置联动](../native-configuration-rules.md) |
| 折扣和优惠码 | [分组定价](../domain/customer-group-pricing.md)、[优惠码](../promo-code-restoration-contract.md) |
| 订单与服务状态 | [交易](../transactions.md)、[变更](../service-configuration-changes.md)、[活动记录](../service-activity.md)、[导航](../service-navigation.md) |
| 支付、退款与历史收款 | [支付](../payments.md)、[人工账单](../manual-invoices.md)、[现金口径](../credit-cash-reporting.md)、[退款操作](../payment-operations-runbook.md) |
| 发票 | [发票申请](../invoice-applications.md) |
| 已退役模块 | [信用退役边界](../customer-credit-portal-contract.md)、[合同退役边界](../domain/contract-settings.md)；旧实现仅历史参考 |
| 客户档案和文件 | [客户逐动作 API](customer-api.md)、[客户字段](../customer-registration-fields-contract.md)、[后台字段管理](../customer-field-admin-restoration-contract.md)、[客户文件](../customer-files.md) |
| 供应商与资源 | [供应商协议](../supplier-protocol.md)、[客户资源操作](../supplier-customer-operations-contract.md)、[NOKVM](../nokvm-delivery.md)、[人工库存](../domain/manual-resources.md) |
| 工单与通知 | [工单](../legacy-tickets.md)、[工单通知](../ticket-notification-contract.md)、[统一通知](../notification-architecture.md) |
| 推广与销售/报表 | [推广API](../affiliate-rules-api.md)、[推广核心](../affiliate-core.md)、[完整报表](../report-restoration-contract.md) |
| 内容与下载 | [内容](../content-management.md)、[分类](../content-taxonomy.md)、[下载](../download-center.md) |
| 认证和后台配置 | [实名认证](../domain/certification.md)、[RBAC](../admin-rbac.md)、[站点](../site-settings.md)、[表单契约](../settings-form-contract.md) |
| 调度、诊断、迁移 | [调度](../architecture/scheduler.md)、[诊断](../architecture/runtime-observability.md)、[迁移](../migration/README.md) |

## 契约验证与前端交付

```bash
node --test scripts/admin-api-inventory.test.mjs scripts/admin-api-consumers.test.mjs
node scripts/test-admin-api.mjs --integration
node scripts/test-customer-group-pricing.mjs --integration
node --test scripts/admin-customer-mutation-components.test.mjs scripts/customer-admin-isolation-components.test.mjs scripts/customer-group-pricing-components.test.mjs
node scripts/test-file-storage.mjs --unit
node scripts/test-file-storage.mjs --integration
node scripts/test-captcha.mjs --integration
./scripts/go.sh test ./internal/archtest -count=1
node scripts/check-doc-governance.mjs
node scripts/check-doc-links.mjs
```

integration要求任务自有回环PostgreSQL 18、`_test`库及强制入口run/pass零skip；不是业务数据库。本轮新增行为的必需测试在 [`test-admin-api.mjs`](../../scripts/test-admin-api.mjs)，代登录签发边界还纳入原认证严格门禁。生成器及源码声明仅E0，独立数据库行为E1；页面候选、TypeScript编译和既有截图不能替代新UI浏览器E2、真实渠道E3或发布E4。

客户摘要、精确上下文、交易/通知历史、成员/销售分配、钱包充值恢复、目录定价、库存和退役边界的实际数据库、组件与历史升级结果集中在[领域验收矩阵](../architecture/contract-verification-matrix.md)及本轮门禁产物。旧信用读取结果仅保留历史范围，不能替代退役测试；既有运行结果不能覆盖本轮新增高级筛选、原凭据自然过期、目录回执或账单归因。

规则配置由管理员/角色→定价锁→原实际凭据的顺序保护；目录 writer 统一通过 `CatalogTx`，在目录锁前锁主体，等待后和提交前复核自然过期。供应商导入、目录创建、商品升级及配置升级中的创建权限 helper 已处于目录事务边界内，不再用旧调用数量描述为未收口旁路。具体协议见[商品管理 API](catalog-api.md)与[分组定价](../domain/customer-group-pricing.md#配置写入的授权与锁序)；源代码调用边界和数据库锁序须分别证明。

客户列表资料/分组及列表、资源池销售归属已有稳定原请求恢复接线；客户详情整体、高级筛选、未付充值/推荐人、排名和库存 UI 继续分别实施。文件存储、认证、通知和其余资金历史升级的既有验收只覆盖各自原范围；新增 UI 浏览器、真实对象存储/其他外部渠道及候选发布未执行时不得标记通过。机器日志、清单和源文件哈希放在忽略的 output 制品中，不在本页累加历史 PASS 数字。

前端实施先完成客户详情：固定身份、summary真实状态、相关列表与创建的customer_id、稳定写入key/版本及分区错误，再按[需求取舍](api-requirements.md)逐领域推进。每批交付记录其请求/响应、权限账号、冲突/未知结果和浏览器证据；未执行的新UI、真实外部链路和部署不得标记通过。
