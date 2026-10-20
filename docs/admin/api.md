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
| 文件 | 数据导出不恢复；附件/合同/发票等业务文件仍使用授权上传下载接口，不用报表 JSON 拼下载地址 |

权限完整定义见[RBAC](../admin-rbac.md)。目录中 `@super` 表示超级管理员；`sessionAuth` 等包装可能另要求浏览器会话，不能仅根据权限字符串推断允许 API 密钥。

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

示例省略其余 section；实际总是返回下表全部8个 section。`available=false` 时 `reason="permission_denied"`、`data=null`；有权但无业务事实返回真实零值/空数组，资料关系不存在时字段可为null。`last_login_at` 来自该客户成功认证审计的最近时间，缺记录为null，不包含IP或凭据；`login_phone/phone_verified` 对应登录账号的手机号，不能给联系人或资料中的另一个号码加验证标记。`profile_version` 用于客户修改，`sales_version` 用于销售归属，不相互替代。

| section | 读取权限 | `data` 字段与口径 |
| --- | --- | --- |
| `services` | `services.view` | `total/active/suspended/overdue/expiring_30_days` 均为服务件数；`groups:[{group_id,group_name,total,active}]` 按真实服务导航组分组，未分组的id/name为null；不归类到固定“魔方云” |
| `invoices` | `invoices.view` | `unpaid_count/unpaid_cents/overdue_count/overdue_cents`；仅status=unpaid，逾期为 `due_at<as_of`；信用未还金额另见credit，不重复当作未付账单 |
| `tickets` | `tickets.view` | `total/merged_sources/open/pending/resolved/closed`；同时执行工单部门范围；total保留合并来源，四种当前状态数排除来源工单，所以四种状态数之和可小于total |
| `wallet` | `invoices.view` 或 `balance.manage` | `balance_cents/credits_cents/debits_cents`；后两者是全历史余额账本正/负变动，含赠款、退款和余额消费，不能称外部收入/支出 |
| `cash` | `invoices.view` | `gateway_receipts_cents/credit_repayment_cents/external_refunds_cents/receipts_cents/net_receipts_cents`；支付成功＋已登记信用还款为收款，已确认外部退款为退款；净收款=收款－外部退款；不含余额消费或赠款，不是利润 |
| `credit` | `credit.view` | `account_exists/limit_cents/used_cents/available_cents/frozen/version/enabled`；available=limit-used，冻结及全局关闭须另外判断；账户不存在时金额/版本为null，不推断为可用零额度 |
| `certification` | `certifications.view` | `profile_id/status/certification_type/version/updated_at`；无档案status=unverified，其余可空；不返回证件原文或提供方结果 |
| `referral` | `affiliates.view` | `attributed/status/referrer_customer_id/referrer_name/registered_at`；推荐人不在当前客户范围时，其id/name为null，不能通过归因跨范围查看另一个客户 |

服务逾期/即将到期仅包含active、suspended，前者 `next_due_at<as_of`，后者 `[as_of,as_of+30天)`；一次性或无到期时间不进入期限计数。所有财务累计均为快照中已经登记的全历史事实，受权限和客户归属约束，未记录的线下现金/费用不会被推断补齐。

原 `GET /admin/customers/{id}` 继续提供编辑资料；其中旧 `income_cents/expense_cents` 是余额账本汇总。新详情页使用 summary 的wallet/cash区分展示，不能再把旧字段标为实收/退款。编辑字段定义见[客户字段契约](../customer-registration-fields-contract.md)。

## 客户详情的关联接口与写操作

客户详情的固定身份来自URL中的客户id；切页签、刷新和重试均保留它。嵌入详情的文件、账单、订单、工单创建不提供可清空的客户归属选择器。全局管理页继续允许授权范围内筛选，服务端始终重新校验。

本节的关联列表及创建客户选项统一接收精确 `customer_id`：省略保留全局范围；显式提供必须为单个纯十进制正整数，空值、重复、符号、非数字或溢出返回400 `invalid_customer`。真实客户不存在或不属于当前客户范围返回404 `not_found`，不降级为全局查询；范围内客户没有匹配记录返回空页。最终数据查询仍保留客户范围条件。客户门户沿用本人身份，不能用这些参数切换客户。

| 用途 | 方法与路径 | 权限/关键输入 |
| --- | --- | --- |
| 客户列表 | `GET /admin/customers/page` | `customers.view`；`q/group_id/sales_owner_id/status/limit/offset`；group/owner可用 `none` 筛未分配，status=active或disabled |
| 编辑资料读取 | `GET /admin/customers/{id}` | `customers.view`；读取profile_version及原资料，缺失值保留null |
| 编辑/启停客户 | `PATCH /admin/customers/{id}` | `customers.manage`；稳定key、`expected_version`，以及至少一个 `display_name/email/status/profile` |
| 充值到钱包 | `POST /admin/customers/{id}/balance` | `balance.manage`；稳定key；`amount_cents`为正整数、`note`≤240；受财务充值开关/上下限限制 |
| 查询充值结果 | `GET /admin/customers/{id}/balance-operation` | `balance.manage`；原 `Idempotency-Key` 放请求头；按客户及key查询人工充值账本事实，不触发充值 |
| 修改客户分组 | `PUT /admin/customers/{id}/group` | `customer_groups.manage`；按[分组定价](../domain/customer-group-pricing.md)契约传 `group_id`，null表示移出分组 |
| 分配销售 | `PUT /admin/customers/{id}/sales-owner` | 超级管理员浏览器会话；`sales_owner_id`可空，`expected_version`使用sales_version |
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

### 信用账户与历史的客户上下文

`GET /admin/credit`、`GET /admin/credit/consumptions`、`GET /admin/credit/repayments` 均要求 `credit.view`，不额外要求 `customers.view`。三个入口的 `customer_id` 遵循本节严格解析、范围外404和不回退全局规则；省略时仍列出当前授权客户范围，合法但无信用账户/记录返回空页。客户核验、总数和当前页使用同一个只读快照，最终SQL保留当前客户范围约束。

返回通用分页 `{items,total,limit,offset}`，保留现有字段。账户支持 `q/status`，status为active或disabled，表示客户状态；消费支持 `q/status/date_from/date_to`，status为outstanding、repaid或refunded；还款支持 `q/date_from/date_to`，不接受status，因为只列已确认到账记录。日期为上海自然日，起止均包含当天；消费按 `created_at`，还款按实际 `received_at`，不改用凭证录入时间。日期/状态错误保留原错误码，精确客户与其他筛选取交集。

## 统一交易分页

`GET /admin/transactions/page` 与 `GET /admin/transactions/summary`，均要求 `invoices.view`，限制客户范围。新详情页替代旧数组 `/admin/customer-transactions`；旧数组继续兼容，不据此假定它含全部新增信用事件。

共同筛选：`customer_id` 正整数、`q`≤120、`kind=topup|product|renew|other`、`direction=income|expense|neutral`、`channel`≤100、`from/to=YYYY-MM-DD`（上海、包含to当天）、`min_cents/max_cents`非负整数且最小≤最大；分页另传limit/offset。筛选无效400 `invalid_transaction_filter`。不指定customer_id时查询授权范围；范围外精确筛选返回空页，不暴露外部客户。summary使用同样过滤，不能把分页中当前一页求和当全部合计。

分页data为 `{items,total,limit,offset}`。每行字段：`event_key/source/id/user_id/invoice_id/invoice_no/occurred_at/channel/amount_cents/direction/transaction_no/description/kind/balance_after/customer_name/customer_username/sales_owner_name/currency`。`event_key`是跨来源稳定唯一键，不能单用id当React key；金额始终非负，方向决定展示符号；balance_after可空。

| source | 方向与渠道 | 含义 |
| --- | --- | --- |
| `payment` | income，具体支付提供方 | 已成功外部支付，含在线充值；统计实收一次 |
| `external_refund` | expense，退款提供方 | 已确认实际外部退款；非钱包退款 |
| `ledger` | income/expense/neutral，balance/admin_topup/gateway_topup | 钱包正负变动；gateway_topup为neutral，避免把同次外部收款再记收入 |
| `credit_consumption` | neutral，credit | 信用支付账单事实；非新现金，按invoice_id稳定标识，不再重复出现invoice fallback |
| `credit_repayment` | income，credit_repayment | 已登记的真实信用还款，时间为received_at；多次还款分别保留；不返回私密收据/备注 |
| `invoice` | neutral，unrecorded等 | 历史已付但无上述支付/账本/信用消费证据的兼容项，不能推断为外部收款 |

summary返回整数分字段 `gateway_income_cents/external_refund_cents/net_gateway_cents/credit_repayment_cents/cash_income_cents/net_cash_cents` 及currency。旧 `net_gateway_cents=gateway_income_cents-external_refund_cents` 保持兼容；`cash_income_cents=gateway_income_cents+credit_repayment_cents`，`net_cash_cents=cash_income_cents-external_refund_cents`。钱包、消费和信用支付均不加入该现金合计。

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

前端成功后刷新服务详情，后续自动续费/上游回读仍按[交易与自动续费](../transactions.md)和[供应商协议](../supplier-protocol.md)执行。管理员代续费或批量立即续费不能通过改Cookie调用客户接口，尚无契约的强制价格/期限/归属修改不在本接口范围。

## 代登录与长期凭据

显式代登录沿用[身份边界](../admin-rbac.md#前后台身份边界)，真实actor与被代登录客户分开保留。代登录会话调用 `POST /account/api-keys` 一律403 `impersonation_credential_forbidden`，不创建key、不增加签发审计；绑定查询故障停止签发。判断只针对本次选中的客户会话绑定，不能因为浏览器同时有后台Cookie就禁用普通客户的独立登录。客户自身及管理员个人的正常密钥管理继续复用各自portal。

## 资源与商品读取

`GET /admin/resources` 要求 `resources.view`，返回 `{items,total,limit,offset,suppliers}`。除原有 `q/ip/supplier_id/status`，新增精确 `customer_id/product_id`。customer_id遵循本页共同规则；product_id显式空、重复、非纯正十进制或溢出400 `invalid_product`，合法但无匹配商品返回空页。分页数据和计数处于同一快照。

资源项新增 `product_id,product_name,next_due_at`，原有 `id,name,domain,status,username,upstream_id,resource_info,user_id,customer_name,supplier_id,supplier_name` 保留；resource_info仅返回已有白名单。到期时间可为null。suppliers仅包含当前客户范围及所选客户/商品所涉及的供应商，不根据搜索、IP或状态进一步缩减。该资源模型是已登记服务，不能当作独立库存池或完整成本台账。

`GET /admin/catalog-groups/{kind}/page` 要求 `products.view`，kind为 `section/category`；返回通用分页结构，按sort_order、id升序。支持 `q`（名称/标识/描述，最多120字符）、`active=true|false`、`limit/offset`；category还支持精确 `section_id`。重复筛选或无效active返回400 `invalid_catalog_filter`，无效/空/重复section_id或在section查询中使用section_id返回400 `invalid_section`。分组类型无效400 `invalid_catalog_group`。不传筛选包含停用分组；合法父分组无结果返回空页。

items保留原数组接口的分组字段（含id、name、slug、description、active、sort_order、version、member_count）；category另含section_id、section_name、section_active。原 `GET /admin/catalog-groups/{kind}` 继续返回完整数组。

商品配置组创建/修改输入新增可选 `notes`，最多5000字符；省略或null保留原备注，显式空字符串清空，新建省略为空。仅后台商品投影返回notes，公开商品和订单创建商品选项不返回。全局配置定义也保存备注，更新省略时按slug保留；分配同步、复制和脱离规则见[商品配置](../product-configuration.md)。仍需原expected_revision/版本及写权限，备注不能绕过共享组/供应商配置保护。

## 工单附件上传恢复

`POST /tickets/{id}/attachments` 使用multipart字段 `file`（兼容 `attachment`），保留类型和10 MiB限制。新前端应在首次上传前保存8–128字符的 `Idempotency-Key`，并保存文件名、类型、大小和SHA-256对应的原文件意图。后台共享工单接口带 `X-Auth-Portal: admin`；上传仍要求既有工单写权限、部门及当前凭据复核。

带key上传先持久化意图，再写文件；最终附件、审计和成功回执同事务提交。同actor/key的文件或工单不同返回409 `idempotency_conflict`；首次成功201，成功重放200并返回原附件，不再次Put、插入或审计。成功响应保留 `id,ticket_id,uploader_id,filename,mime_type,size_bytes,sha256,created_at`，另含 `state:"confirmed",replayed`。没有key的旧客户端继续兼容，但不具备本协议的恢复保证。管理员有key/无key上传统一复核工单部门及真实客户范围；保留历史管理员归属工单的部门处理规则。

结果查询使用 `GET /tickets/{id}/attachment-upload`，同一个 `Idempotency-Key` 放请求头，不进入URL，返回private/no-store：

```json
{"data":{"state":"confirmed","attachment":{"id":456,"ticket_id":123,"filename":"evidence.txt","mime_type":"text/plain","size_bytes":12,"sha256":"...","uploader_id":7,"created_at":"2026-10-20T08:00:00Z"}}}
```

仅有意图而没有已提交业务回执时，返回 `{state:"unconfirmed"}`；此时同key上传409 `attachment_upload_unconfirmed`，不得换key或自动重传。未知key、其他actor或其他工单的key返回404；查询仍验证当前工单可读权限和凭据，关单后可查已确认结果。确认成功表示业务上传曾提交，不证明附件仍未被删除、移动或能从外部存储下载，后续访问继续走原授权下载。

断网、5xx和明确的上传未知错误都先查原key；查不到回执不证明可以安全重试。413/415等预约前的确定输入错误可修正文件后重新提交；预约后的审计回滚、外部Put超时、进程退出或提交确认丢失保守保留未确认意图，人工核验原存储及journal，不能用SHA相同推定是此次上传。对象路径、存储profile和原始外部错误不返回。工单/回复创建成功与附件未确认必须分别展示；当前UI尚未接入此恢复协议。

## 其余领域的权威字段和状态机

以下文档与生成目录配套阅读；目录负责覆盖注册操作，领域文档负责业务输入、状态、冲突和错误。需求声明中 `partial/unverified/deferred` 的项目仍需补充逐动作契约或验收，不视为前端可无条件交付。

| 领域 | 契约入口 |
| --- | --- |
| 商品、配置、购买字段 | [目录管理](../catalog-management.md)、[商品配置](../product-configuration.md)、[购买字段](../product-purchase-fields.md)、[配置联动](../native-configuration-rules.md) |
| 折扣和优惠码 | [分组定价](../domain/customer-group-pricing.md)、[优惠码](../promo-code-restoration-contract.md) |
| 订单与服务状态 | [交易](../transactions.md)、[变更](../service-configuration-changes.md)、[活动记录](../service-activity.md)、[导航](../service-navigation.md) |
| 支付、退款、信用 | [支付](../payments.md)、[人工账单](../manual-invoices.md)、[现金口径](../credit-cash-reporting.md)、[退款操作](../payment-operations-runbook.md) |
| 发票和合同 | [发票申请](../invoice-applications.md)、[合同状态机](../admin-contract-lifecycle-plan.md)、[合同设置](../domain/contract-settings.md) |
| 客户档案和文件 | [客户字段](../customer-registration-fields-contract.md)、[后台字段管理](../customer-field-admin-restoration-contract.md)、[客户文件](../customer-files.md) |
| 供应商与资源 | [供应商协议](../supplier-protocol.md)、[客户资源操作](../supplier-customer-operations-contract.md)、[NOKVM](../nokvm-delivery.md) |
| 工单与通知 | [工单](../legacy-tickets.md)、[工单通知](../ticket-notification-contract.md)、[统一通知](../notification-architecture.md) |
| 推广与销售/报表 | [推广API](../affiliate-rules-api.md)、[推广核心](../affiliate-core.md)、[完整报表](../report-restoration-contract.md) |
| 内容与下载 | [内容](../content-management.md)、[分类](../content-taxonomy.md)、[下载](../download-center.md) |
| 认证和后台配置 | [实名认证](../domain/certification.md)、[RBAC](../admin-rbac.md)、[站点](../site-settings.md)、[表单契约](../settings-form-contract.md) |
| 调度、诊断、迁移 | [调度](../architecture/scheduler.md)、[诊断](../architecture/runtime-observability.md)、[迁移](../migration/README.md) |

## 契约验证与前端交付

```bash
node --test scripts/admin-api-inventory.test.mjs scripts/admin-api-consumers.test.mjs
node scripts/test-admin-api.mjs --integration
node scripts/test-file-storage.mjs --unit
node scripts/test-file-storage.mjs --integration
node scripts/test-captcha.mjs --integration
./scripts/go.sh test ./internal/archtest -count=1
node scripts/check-doc-governance.mjs
node scripts/check-doc-links.mjs
```

integration要求任务自有回环PostgreSQL 18、`_test`库及强制入口run/pass零skip；不是业务数据库。本轮新增行为的必需测试在 [`test-admin-api.mjs`](../../scripts/test-admin-api.mjs)，代登录签发边界还纳入原认证严格门禁。生成器及源码声明仅E0，独立数据库行为E1；页面候选、TypeScript编译和既有截图不能替代新UI浏览器E2、真实渠道E3或发布E4。

上一批在隔离PG18以race分组实跑并严格核对后台API的38个必需入口、文件存储的53个必需入口，全部run/pass、零跳过。文件存储由原42个入口与冻结版11个附件入口合并核对；后台由37个入口与这11个附件入口合并核对，覆盖最后增加的回执锁等待后撤权；两组有交集，不相加为独立用例数。另29个存储单元入口、16个静态盘点/消费者夹具、全仓Go编译、相关包vet、架构约束与文档治理/链接/契约门禁检查通过。该批架构基线从692减至676条，仅移除已下沉的16条豁免，不放行新越界。机器日志、清单和源文件哈希保存在忽略的output制品中。

本轮严格后台入口扩为59个（HTTP 57、Store 2），在独立PG18的UTC会话以race串行实际运行，全部run/pass、零跳过。新增覆盖严格信用客户上下文及上海日期、充值8并发单次入账、旧人工账本与跨管理员原key核验、网关记录隔离、审计回滚、锁等待后角色/范围/会话/API密钥撤销、凭据自然过期、真实密码轮换锁序，以及PostgreSQL已提交但COMMIT确认报文丢失后的查询与原key重放。信用读取又下沉4条HTTP持久化调用，基线由676减至672，未增加豁免。充值结果查询和固定信用上下文的前端交互尚未浏览器验收；不把API测试当作页面闭环。

另15个充值/退款/资金锁序与历史升级入口在同一隔离PG18以race串行通过且零跳过，覆盖在线充值与管理员加款/余额消费/退款的竞争、资金→设置锁序及真实schema168/169起点升级。历史测试资金使用专用事务夹具保留余额、账本和审计事实，生产鉴权不回退旧身份表。

本批没有重跑完整认证/通知业务门禁，也没有执行新UI浏览器、真实对象存储或部署；既有验收只保留其原范围。完整后台各领域仍按需求清单中的API/UI/验证状态推进。

前端实施先完成客户详情：固定身份、summary真实状态、相关列表与创建的customer_id、稳定写入key/版本及分区错误，再按[需求取舍](api-requirements.md)逐领域推进。每批交付记录其请求/响应、权限账号、冲突/未知结果和浏览器证据；未执行的新UI、真实外部链路和部署不得标记通过。
