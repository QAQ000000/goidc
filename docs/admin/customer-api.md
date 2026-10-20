# 客户管理 API

本文给后台客户列表、新建客户、详情及关联动作提供实现契约。共同的响应包装、认证、错误与跨域分页规则见[后台 API 契约](api.md)；商品、账单、服务、认证与推广仍使用各自领域接口。客户 `users` 与管理员 `administrators` 物理分表，普通管理员凭据不能用于客户购物；受控代登录是独立、可审计的操作。

## 范围和页面装配

客户核心包括列表与高级筛选、资料和内部备注、账号启停、密码重置、客户分组、销售归属、联系人、受控代登录、管理员代建未付充值账单及推荐人调整。独立 CRM 跟进记录、补充说明和跟进状态暂不恢复；内部备注只维护 `profile.admin_note`。信用与合同已退役；客户物理删除、财务历史删除、数据导出、客户自助退款/停用申请不属于恢复范围。退款诉求通过工单交由管理员处理。

前端应分别加载详情、摘要和所选页签，按各接口权限决定按钮；接口拒绝不能转换成零金额或空历史。查看权限不能隐含写权限，查询其他业务页签也不能绕过该领域权限。`/admin/customers/{id}` 系列及明确实行客户存在/范围检查的接口，在客户不存在或不在当前销售范围时返回 404；关联领域遵循各自契约，例如交易分页/汇总对范围外客户返回空页/零汇总。完整客户详情可通过以下入口装配：

| 页面区域 | 接口 | 权限及关键状态 |
| --- | --- | --- |
| 资料 | `GET /admin/customers/{id}` | `customers.view`；返回 `profile_version`、`sales_version` |
| 摘要 | `GET /admin/customers/{id}/summary` | `customers.view`；每节 `available/reason/data`，继续要求对应业务权限 |
| 服务 | `GET /admin/services/page?customer_id={id}` | 服务权限与当前客户范围 |
| 账单 | `GET /admin/invoices/page?customer_id={id}` | 账单权限与当前客户范围 |
| 收支 | `GET /admin/transactions/page?customer_id={id}`、`GET /admin/transactions/summary?customer_id={id}` | 交易权限；现金和余额流水分开 |
| 操作日志 | `GET /admin/logs/page?kind=system&customer_id={id}` | 日志权限；精确主体历史 |
| 通知历史 | `GET /admin/notification-deliveries/page?customer_id={id}` | 通知权限与当前客户范围 |
| 推广上下文 | `GET /admin/customers/{id}/affiliate/page` | `customers.view`；其他客户身份继续按范围脱敏；推广领域的独立分页仍要求 `affiliates.view` |

本文中的路径统一省略 `/api/v1`；浏览器通过现有后台代理调用。金额都是 CNY 整数分。`Idempotency-Key` 是请求头，不能放进 URL；重试未知结果必须复用原请求体和原键，不能先换键再重新发起动作。

## 客户列表和高级筛选

`GET /admin/customers/page` 需要 `customers.view`，返回 `{items,total,limit,offset}`，固定按客户 ID 倒序。`total` 和 `items` 来自同一个数据库快照。旧 `GET /admin/customers` 保留数组兼容协议，只支持原基础 `q/limit/offset`，新页面使用 `/page`。

| 参数 | 协议 |
| --- | --- |
| `q` | 用户名、姓名或邮箱包含匹配；去首尾空白，最多 120 字符 |
| `group_id` | 正整数；`none` 表示未分组，省略、空或 `all` 表示不限 |
| `sales_owner_id` | 正整数；`none` 表示无销售归属，省略、空或 `all` 表示不限 |
| `status` | `active` 或 `disabled`；省略或空表示不限 |
| `company` | 公司包含匹配，1–200 字符 |
| `phone` | 资料手机包含匹配，1–80 字符；与 `users.phone` 登录手机号不同 |
| `qq` | 资料 QQ 包含匹配，1–40 字符 |
| `certification_status` | `unverified/pending/approved/rejected`；无认证档案视为 `unverified` |
| `level_id` | 存在且启用的客户等级规则 ID，正整数 |
| `custom_fields` | URL 编码 JSON 对象，以 `field_key` 为键，最多 20 项和 16384 字节；多个条件 AND |
| `limit/offset` | 沿用共同分页协议，`limit` 最大 100，`offset` 非负 |

所有筛选条件 AND。`q` 保留原 PostgreSQL `ILIKE` 的 `%/_` 通配兼容；新 `company/phone/qq` 和自定义文字字段按字面包含匹配，对 `%/_/\` 转义。公司和资料手机与当前列表显示投影一致：优先 `customer_profiles`；没有资料行时才回退到首个相应非空联系人值，已存在资料行的空串不会再回退。手机号筛选和资料编辑都不修改或验证登录手机号。

`custom_fields` 只接受当前启用的字段定义。`text/textarea` 使用包含匹配，搜索片段不需要满足完整值正则；`number/boolean/date/select` 使用类型保留的精确匹配，其中日期要求真实 `YYYY-MM-DD`，选择值必须属于定义选项。数字 `0` 和布尔 `false` 是有效条件；空串、空白、null、数组、嵌套对象、重复 JSON 键、未知或停用字段均返回 400。原 JSON 必须作为一个查询参数编码，例如：

```text
GET /api/v1/admin/customers/page?company=example&custom_fields=%7B%22number_value%22%3A0%2C%22boolean_value%22%3Afalse%7D
```

`level_id` 复用现有规则的六门槛 AND：已付账单金额、已付/生效订单的商品数量、全部登录次数、近 30 天登录次数、已付续费项数量、近 30 天续费项数量，仅匹配启用客户。它表示符合指定规则，不引入持久化等级、最高等级或排他规则；同一客户可以匹配多条规则。`GET /admin/customer-levels` 的 `matching_customer_count` 使用同一判定并保留 `customer_levels.view` 权限；目录筛选本身只要求 `customers.view`。

新增参数显式传空、非法值、未知或重复查询参数、无效 URL 编码返回 400；停用或不存在的等级、字段类型/选项错误同样返回 `invalid_customer_filter`。既有 `group_id/sales_owner_id/status` 错误码仍保留兼容。无权限返回 403。旧 `api_status=off/on/locked` 三态不恢复，不能用 API 密钥数量伪造该状态。

## 新建、编辑、启停和字段定义

`POST /admin/customers` 需要 `customers.manage` 和 8–128 字符 `Idempotency-Key`，必填 `email/password`，`username` 省略时从邮箱本地部分生成，`display_name` 省略时使用用户名；密码长度 8–72 字节。`status` 默认 `active`。可选 `group_id/sales_owner_id` 是正整数；显式设置分组额外要求分组权限，指定销售归属只允许超级管理员。创建只产生普通客户及其购物车，不创建管理员身份。

创建时资料字段平铺在请求体：`gender/company_name/phone/qq/country/province/address/postcode/source_channel/payment_method/language/admin_note/receive_marketing/custom_fields/custom_fields_version`。`gender` 为 `unknown/male/female`，`payment_method` 为 `bank/alipay/wechat`，`language` 为 `zh_CN/en_US`；其他长度与类型由现有资料规则验证。创建成功 201，同键同参数重放 200，返回 `{id,username,email,display_name,status,profile_version:1,replayed}`；创建响应的标识字段是 `id`。密码、hash 与其他凭据不回传。

旧客户资料中的头像、职业、签名及独立城市/区县字段没有加入当前客户资料模型，作为非本轮必需的旧资料扩展暂缓恢复。联系人里的 `avatar/city/region` 只属于联系人，不能据此宣称客户资料已支持这些字段。

`GET /admin/customer-field-definitions?limit&offset` 只读，允许 `customers.view` 或 `customers.manage`，返回 `{items,total,limit,offset,definitions_version}`。前端遍历分页后按定义渲染六类型输入；提交自定义字段时附带 `custom_fields_version=definitions_version`。该读取不授予字段定义 CRUD 权限。定义管理仍使用 `/admin/customer-fields`，需要 `customers.manage`。

`PATCH /admin/customers/{id}` 需要 `customers.manage`、8–128 字符请求键及当前 `expected_version=profile_version`。允许顶层 `display_name/email/status` 和嵌套 `profile`；嵌套资料采用稀疏更新，省略值保留，字段值不能直接传 null。更改自定义字段时传对象和目录版本；提交 `profile` 时，即使省略 `custom_fields`，也会合并原值并校验当前必填规则，仅修改顶层账号字段不会触发这项校验。停用/已删除定义的历史值原样保留且不可偷偷改写。

```json
{
  "expected_version": 3,
  "profile": {"admin_note": "仅内部可见的备注"}
}
```

PATCH 成功返回 `{user_id,profile_version,replayed}`，每次成功变更资料版本加一。缺版本为 409 `customer_version_required`，旧资料版本为 409 `stale_customer_profile`，定义版本变化为 409 `stale_customer_fields`；同键异参为 409，未授权/越界不能借版本错误探测客户。状态改为 `disabled` 同事务撤销客户会话，保留服务、账单、余额和财务历史；重新启用用同一 PATCH 协议。新建、编辑与旧回执读取均在事务内复核当前管理员、角色、原始会话/API 密钥及客户范围，等锁后的自然过期和提交前失效不能产生部分写入或成功回执。

`POST /admin/customers/{id}/password` 需要 `customers.manage`，请求 `{password}`；成功 `{user_id,password_reset_required:false}`。它是专用安全动作，撤销客户现有凭据，不接受任意凭据字段 PATCH。没有回执查询协议，前端不对未知结果自动换键循环重置。

## 分组、销售归属和联系人

| 动作 | 输入与输出 | 权限、版本和恢复 |
| --- | --- | --- |
| `GET /admin/customers/{id}/group` | `{customer_id,group_id,group_membership_version}` | `customer_groups.manage`；真实原始凭据与客户范围 |
| `PUT /admin/customers/{id}/group` | 显式 `group_id` 正整数或 null；`expected_version=group_membership_version` | `customer_groups.manage`、稳定请求键；CAS、同键重放；分组规则见[分组定价](../domain/customer-group-pricing.md) |
| `GET /admin/sales-owners` | 可选销售目录 | 超级管理员浏览器会话 |
| `PUT /admin/customers/{id}/sales-owner` | 显式 `sales_owner_id` 正整数或 null；`expected_version=sales_version` | 超级管理员浏览器会话；新前端总是发送稳定请求键；读取当前销售版本后处理 409 |
| `GET /admin/customers/{id}/contacts` | 联系人数组，含每项 `version` | `customers.view` 和客户范围 |
| `POST /admin/customers/{id}/contacts` | 联系人资料，邮箱必填；返回 `id/customer_id/version/replayed` | `customers.manage`；使用稳定请求键 |
| `PATCH /admin/customers/{id}/contacts/{contact_id}` | 稀疏联系人字段、`expected_version` | `customers.manage`；支持 CAS 与同键回执 |
| `DELETE /admin/customers/{id}/contacts/{contact_id}` | `{expected_version}`；返回 `deleted` | `customers.manage`；支持 CAS 与同键回执 |

联系人包含 `username/sex/avatar/company_name/email/wechat_id/country/province/city/region/address1/address2/postcode/phone_number/permissions/general_emails/invoice_emails/product_emails/support_emails/status`；`sex` 为 0/1/2，`status` 为 `active/inactive/disabled`。联系人不等于可直接登录的客户子账号。联系人旧客户端允许省略版本及请求键，为兼容保留；新后台必须保存读取版本并发送键，才能获得完整的覆盖冲突和未知结果恢复保护。联系人写入和旧回执重放在 Store 事务内重新核验权限、原始凭据和客户范围，审计失败回滚。

## 代建未付充值账单

管理员有两条不同的资金动作：直接人工调整余额使用现有 `/admin/customers/{id}/balance` 与 `/balance-operation`；代建未付充值账单使用以下新接口，创建时不入钱包。

`GET /admin/customers/{id}/topup-options` 与 `POST /admin/customers/{id}/topups` 均要求 `invoices.view`、`invoices.manage`、有效管理员原始凭据及当前客户范围；客户必须启用。选项返回 `enabled/min_topup_cents/max_topup_cents/max_balance_cents/balance_cents/reserved_cents/available_capacity_cents/require_active_order/has_active_order/version`，沿用客户在线充值的开关、金额、余额容量和有效订单要求。`available_capacity_cents=null` 表示没有余额上限，不等于容量为零。

```json
{"amount_cents": 10000, "expected_version": 2, "note": "应客户工单要求代建充值账单"}
```

POST 必须带 8–128 字符 `Idempotency-Key`，`expected_version` 使用选项的设置版本，`note` 必填且不超过 240 字符。返回 `invoice_id/invoice_no/amount_cents/status=unpaid/topup_status/version/settings_version/user_id/replayed`，新建 201、重放 200。回执重放保留原创建事实 `status=unpaid`，账单可能已支付；实时状态须另读账单详情，不能用旧回执显示当前待付状态。客户之后走既有网关支付和恰好一次余额入账流程；未支付、重复请求、创建失败都不直接增加余额。设置或客户资格变化返回明确冲突，前端刷新选项后要求用户重新确认，不能改写旧键的请求。

## 推荐人调整和历史归因

以下四个接口均要求 `customers.manage`、`affiliates.manage`、真实管理员会话/API 密钥及目标客户范围，返回私有、不缓存响应。

| 接口 | 协议 |
| --- | --- |
| `GET /admin/customers/{id}/referrer` | 当前关系：`customer_id/referral_version/attributed/referrer_visible/referrer_customer_id/referrer_name/effective_scope`；不接受查询参数 |
| `GET /admin/customers/{id}/referrer/options?q&limit&offset` | 分页候选 `{items,total,limit,offset}`，搜索最多 120 字符，limit 1–100，offset 非负 |
| `GET /admin/customers/{id}/referrer/history?limit&offset` | 不可变调整历史；含原因、操作者、前后版本、前后是否归因和范围内的推荐人身份 |
| `PUT /admin/customers/{id}/referrer` | 显式 `referrer_customer_id` 正整数或 null、`expected_version=referral_version`、1–500 字符 `reason`、单个 8–128 字符请求键 |

候选必须是启用客户、已审核推广账户、启用推广计划且在当前管理员客户范围内，排除客户本人；服务端拒绝自荐和循环关系。null 表示清除推荐人，清除后可重新绑定。同目标是 `changed=false`，不增加关系版本，仍保留审计和回执。PUT 返回当前关系并增加 `changed/replayed`；同键同参数重放原版本事实，同键异参为 409 `idempotency_conflict`，旧版本为 409 `version_conflict`。

当前关系和历史中的越界推荐人仅保留是否有归因，ID/姓名为 null，并通过 `referrer_visible` 明确提示不可见；重放旧回执时也按当前范围重新脱敏。`effective_scope` 固定为 `future_invoices_only`：人工调整只影响之后新建账单的归因，既有未付/已付账单、佣金、提现和退款不回溯修改。迁移 187 为历史账单冻结归因：已有佣金/周转证据优先，否则按升级时有效归因回填，冲突阻断升级；新账单创建时记录不可变归因或显式无归因；账单出生与推广关系写入要求 READ COMMITTED，其他事务隔离级别明确拒绝，防止等图锁后继续读旧快照。182 拆表前的历史管理员账单仍引用全局身份保留，不复制到客户表。发布须停止旧 writer 后成套升级，不能混跑旧版写入。

## 受控代登录

`POST /admin/customers/{id}/impersonate` 要求 `customers.manage` 与浏览器管理员会话，拒绝 API 密钥。成功返回 `{customer_id,expires_in_seconds:7200}` 并设置独立客户 Cookie；不把管理员账号转为客户。客户页面调用 `GET /auth/impersonation` 展示代登录状态，退出使用 `POST /auth/impersonation/exit`，清除代登录客户 Cookie。管理员原会话、当前角色、客户范围和双凭据继续约束后续访问；显示和退出按钮沿用[前后台身份边界](../admin-rbac.md#前后台身份边界)。

## 旧版 34 组动作对照

以下是旧 `UserManage` 的 44 条路由按真实动作归并后的覆盖表。跨领域动作保持领域归属；旧控制器里有方法不代表要新增同名客户接口。

| 旧动作组 | 当前承接或明确取舍 |
| --- | --- |
| 客户列表与基础筛选 | `/admin/customers/page`，本轮补公司、资料手机、QQ、实名、规则等级、自定义字段 |
| 无销售资源池 | 同一分页接口 `sales_owner_id=none` |
| 客户身份与编辑资料读取 | `/admin/customers/{id}` |
| 真实客户摘要 | `/admin/customers/{id}/summary`，按节权限 |
| 新建客户选项 | 字段只读定义、客户分组、销售人员、支付模块目录分别授权 |
| 新增普通客户 | `POST /admin/customers` |
| 编辑资料与内部备注 | `PATCH /admin/customers/{id}` |
| 启停客户 | 同一 PATCH `status=active/disabled`，保留财务历史 |
| 重置密码 | `/admin/customers/{id}/password` |
| 客户分组成员 | `/admin/customers/{id}/group` |
| 销售归属 | `/admin/customers/{id}/sales-owner` |
| 以客户身份登录 | 显式 `/impersonate`，双 Cookie 与审计 |
| 客户服务列表 | 服务分页 `customer_id` 精确筛选 |
| 客户账单/产品账单 | 账单分页 `customer_id` 与账单详情 |
| 客户流水/收支 | 交易分页及汇总，不混用充值和业务消费 |
| 固定客户人工账单 | 账单客户选择器精确筛选及现有人工账单创建 |
| 直接人工充值钱包 | 现有 `/balance` 与 `/balance-operation` |
| 代建未付充值账单 | 新 `/topup-options`、`/topups`，复用在线充值结算 |
| 客户操作日志 | `/admin/logs/page?kind=system&customer_id={id}` |
| 客户通知历史 | `/admin/notification-deliveries/page?customer_id={id}` |
| 推荐人与推广上下文 | 客户推广页与推广归因分页 |
| 修改推荐人 | 新 `/referrer`、`/referrer/options`、`/referrer/history` |
| 独立跟进记录读取 | 用户暂缓，当前仅单个内部备注 |
| 新增跟进记录 | 用户暂缓 |
| 跟进补充说明 | 用户暂缓 |
| 客户跟进状态 | 用户暂缓 |
| 物理删除客户和财务历史 | 用户排除 |
| 授信/信用读取及付款 | 已退役，鉴权后 410 |
| 客户合同 | 已退役，鉴权后 410 |
| 客户自助退款/停用申请 | 不恢复，工单与管理员退款/服务动作承接 |
| 客户认证列表/日志 | 认证领域分页、详情、历史；不冒充客户资料写入 |
| 认证提交/更正/审核 | 认证领域独立契约；任意替换认证资料不能由 generic PATCH 冒充 |
| 认证文件下载 | 认证领域授权业务下载，与已移除数据导出不同 |
| 联系人读取与管理 | `/admin/customers/{id}/contacts` 四方法，Store 权限与凭据复核 |

## 验证边界和前端交付

专项入口包括 `TestCustomerDirectoryFilterInput`、`TestCustomerDirectoryAdvancedProfileFilters`、`TestCustomerDirectoryLevelFilterMatchesExistingEligibility`，以及客户创建/资料、凭据过期、密码撤权、客户范围、分组/销售锁序、联系人和充值/推荐人回归。实际执行证据由[领域验收矩阵](../architecture/contract-verification-matrix.md)及 `output/` 中运行产物记录，测试名存在不能写成已通过。新增高级筛选、充值和推荐人后台 UI 仍需按本文接线，并分别完成浏览器加载、空、无权限、冲突、未知结果重试与响应式验收；接口实现不能替代界面和生产发布证明。
