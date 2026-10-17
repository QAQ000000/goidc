# 补账后充值外部退款契约（2026-10-17）

本增量只扩展迁移148已实际补入余额的充值整单外部退款，承接[充值冲正契约](external-refund-reversal-contract-20261016.md)。不调用真实商户，不执行部署。订单/服务、续费、升级、信用、流量、佣金和部分退款仍拒绝；不以账单改为 refunded 推断任何资源已取消。

## 可接受事实链

原支付必须唯一成功全额收款、与客户和账单相同，账单仍 paid、invoice_kind=topup；balance_topups 必须 credited。以下二者之一才证明实际入账：

1. 原 settlement=credited，非空 ledger_id 指向同客户、同账单、同金额的正向 gateway_topup/topup 流水，且不存在独立补账事实。
2. 原 settlement=pending_liability、ledger_id=NULL；不可变 balance_topup_liability_resolutions 必须绑定相同 topup、invoice、payment、user 和 amount，其 ledger_id 指向上述同等正向流水。

迁移153新增只读 `external_refund_topup_credit_valid`，供 Store 申请/确认与149预约触发器共同校验，避免资格漂移。不修改148原收款失败事实、补账事实和正向流水；仅 topup.status=credited 不足以获得资格。未补入余额仍 `external_refund_topup_not_credited`，不存在新退款、预约或操作记录。

## 资金和权限

沿用149：申请在客户资金锁内冻结全额可用余额；已消费/余额不足拒绝，驳回释放，独立确认原子扣余额、追加负向 refund 流水、应用预约、账单 refunded 和审计。负向流水沿用 `external-refund-balance-{refund_id}` 独立键，绝不复用补账/原充值键。确认前非法消耗预约仍明确拒绝，不部分扣款；审计错误整体回滚。

保留既有浏览器会话、三权限、客户范围、申请人禁止自审、版本与请求载荷绑定幂等。未知结果前端保存原键/版本/载荷，仅用原请求查询结果；本增量不改变恢复流程。原成功回调重放不能重复补账、恢复 paid 或丢失 pending_liability 原结算事实。

查询和写入统一销售范围共享 advisory → 员工/角色锁 → 新语句重读范围 → 客户资金锁 → 原支付 → 账单 → 预约。销售分配先获取范围锁，故不能先拿员工锁；范围重读防止员工行锁等待后 SQL 函数旧快照继续放行。权限/范围撤销后包含幂等重放也拒绝。

前端只放开 credited 且补账投影金额匹配、ledger_id>0 的充值；后端完整链路仍是权威。接口/权限/字段/状态和公共账务统计口径不新增。
