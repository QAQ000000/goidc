# 外部退款到账登记与关联冲正契约（2026-10-16）

状态：后端可验证核心已实现，隔离PG18/race与fixture生命周期通过，见[后端验收](external-refund-reversal-backend-acceptance-20261016.md)；主线全量及前端浏览器由集成验收另列。本契约承接 `external_refunds` 第一增量，限定在“原商户已实际退款、证据已由人工核验”的后台登记流程；不调用商户退款接口，也不把本地过期或管理员口头确认当作退款到账证据。

旧版事实参考：`/www/wwwroot/zjmf-manger-decoded/app/admin/controller/InvoiceController.php:1151` 以原 accounts 收款/退款合计约束金额，区分 `addascredit` 与 `only`，`only` 记外部出款并把账单改 `Refunded`；`ClientsServicesController.php:1452` 另有按天/全额/自定义的资源退款。Go 保留原收款与出款分开追溯的语义，但不照搬未核验到账、浮点金额、只改资源退款记录的宽松状态操作。

2026-10-17 后续单一增量：[补账后充值整单外部退款契约](resolved-topup-external-refund-contract-20261017.md)扩展了下表“已由迁移148补入余额”的拒绝边界；未补账仍拒绝。旧批次描述保留为历史验收范围。

## 目标和不变事实

登记请求只保存原支付、客户、账单、金额、商户身份快照、商户退款流水和证据摘要。`pending_review` 不是出款事实；只有独立员工确认后的 `confirmed` 才能改变本地账务。相同幂等键重放原响应，载荷变化返回 409；账单版本、会话、权限、客户销售范围和申请人与复核人都在资金事务内重新核对。审计失败或任何冲正失败必须整体回滚。

原支付成功事实、支付完成时间、原收款流水和原商户交易号不可改。退款不是余额充值，不能把外部退回误记为客户余额增加；账单状态以 `refunded` 为权威，查询和列表不能继续按 `paid` 展示。确认、驳回和补正历史都不可覆写。

## 关联业务分类

| 账单关联 | 本批策略 | 资金/状态规则 |
| --- | --- | --- |
| 在线充值 `invoice_kind=topup` 且结算为 `credited` | 实现 | 登记时在客户资金锁内冻结等额可用余额；冻结不足拒绝。驳回释放冻结；确认原子扣余额、写一条负向 `balance_ledger`（`entry_type=refund`、`reference_type=invoice`、独立幂等键）并写不可变冲正事实。充值结算和原支付保留。 |
| 原结算 `pending_liability`（包括后来已由迁移148补入余额） | 拒绝 | 本批只支持原结算直接入账；后补账有独立解决事实，不能仅凭 `balance_topups.status=credited` 推断原ledger，后续专项支持。不能把商户退款伪造成扣余额。 |
| 信用付款 | 拒绝并记录原因 | 信用消费没有可核验的原支付收款关联；现有信用退款只处理本地信用债务。不能把外部退款登记当作还款或直接改 `used_cents`。 |
| 订单/服务、续费、升级、流量、佣金关联 | 保守拒绝 | 供应商资源、服务交付、库存、续费日期和佣金需要逐项核验；本批不把“账单 refunded”当作资源已取消。后续必须先有上游取消/同步事实，再实现可回滚的业务冲正。 |
| 普通人工账单 | 沿用第一增量 | 只更新账单为 `refunded`，保留原支付和收款，不产生余额流水。 |

## 余额冻结与冲正

新增 `external_refund_reversal_reservations`，一条活动预约唯一绑定一条退款事实/账单。预约行保存客户、金额和状态（`pending`、`released`、`applied`）；申请和审核沿用员工/范围→客户→支付→账单→退款预约的固定顺序加锁。所有余额扣减路径必须考虑活动预约，不能让客户消费冻结金额。确认时校验预约仍为 `pending`、余额不少于金额，然后在同一事务扣减用户余额、追加负向流水、把预约置为 `applied`，并写审计；失败不更新退款状态。驳回只把预约置为 `released`，不改账单、支付或余额。

冲正流水不能复用原充值流水的幂等键；重复确认只能重放同一操作结果，数据库唯一约束阻止重复负向流水和第二条活动预约。若确认前发现余额已被非法或旧路径消耗，返回明确 `external_refund_balance_reserved`/`external_refund_balance_insufficient` 并保持待复核，禁止部分扣款。

## 接口与权限

沿用：

- `GET /api/v1/admin/invoices/{id}/external-refunds`（`external_refunds.view`）
- `POST /api/v1/admin/invoices/{id}/external-refunds`（`external_refunds.create`）
- `POST /api/v1/admin/invoices/{id}/external-refunds/{refund_id}/confirm|reject`（`external_refunds.review`）

不新增前端或运行时插件接口；响应可增加只读 `reversal` 白名单（状态、冻结金额、流水编号、应用时间），不得返回内部凭证或密钥。所有写入要求有效浏览器会话、幂等键、`expected_version`、确认说明和客户范围；使用 API key、过期/撤销会话、无权限员工、申请人自审均拒绝。

## 验收和边界

- 隔离 PG18：充值已入账→申请冻结→余额消费/后台加款并发→驳回释放；确认扣余额/负向流水/账单状态/原支付保留；同键重放、异参 409、不同键重复、版本冲突、审计失败回滚。
- 隔离 PG18：pending liability、信用、订单、服务、供应商续费和有效发票申请均拒绝且无部分事实；普通人工路径回归。
- HTTP：权限/会话/客户范围、幂等、409、错误码及真实状态投影；不把 `paid` 作为已退款展示。
- race：客户余额消费、管理员加款、补账和外部退款申请/确认固定锁序无死锁、余额守恒。

本批不证明真实商户退款、自动原路退款、复杂供应商资源销毁、部分退款或实际生产切换；这些保持待验收或独立批次。
