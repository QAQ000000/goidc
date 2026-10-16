# 客户信用门户设计契约（2026-10-16）

状态：本批已实现并通过[隔离验收](customer-credit-portal-acceptance-20261016.md)，未部署。设计核对基线：`fd2a576e3c65c129ac7d1b5a8497c5d42bd61e87`；下文源码现状描述保留设计时点，实际实现与证据以验收文档为准。线上还款、账期合账、授信审批及现金退付仍未实现。

## 1. 范围与旧规范映射

本批限定三个客户页面/分区和两个后台回归页：账户信用概览、信用消费/还款记录、账单详情信用状态；后台信用额管理、信用额设置。记录页建议 `/console/credit`，账户和账单沿用现有路由。本批路由现已实现；以下保留设计映射。

| 旧规范依据 | 当前证据 | 本批承接与边界 |
| --- | --- | --- |
| 旧规范第15页，330–353行：消费/还款/客户三个标签、账单及客户筛选 | [台账#15](admin-full-ui-restore-ledger.md:27)；[后台组件](../web/src/components/admin-credit.tsx:28)、[记录组件](../web/src/components/credit-records.tsx:14) | 后台已有，客户门户借用消费/还款语义，不复制管理员客户列表 |
| 同页：账单、产品、金额、付款时间、状态 | [消费查询](../internal/httpapi/credit_lifecycle.go:79) 仅有消费和账单字段，未带产品 | 显示账单号、消费金额、剩余债务、信用付款时间、状态；产品通过账单详情查看，不编造单一产品 |
| 同页：应还、已还、还款日期；未出账、当月还款状态 | [迁移129](../db/migrations/129_credit_lifecycle.sql:1) 无账期、到期日、合账实体 | 仅按消费对应还款事实展示；不把创建日当还款期限，不把未还债务称未出账，不生成当月还款结论 |
| 第39页，824–839行：信用总开关 | [信用设置](../internal/httpapi/credit_settings.go:8)、[付款校验](../internal/store/finance.go:164) | 总开关决定能否新增信用付款，不抹去既有债务 |
| 常规设置，921行：前台信用额开关 | [设置结构](../internal/store/site_settings.go:37)、[设置表单](../web/src/components/site-settings.tsx:63) | 独立显示偏好；不得冒充付款授权开关 |
| 缺口审计：客户信用门户 | [缺口记录](replacement-gap-audit-20261016.md:52) | 关闭客户可见性缺口；线上还款、授信审批、账期合账、已还款现金退付继续待实现 |

旧规范源文件：`/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md`。以上为逐项设计映射，不将后台旧规范当作已有客户门户规范。

## 2. 现有事实与金额口径

现有客户会话 `POST /invoices/{id}/pay-credit`；后台 `GET /admin/credit`、`GET /admin/credit/consumptions`、`GET /admin/credit/repayments`、线下还款登记、冻结及限额路由见 [server.go](../internal/httpapi/server.go:144)。当前没有客户信用读取路由。客户账单已有信用按钮和未知结果原请求恢复，见 [transactions.tsx](../web/src/components/transactions.tsx:982)；账户页只有余额概览及流水，见 [data-pages.tsx](../web/src/components/data-pages.tsx:813)。

所有金额为 CNY 整数分，禁止浮点累加。`customer_credit_accounts.limit_cents` 是该客户实际授权额度，`used_cents` 是当前占用额度，`available_cents=max(limit_cents-used_cents,0)` 是账面剩余额度。冻结、总开关关闭时仍显示账面数值，但必须注明“当前不可新增信用付款”；不能把账面可用额度当实际可付款承诺。`balance_cents` 是另一账户，不能和额度相加。

[迁移126](../db/migrations/126_credit_management.sql:1) 对现有和新建客户初始额度均为0。`credit_settings.limit_cents`、`auto_recharge` 在当前设置接口可存取；现有付款只校验总开关和独立客户账户，不能声称全局额度自动授予客户或自动充值已执行。门户不得用全局额度兜底客户额度。

消费 `amount_cents` 是原始信用付款金额；`remaining_cents` 是剩余债务。对非退款消费，已还金额可由该消费的真实还款记录求和；不得仅以 `amount-remaining` 标成已还金额，因为退款会把 remaining 清零。`outstanding` 包括未还和部分还，`repaid` 为还清，`refunded` 为信用债务已冲销。付款成功不表示已还款。

## 3. 两个开关的明确契约

`credit_settings.enabled` 控制新增信用付款，沿用服务端事务校验；`site_settings.admin_settings.frontend_credit_enabled` 控制前台额度摘要和信用付款入口显示。当前 [公开站点接口](../internal/httpapi/site_settings.go:11) 只返回 SiteProfile，不能假设客户已能取得 AdminSettings，也不能把完整管理设置公开。

拟由下述客户概览接口只返回 `display_enabled` 和 `payment_enabled` 两个安全布尔量。显示开关关闭时，隐藏账户额度卡片及普通信用付款入口；客户已有债务的消费/还款记录和账单信用结算事实仍可访问，避免隐藏其债务。记录入口保留在账户的财务记录区，文案不承诺新授信。总开关关闭、冻结或零额度均不阻断历史读取。

显示开关只是显示策略，不作为资金安全授权。直接调用现有付款接口仍由总开关、冻结、额度、账单条件及会话校验决定，不能宣称显示关闭即服务端禁止付款。已发出的未知结果请求保留原键/原载荷恢复入口，不因显示开关变化丢失恢复能力。

## 4. 客户只读 API 契约

以下端点现已实现；不修改现有后台 API 响应或付款/还款写契约。

| 端点 | 响应字段白名单 |
| --- | --- |
| `GET /credit/account` | `display_enabled,payment_enabled,limit_cents,used_cents,available_cents,frozen,version,updated_at` |
| `GET /credit/consumptions` | 包装 `items,total,limit,offset`；行字段 `invoice_id,invoice_no,amount_cents,remaining_cents,status,version,created_at,refunded_at` |
| `GET /credit/repayments` | 同样包装；行字段 `id,invoice_id,invoice_no,amount_cents,received_at,created_at` |
| `GET /invoices/{id}/credit` | 无信用付款时 `credit:null`；否则 `credit` 仅包含该消费白名单和由真实还款求和的 `repaid_cents` |

还款 `receipt`、`note`、`actor_id`、幂等键、退款操作人及管理员内部字段全部不返回；不得在前端隐藏后仍放进 JSON。概览、列表和详情均采用类型化 DTO，不用管理员通用行转Map响应。不得以遗漏账户行默默补零；正常注册已有触发器创建，异常缺行返回可重试服务错误并记录服务端诊断，不触发写修复。

身份和租户边界：必须是有效客户会话，匿名401、非客户403；仅以会话 identity.ID 作为 user_id，用户参数不能选择客户。拒绝 `customer_id/user_id/tenant_id` 等越权筛选参数400。项目当前按客户归属隔离，不虚构额外 tenant_id 模型；消费、还款与账单连接必须同时核对各自 user_id 与会话一致。账单不存在或非本人统一404。无信用付款的本人账单返回 `credit:null`，不能泄漏其他账单存在性。联系人权限标签不等于独立登录授权；本批不增加联系人登录。

所有客户资金响应 `Cache-Control: no-store`；前端缓存键包含当前身份，退出/切换客户清空，不使用跨身份 sessionStorage 数据。现有信用付款未知结果恢复继续使用已有身份隔离机制。

## 5. 筛选、分页与一致性

消费列表：`q` 仅搜本人账单号/账单ID；`status` 允许空、outstanding、repaid、refunded；`date_from/date_to` 按信用付款 created_at。还款列表：q同上、日期按 received_at；不搜内部凭证、不接受债务状态。两列表可接收正整数 `invoice_id`，仍强制本人范围。非法日期、反向日期、非法状态及invoice_id返回400。

日期沿用 [现有筛选](../internal/httpapi/credit_lifecycle.go:39)：Asia/Shanghai自然日，起日包含，止日次日00:00排除，不能仅凭浏览器本地时区计算。q去首尾空白、最多100字符，按字面子串处理 `%/_`，不允许无界输入。

沿用 [pagination](../internal/httpapi/server.go:860) 契约：默认25，limit合法1–100，否则回落25；offset缺省或负值归0。UI提供25/50/100。消费按 created_at DESC,invoice_id DESC，还款按 received_at DESC,id DESC。total与items在同一只读一致性快照中取得，分页不能跨客户计数；并发新增可能改变后续页，刷新后重新定位，不承诺整个翻页周期冻结快照。

tab、q、日期、状态、页码及page size写URL；改筛选/条数重置第一页，浏览器后退恢复。零条和翻页后越界分别处理，越界返回空items与真实total并给回第一页入口。概览和记录独立读取不承诺跨请求原子快照；付款/还款/退款后刷新相关资源，标注加载失败，不能用旧摘要断言新记录错误。

## 6. 页面状态和退款同步

账户卡显示总额、已用、账面可用、冻结/关闭提示和记录入口，不新增还款按钮。记录页双标签，消费列为账单、消费额、剩余应还、时间、状态；还款列为账单、到账金额、到账时间、登记时间。消费详情链接进入本人账单；从账单返回保留筛选上下文。账单显示“信用付款完成”和债务状态两个事实，防止把 invoice.paid 当作 debt.repaid。

退款沿用 [finance.go](../internal/store/finance.go:433)：只有未发生任何还款、remaining等于原消费额的信用付款才可能走现有退款；仍受其他既有业务保护限制。成功在同事务减used、remaining置0、status置refunded，见 [冲销](../internal/store/finance.go:535)。前台文案为“信用债务已冲销”，不能说“现金到账”。消费已部分/全部还款时现有 `credit_repaid_refund_manual` 拒绝自动退款，本批不新增强行完成或现金退付入口，也不造还款行。

后台真实线下还款登记成功后，[现有实现](../internal/httpapi/credit_lifecycle.go:179) 同事务减used、减remaining并落还款行；门户下次读取应反映部分还款/还清。还款记录只表示后台已确认到账，不能显示等待在线支付状态。消费总额减剩余不等于现金退款或合账余额。

每个资源覆盖加载、空态、失败重试、登录失效和禁止访问；失败时不能以0或“已还清”替代未知。只读请求可重试；现有信用付款继续保持原载荷/幂等键和409提示，不增加自动重复付款。

## 7. 设计验收清单

本表列出设计要求；实际运行结果和边界见[验收文档](customer-credit-portal-acceptance-20261016.md)，不把静态核对记作运行PASS。

| 检查 | 必须形成的证据 |
| --- | --- |
| A/B客户与匿名/管理员 | 响应码、字段白名单、改参数/账单ID/分页攻击无跨客户数据及计数；会话撤销后不可继续读 |
| 金额闭环 | 隔离库信用付款→部分还款→还清，额度和债务守恒，余额不变；冻结/总开关关闭/0额度仍可读历史 |
| 退款闭环 | 未还款消费退款后refunded、remaining=0、used减少；已还款拒绝退款且数据不变；不能把冲销当还款 |
| 设置矩阵 | 显示开/关×总开/关×冻结状态；关闭显示仍能看本人历史；未知付款结果可继续恢复 |
| 筛选分页 | 同时刻多条稳定排序、日期边界、非法参数、空态、越界、URL后退恢复；total与items本人范围一致 |
| 字段泄露 | 消费/还款/详情不含receipt、note、actor、键及内部设置；错误体不带SQL或其他客户资料 |
| 四宽浏览器 | 1440/1024/390/320下账户、双标签记录、账单及两个后台回归页；表格受控横滚、控件不遮挡、键盘焦点/标签可用 |
| 错误和回归 | 各页加载/失败/重试/401；现有信用付款409与未知结果恢复；后台限额、冻结、线下登记仍按权限与客户范围执行 |

后续开发需要隔离PG集成测试和真实API浏览器证据；typecheck/lint/build仅作为代码检查。无需真实收付款、上游资源或NOKVM条件来完成这批客户只读门户验收。

本批已按上述清单完成隔离PG18全量、相关race、Go/前端检查及146张浏览器截图摘要核对；105页#15仍部分完成，总体目标继续active。
