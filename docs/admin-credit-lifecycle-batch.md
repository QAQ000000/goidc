# 信用消费与线下还款闭环

独立信用账户保持预存余额隔离。消费需客户明确提交账单版本；复用余额支付的账单类型/审批/真实支付pending检查与订单开通、续费结算、通知及返佣链路，资金来源仅为授信占用。账单锁→信用账户锁；授信不足/冻结拒绝，充值和不支持的账单拒绝。

消费记录保留原金额与剩余债务。管理员线下确认还款必须填写真实唯一到账凭证与到账时间，逐消费记录归还，不能仅删除债务。还款记录独立于余额流水。冻结只阻止新信用消费，不阻止偿债。

退款复用现有订单/库存/续费撤销规则。完全未偿还的信用消费退款冲减债务，绝不增加余额；已发生任何线下还款的消费禁止自动退款，需人工核实线下退付，当前不提供已还款现金退款闭环。线上还款尚未实现。

推介返佣在信用消费时不创建佣金。QueueAffiliateCommissions 对未完全还款信用账单设全局门禁，实际还款全部到账后才按既有规则生成佣金；销售提成预览与阶梯营业额排除未偿还信用账单。销售结算本身仍是既有只读预览，不新增员工现金发放能力。

API：客户 POST /invoices/{id}/pay-credit（expected_version、Idempotency-Key）；后台 GET /admin/credit/consumptions、/repayments；POST /admin/credit/consumptions/{invoice_id}/repay（整数分 amount_cents、消费 expected_version、唯一 receipt、received_at、note）；PUT /admin/credit/{customer_id}/freeze（frozen、账户 expected_version、note）。写入口均为会话认证，管理员写还需 credit.manage 与事务内角色/会话/客户范围复核；退款用 invoices.refund。

三个标签支持真实分页、客户ID/日期筛选与URL恢复，消费状态为待还/已还/已退款；还款日期按实际 received_at（上海日期边界）。账单安全投影包含 credit_payment 金额/剩余/状态，后台信用退款标为撤销债务并明确不加余额，发生还款后普通退款按钮不可用。冻结、还款、信用支付未知结果保留 actor-scoped sessionStorage 请求键/内容，重试使用原版本，未确认前禁用修改或其他资金方式。

验证：隔离 PostgreSQL 18 migration129/HTTP专项与race，包含真实消费、还款/重放、余额不变、未还退款、已还退款拒绝、冻结拒付、信用续费到期更新、余额/信用并发只成功一次、审计回滚、范围/撤权、真实支付pending拒绝、筛选校验与全后台路由权限覆盖；前端typecheck与改动组件lint通过。主代理负责全量构建及浏览器验收。
