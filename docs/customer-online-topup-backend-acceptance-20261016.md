# 客户在线充值后端与资金边界（2026-10-16）

## 接口与状态

客户浏览器会话专用：`GET /api/v1/balance/topup-options`、`POST /api/v1/balance/topups`、`POST /api/v1/balance/topups/{invoice_id}/cancel`。创建 JSON 为 `amount_cents,expected_version`；取消为 `expected_version,note`；写操作必须带 8–128 字符 `Idempotency-Key`。API key、管理员、跨客户资源不允许充值。

选项返回开关、最小/最大分、余额上限、当前余额、`reserved_cents`、可接纳容量（无上限时 null）、订单条件和设置 `version`。`reserved_cents` 包含 `accepted` 预留及尚欠客户的 `pending_liability`，防止连续入账异常后无限接纳。

创建保存 `balance_topups` 与 `invoice_kind=topup`，不预留资金；相同用户幂等键同参返回原单，异参 409。创建版本固定：首次真实支付意图时设置版本变化返回 `version_conflict`，允许取消 created 后重新创建；不静默以新条件接纳。支付使用既有账单接口，额外强制有效客户会话，禁止 mock、余额和信用。首次接纳在客户资金锁下校验当前资格和余额加预留上限，保存设置与资格快照。`pending`、本地 `expired`、失败通知均不释放接纳额度；保留原支付核对。

仅 `created` 且没有任何支付记录可以按账单版本幂等取消。`accepted` 不支持客户取消、人工退款、外部退款登记、合并或发票申请；充值不生成订单/服务/资源任务/佣金。

## 收款与负债

已准入充值在商户可信成功通知或可信查单返回后，使用原支付事务原子处理成功收款、入余额、唯一账本、充值结算事实、审计和既有支付通知。后续设置关闭、限额降低、订单失效不否定已准入收款；即使之前收到失败通知，迟到真实成功仍兑现。`payment`、充值单和账本的唯一约束保证不同事件 ID 对同一原交易只入账一次；不同外部交易号仍按原网关的一致性校验拒绝，不能伪作同一笔钱吞掉。

入余额使用 savepoint。余额溢出或账本写失败会回滚局部余额变化，保留成功收款并创建不可修改的 `pending_liability` 结算事实；不会显示为余额到账。该负债继续占用额度。账单详情 `topup` 返回状态、设置版本、接纳时间、失败代码、原支付 ID、账本 ID 和金额；账单列表返回 `topup_status`、`topup_pending_liability_cents`，避免负债藏在 paid 状态下。第一增量没有负债人工解决入口，须财务核对，不可重复收款替代原款。

## 锁序审计

- 客户创建/取消：客户 `FOR NO KEY UPDATE` → 会话/设置共享锁 → 充值/账单。
- 首次充值 intent：无锁分类读取 → 客户资金行 → 支付配置/操作幂等锁 → 账单 → 充值 → 新支付。
- 充值回调/成功查单：无锁分类读取 → 客户资金行 → 查单授权（若有）/事件幂等锁 → 原支付 → 账单 → 充值 → savepoint 余额及账本 → 结算与通知。
- 余额/信用消费及退款：既有客户资金行 → 账单 → 信用/债务/服务；充值在账单分类后显式拒绝这些付款/退款路径。
- 管理员 TopUp：销售归属共享 advisory → 设置共享行 → 客户行 → 账本；该路径不等待支付或账单行；额度同时统计已接纳和待处理负债。
- 外部退款：销售归属/客户 advisory → 客户资金行 → 原支付 → 账单；只接纳 manual 账单，充值不可进入。
- 人工查单 Begin/未成功结果：支付配置共享 → 客户授权共享 → 原支付 → 查单租约；自动 claim/本地过期只持支付/租约锁，不等待客户资金行。成功结果统一进入充值回调锁序。未全局改动非充值回调，避免扩大行为范围。

## 财务口径

保留原仪表盘/年度收入/销售收入的消费口径，仅排除充值账单；后续余额购买依旧计消费收入。订单销售佣金报表同样排除充值。既有交易现金汇总只计成功 gateway payment 和外部退款；充值现金计一次，余额到账流水 channel=`gateway_topup`、direction=`neutral`，不能与现金收入相加。客户余额收支表仍为账户变动口径。客户资金历史的在线充值入账有独立 label 并可追溯原账单。

## 验证边界

隔离 PG18 每项测试创建随机 schema 并完整运行迁移/校验；数据库名强制 `_test` 后缀，未接触业务数据库。资金测试覆盖并发不同单额度、管理员加款竞争、余额消费/回调/可信查单竞争、不同事件重复成功、设置变化后的迟到款、失败后成功、取消幂等、会话与支付方式限制、强制账本失败 savepoint 回滚、余额溢出负债及负债继续占用额度。测试使用虚构商户凭据与本地 Store 调用；没有外部支付、部署或真实资金动作。HTTP/浏览器验证由独立集成验收补充，不能据此声称真实扫码/渠道查单已通过。

已执行证据（本隔离分支）：`REQUIRE_INTEGRATION=1 go test -race ./internal/store -run 'TestOnlineTopup|TestTopUp|TestExternalRefund' -count=1 -timeout=5m` 通过（31.564 秒）；`go test ./internal/store -count=1 -timeout=5m` 通过（36.918 秒）；针对 dashboard/credit cash/customer scope/sales/external refund/transaction 的 HTTP 回归通过（27.547 秒）。溢出用例另行读取实际 `server_version_num=180000` 并通过；所有充值集成 fixture 强制检查 PG18。`git diff --check` 通过。HTTP 完整新流程与四尺寸浏览器结果另见集成记录。
