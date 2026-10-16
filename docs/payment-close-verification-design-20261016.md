# G02 关单未知结果核验闭环设计审查（2026-10-16）

状态：待主代理汇总契约和批准实现；本分支只有设计文档，不改数据库、接口或运行配置。基线 `b47b435`。

## 推荐范围

员工对原 `unknown` 或已过租约 `checking` 关单发起只读核验，由编译进二进制的适配器查询**原关单操作**的最终结果。可信最终关闭/已收款结果只形成待复核证据；另一位员工复核后，分别原子应用关闭或复用正常可信收款结算。人工备注、截图、勾选和未付款查单永远不能替代适配器证据。无适配器能力、结果未知、原密钥缺失、身份关联不符均继续冻结。

本批不解决 `payment_close_anomalies` 的关闭后收款异常；那是已经发生的资金差异，应单独做退款/补账闭环，不能借核验抹去异常。

## 可选适配器契约

```go
type CloseStatusRequest struct {
    OperationID       string // 只读原外部关单号，不得生成新号
    ProviderPaymentID string
    AmountCents       int64
}
type CloseStatusResult struct {
    OperationID           string
    ProviderPaymentID     string
    AmountCents           int64
    Status                string // closed | succeeded | unknown
    ExternalTransactionID string // succeeded 必须非空且严格校验
    ProviderEvidenceID    string // 可选渠道侧只读证据标识，固定长度白名单
}
type CloseStatusGateway interface {
    Gateway
    LookupClose(context.Context, CloseStatusRequest) (CloseStatusResult, error)
}
```

- 能力名 `lookup_close`。不嵌入 `CloseGateway`，使只读核验调用方无法获得“再次关单”的必需入口；实现可同时具备两种接口。
- `closed` 必须代表原操作已经使该支付最终不可再收款；`succeeded` 为同一原商户、支付、金额的真实收款。一般订单查询的 `pending/status=0`、404、超时或查询不到原操作都只是 `unknown`。
- 请求必须用原凭据快照，禁止回落当前商户配置。适配器负责验证商户关联、TLS/签名/协议；Store 严格复验操作号、支付号、金额、流水及原凭据摘要。真实 Epay/MPAY 不支持此协议，保持不支持。
- 显式模拟环境可用绑定完整三元组的已记录只读结果进行测试；不能给 Mock 增加“任意操作号查出来都 closed”的默认推断。重启丢失模拟证据应返回 unknown。
- 数据库仅保存规范化白名单结果和摘要，不保存原始响应、商户密钥、签名、URL 查询串或错误文本。

## 迁移 151：追加事实，原操作不改写

建议 `151_payment_close_verifications.sql`，三张表：

1. `payment_close_verification_requests`：原 `close_operation_id`、payment/invoice/customer、provider/原实例/原支付号/金额/原 operation_id/credential_sha256、发起员工、expected_invoice_version、员工幂等键和请求哈希、created_at、短租约。仅插入，不更新/删除。请求创建后即是执行 fence，网络调用在提交后进行。
2. `payment_close_verification_results`：每请求至多一条；`closed/succeeded/unknown/unsupported/error`、固定错误码、可信结果白名单、结果摘要、received_at。结果插入也需在原支付行锁下核对请求租约与关联。无结果但租约过期，投影为 expired，原请求仍不改写。
3. `payment_close_verification_decisions`：每请求至多一条最终 `applied_closed/applied_paid/already_paid/rejected/superseded_paid`；原请求/结果引用、独立复核员工、当前expected_invoice_version、复核幂等键和哈希、note、applied_at，应用后的invoice version，必要的稳定结算事件ID。只插入，不更新/删除。

数据库约束应包括：

- 请求员工+幂等键唯一；结果request唯一；decision request唯一；**每个原close_operation至多一个有效applied_closed/applied_paid/already_paid/superseded_paid决策**。reject不能屏蔽后续重新只读核验。
- 所有请求/结果/决策不可更新或删除；关联触发器校验完整payment/invoice/customer/provider/instance/operation/amount/digest。复核人不得等于核验请求人；建议也不得等于原关单员工，避免原操作人自证。
- 不能用部分唯一索引 `lease_until>now()`；并发预约在原支付行锁下检查最近无结果活跃请求，过期请求仍保留，可再建新请求。
- `require_trusted_payment_close` 同时接受：原可信closed操作，或关联完备的 `applied_closed`独立复核事实。两条分支都核对原实例与凭据摘要，不允许仅插入孤立decision解锁；约束继续延迟到事务结束。
- 原 `unknown` 永远不改；原 `checking` 在租约失效后也不可UPDATE/DELETE。151收紧现有 `protect_payment_close_operation`，阻止过期结果或 FailPaymentClose 回写旧行；超时/迟到响应只能另存新核验事实。
- 旧关联凭据密文非空时已有087迁移不可变保护。对历史空快照的真实渠道，核验必须拒绝fallback；不能把空字节digest匹配误当原商户证据。

## API 与 DTO

所有路径沿用稳定 `/api/v1/admin/invoices/{id}/payments/{payment_id}`，浏览器会话专用、Origin校验、认证前no-store。

- `GET .../close-verifications?limit=20&offset=0`：真实 `items/total/limit/offset`；权限 `invoices.view`。包括原操作号、有效冻结原因、lookup_supported、can_request、最近核验请求/结果/复核状态及can_confirm/can_reject；密钥/digest/key/hash/session不回显。
- `POST .../close-verifications`：权限 `payments.close_verify`，body `{close_operation_id, expected_version}`，必须Idempotency-Key。金额/商户/状态不能由前端传入。可选note只作审计说明，不作证据。创建后只执行一次LookupClose，同键重试返回已存结果或进行中，**不再次lookup或Close**。想重新查询需要新键和新请求。
- `POST .../close-verifications/{verification_id}/confirm`：权限 `payments.close_review`，body `{expected_version, note}`，必须新Idempotency-Key。根据存储可信结果决定closed/paid，前端不得提交target_status/result。与发起人/原关单员工不同，事务中复核权限、会话、角色启停和销售范围。
- `POST .../close-verifications/{verification_id}/reject`：同review权限和独立性。只追加驳回事实，继续冻结；没有“拒绝后解除保护”语义。

DTO推荐：`id/close_operation_id/payment_id/invoice_id/requested_by/requested_at/status/result_status/result_code/received_at/review_status/reviewed_by/reviewed_at/can_confirm/can_reject/version/replayed`。结果证据只显示白名单字段和外部流水，固定状态文本由前端映射。

状态：`checking/expired/unknown/unsupported/error/awaiting_review/applied_closed/applied_paid/already_paid/rejected/superseded_paid`。request/result/decision的原事实可以分开返回，聚合status只是投影，不能回写原表。

建议响应：预约+已取得结果为201；同键重放200；进行中202；版本/幂等/资格/不支持/无可信证据/已经复核409；未知网关结果502但返回可查询的request_id（统一错误结构扩展details，或前端随后GET读取）；禁止把unknown作为可审批候选。所有审批返回最新invoice version，不用旧operation expected_version+1猜测。

## 权限

新增 `payments.close_verify`（只读核验并登记可信候选）、`payments.close_review`（独立复核并应用），均要求 `invoices.view`，既有角色不隐式扩权。`payments.close` 仍只用于原关单动作，不自动获得新权限。独立复核限制对super_admin同样有效。核验者可与原关单员工相同，但复核者不能等于二者任何一位。

不要求复核人在应用时额外持有 `payments.reconcile`：`close_review` 必须明确表示“复核关闭或可信收款结算”，UI需展示资金影响。若产品希望拆开两类应用，再另加权限；本批不建议暗中使用更弱旧权限。

## 核验与应用流程

预约/记录结果/复核应用都先销售范围共享advisory，再锁当前员工/会话/角色并复查范围；充值则取得客户资金锁，之后支付→账单→原操作/核验请求→决策。不能复用当前 `processPaymentWebhook` 的“资金锁在前、随后管理员授权”的外层来引入锁序反向。

预约只允许unknown或过期checking，原支付pending/expired、账单unpaid、没有late anomaly、没有活动正常查单或核验；核验不受原关单expected_version旧值永久束缚，而使用当前账单版本创建新请求。操作号/支付/金额/digest来自原操作，不能重新发Close。原checking仍在租约内时不可核验。

结果入库只形成候选：核验请求租约有效、原关联/digest匹配，当前员工仍有效，否则不产生可批准证据；可保留固定authorization_lost等失败事实，始终不放款。关联查询遇到超时、撤权、落库失败，原冻结不变。同键不能发第二次lookup；租约过期后新键可以再只读核验。

复核应用closed需当前账单unpaid、原支付pending/expired、无anomaly、无其他有效核验、可信结果确为原operation最终closed，凭据和范围未变；原子写decision+支付closed+账单版本+审计。若成功回调先提交，不能关闭：追加superseded_paid（已核实同一成功流水）或409，支付与账单保持成功。若复核先关闭，后来的成功回调仍走既有late anomaly，绝不重复入账。

复核应用succeeded须重用正常支付结算的**事务内部函数**：提取原 `processPaymentWebhook` 的验重复/验证关联/金额/成功状态/账单审核/充值或业务结算/通知/佣金/支付流水/审计为接收已打开tx的函数。顶层通知、正常查单、核验复核分别做自己的入口授权与fence，然后在同一事务调用该函数。稳定事件ID例如 `close-lookup:<result_id>:<external_transaction_id>`，与decision一起原子提交，禁止先commit复核再调用独立Store结算事务。禁止伪造普通PaymentCheck或调用无权限wrapper绕过审核。

若付款已被回调结算，同流水只记录already_paid，不再入账；不同流水/账单非未付/异常事实继续冲突冻结。充值结算沿用原pending_liability保护；不能把“已确认收到款但余额入账失败”当成closed或删除原证据。失败事务回滚decision，可信result保留供当前状态再次核验，不伪称已处理。

## 有效冻结与共享代码

必须统一 `paymentCloseUnresolvedPredicate`（推荐DB稳定函数+Go SQL引用，避免复制十处），语义：原支付仍pending/expired，存在checking/unknown关单且没有该原操作的有效已应用决策。late anomaly独立恒定阻断。成功回调先赢后，旧unknown历史不应继续把paid账单显示为“未完成付款”。

共享修改点：

- payment：CloseStatus接口、能力、测试用只读协议适配器；Epay保持不支持。
- store/payment_close.go：旧过期原操作不再更新、新核验Store与会话/范围通用授权参数化。
- store/payments.go：结算函数提取和稳定事件幂等、requireNoRealPaymentPending新predicate。
- store/payment_reconciliation.go、payment_monitoring.go：只拦原关单**有效未过期**checking；允许过期checking后的正常只读查单发现成功，不得将其当作关闭证据。
- store/orders.go、order_mutations.go、httpapi/traffic.go：资金/取消/删除保护统一effective unresolved，避免旧行永久阻断。
- transaction_details.go、GET close历史：新增lookup和review投影、有效close_frozen、can_retry/cancel等保持一致，保留原操作status。
- admin_permissions/server/admin_rbac：新路由、浏览器会话、权限依赖和既有角色不隐式扩权测试。
- web payment-close/online-payment/topup：核验列表、发起和独立确认/驳回、unknown和租约状态、版本冲突与幂等恢复；没有人工状态输入。

## 本次静态审查发现

1. **过期checking阻断所有补偿查单**：`payment_reconciliation.go:86`和`payment_monitoring.go:35`仅查status不查lease。进程在外部关单后、落库前崩溃，会永久阻断只读发现成功。闭环应允许原行不变的read lookup及正常只读支付查询。
2. **原unknown即使后续已成功也继续冻结**：`transaction_details.go:188`及多个funds/delete predicate直接EXISTS旧状态。加decision若不同时换predicate，trusted closed也仍无法取消/删除，UI继续显示冻结。应基于支付当前终态和新应用事实投影。
3. **过期原行当前仍可被写**：150的immutable trigger只限制OLD.status不是checking；`FailPaymentClose:288`不检查lease，可在过期后改写原checking。与本批“过期原行不可变”不符，151需收紧并修改失败记录路径，不能靠人工更新旧行为unknown。
4. **可信关闭证据触发器关联不足以支撑新流程**：150函数只查原op.closed，不比对instance/digest，也不接受独立复核。必须扩展完整关联约束，不能临时关掉触发器或把旧unknown改closed。
5. **未来适配器的空快照fallback风险**：`payment_expiry.go:98`对空快照回落当前配置；这是旧兼容行为，不能用于恢复原关单的可信lookup。当前Epay无lookup，因此尚无可触发的错误解锁路径，但新增能力必须显式拒绝。
6. **外部已调用后的state_changed被记为error**：`CompletePaymentClose:228-235`对调用后账单版本变化记录error，而预约busy只拒checking/unknown，未来支持真实Close的适配器可能被新键再次调用。建议本批统一将“已发送但不能原子应用”的结果保留为待核验冻结事实，区别真正未发送的provider_unavailable；不要把已发请求当成可重新发起Close。

以上为源码静态发现，未执行真实商户、数据库迁移或浏览器测试。

## 实施验收建议

隔离PG18验证151重放和DB约束；unknown与过期checking不可写；lookup请求同键不二次调用、换键只发生只读查询，Close调用计数始终0。两种可信结果、同/异流水、回调先赢/后到、原凭据缺失与实例变化、未付款/404/超时/unsupported全部覆盖。

权限/会话/范围在预约、结果入库、复核三个时间点撤销；自审、原关单员工复核、super_admin自审、API Key、客户均拒绝；8路并发核验/审核只有一个有效事实。审计失败、settlement失败、topup待补账和退出后重放不重复余额/佣金/开通。有效冻结消除只取决于新可信应用，不改历史unknown；late anomaly仍不解冻。最后复用统一PaymentException夹具做四尺寸及403/409/丢回执恢复验收。
