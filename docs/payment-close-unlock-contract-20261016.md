# 支付可信关单与安全解锁契约（2026-10-16）

状态：后端实现及隔离回归完成。本契约只覆盖在线支付意图在“未付款且上游已经确认不可再收款”时解除本地付款保护；它不把本地 `expired`、查询返回未付款、404、超时或人工勾选当成上游关单证据。真实商户关单、真实资金到账和生产切换仍需单列验收。

## 目标与边界

- 保留现有支付成功事实、历史商户凭据快照和迟到回调入口。安全解锁只改变对应原支付意图的终态，不删除订单、账单、回调事件或审计记录。
- 只有编译进 Go 的支付适配器声明 `close_intent` 能力，并同时返回经过校验的最终 `closed` 结果，才允许走解锁流程。没有能力的易支付/MPAY 实例继续返回“渠道不支持可信关单”，账单付款保护保持冻结。
- `pending` 和 `expired` 都需要远端确认；本地过期时间只影响重试调度，不参与解锁判定。
- 网络超时、连接取消、非 2xx、格式错误、未知状态和“仍未付款”都保存为非终态结果，不解锁。未知结果不得自动重试同一外部操作号。

## 适配器契约

支付注册表增加可选 `CloseGateway`：

```go
type CloseRequest struct {
    ProviderPaymentID string
    AmountCents       int64
    OperationID       string // 每次外部关单尝试的稳定操作号
}
type CloseResult struct {
    ProviderPaymentID     string
    ExternalTransactionID string
    AmountCents           int64
    OperationID            string
    Status                 string // closed 或 succeeded；只有 closed 可解锁
}
type CloseGateway interface {
    Gateway
    QueryGateway
    Close(context.Context, CloseRequest) (CloseResult, error)
}
```

适配器必须保证：请求使用原支付凭据快照；`OperationID` 可在渠道侧幂等；`closed` 的语义是该订单已被上游拒绝/关闭且不可再付款；`succeeded` 表示已有收款，仅记录为收款结果并保持保护；后续签名通知或可信查单继续走原结算流程，不能解锁。适配器不能把 `status=0`、404、受理中或本地倒计时映射为 `closed`。能力目录仅在实现实际满足契约时声明 `close_intent`。

## 数据事实与状态机

新增不可变证据表 `payment_close_operations`：每次尝试绑定原 `payment_id/invoice_id/user_id/provider/provider_payment_id/amount_cents`，保存原实例标识、原凭据密文 SHA-256、请求摘要、外部操作号、结果状态、固定内部结果码、开始/完成时间、执行员工、请求幂等键和审计关联。状态为 `checking`、`closed`、`succeeded`、`error`、`unknown`；`checking` 有短租约且每笔支付至多一个活动操作。请求哈希和结果 JSON 只保存白名单字段，禁止保存商户密钥、原始响应或网络错误文本。

关单预约及完成先取得销售范围共享锁和员工/会话/角色锁，再取得充值客户资金锁（如有），之后按支付行、账单行、操作行的顺序加锁，再次核对：

1. 原支付仍是 `pending` 或 `expired`，账单仍是 `unpaid`，金额、客户、渠道和凭据快照未变。
2. 当前操作者仍是有效浏览器会话、拥有 `payments.close` 且通过销售范围；权限撤销、员工停用、客户转交均拒绝解锁。
3. 自上次预约后没有成功回调、成功查单、退款或其他活动关单操作；成功收款优先于关单结果。
4. 适配器结果的支付号、操作号、金额和 `status=closed` 全部匹配。

通过后将支付意图置为 `closed`，写入固定 `completed_at`、白名单 `raw_result` 摘要和 `payment.intent.closed` 审计；账单仍保持 `unpaid`，之后可创建新的支付意图。原支付历史永不删除。并发成功回调必须先赢：若回调已提交，关单事务返回冲突并保留 `succeeded`，不产生解锁。

迟到成功回调命中 `closed` 支付时，验签、金额、账单和商户快照仍正常校验；事件记为 `ignored`，写入 `payment.webhook.ignored_after_close` 审计并返回成功响应，绝不重复结算、开通、佣金或余额入账。同时写入不可变的 `payment_close_anomalies` 待核验资金事实，绑定原支付、账单、客户、金额、外部流水和载荷摘要，并冻结新付款、余额/信用结算、账单编辑/作废/合并、订单/流量包/充值取消和订单软删除。当前不提供异常消除/退款入口，必须保持冻结，不能凭人工勾选抹掉事实。

## HTTP 与权限

- `GET /api/v1/admin/invoices/{invoice_id}/payments/{payment_id}/close`：要求 `invoices.view`，返回账单版本、原支付状态、可关单能力、最近 50 条操作和异常及各自总数；不返回密钥、凭据摘要或幂等键。认证失败也设置 `Cache-Control: no-store`。
- `POST /api/v1/admin/invoices/{invoice_id}/payments/{payment_id}/close`：只允许管理员浏览器会话，要求 `expected_version` 和 `Idempotency-Key`，请求体不得包含金额、客户或渠道（全部从数据库读取）。
- 新权限 `payments.close` 独立于 `payments.reconcile`，既有角色不自动获得。后台权限中间件先拦截，事务内再次核对角色、会话和客户销售范围。
- 同一员工和幂等键同参返回原结果；同键异参返回 `409 idempotency_conflict`。同支付并发请求返回 `409 payment_close_in_progress`。版本冲突、已成功收款、非未付款账单、非在线支付和未声明能力均为明确 `409`，不得改变资金事实。
- 真实结果未知返回 `502 payment_close_unknown`，原支付和付款限制继续保留；请求记录为 `unknown`，只能使用新的人工核验流程，不自动标记失败或解锁。
- 成功返回 `200`，包括 `payment_id/invoice_id/status=closed/version/operation_id`，不返回密钥、签名、原始网关响应或内部说明。

## 验收要求

- 隔离 PG18：迁移可重放；成功收款先于关单、可信未付款才关闭；查询未付款、本地 expired、404、超时、未知和审计失败均不解锁；关闭后创建新意图可行。
- 权限/范围/会话撤销、员工停用、客户转交、版本冲突、同键重放与异参、并发重复提交、活动查询租约、迟到回调和数据库回滚均有断言。
- 适配器单测覆盖结果字段严格匹配、操作号稳定、密钥不出日志；易支付当前未声明 `close_intent`，因此真实 MPAY 不得被标记为可关单。
- 仅使用隔离测试网关，不调用真实关单、退款或生产写接口；真实商户最终关单和资金闭合另附证据后才能关闭 G02。

## 本批实现范围

- 迁移 `150_payment_close_unlock.sql`：支付 `closed` 状态、不可变操作证据和迟到资金事实，延迟约束拒绝没有关联关单证据的直接关闭。
- 当前只有显式开启的模拟支付实现 `CloseGateway`；易支付/MPAY 没有可信最终关单协议，仍不实现此能力，不发送虚构远端关单请求。
- 已接纳充值在原支付全部可信关闭且无结算/异常时，可按原接纳快照重试支付或取消；重试复用原额度预留，不重新接纳当前政策或重复占用额度。
- 账单支付详情提供 `close_supported/close_enabled/close_status/close_code/close_requested_at/close_anomaly/close_frozen`；账单提供 `payment_close_anomaly/topup_can_retry`。`close_enabled` 同时要求员工权限、能力、原支付待处理和无冻结异常，页面不得仅凭 `close_supported` 展示可执行操作。
- G02 仍需真实具备最终关闭能力的适配器及商户验收；本地模拟协议、迟到异常登记和保护不等于真实资金闭合。
