# 推介返利第二阶段验收

本批完成资金生命周期收口和提现/线下打款审核流。迁移 100_affiliate_settlement_withdrawals.sql 增加分桶式佣金、提现申请/分配、审核、打款、结算租约、冲正待追缴和审计时间线；不修改 users.balance_cents，不调用银行接口。

worker 每分钟抢占结算 run，按 hold_days 释放 pending 佣金，按账单、订单、服务状态和待核验任务复核；失败条目单独记录并支持租约恢复。退款和订单取消在原事务中追加 pending/available 冲正，reserved/paid 生成 clawback，后续 available 佣金优先抵扣。

客户可创建和查询提现；管理员按 affiliates.withdrawals.review 审核，按独立 affiliates.payout 权限登记线下流水和 SHA256 凭证摘要。目标使用提现单 ID 绑定 AES-GCM 加密，列表/详情只返回脱敏快照；完整目标读取需要审计用途并禁止缓存。写操作带幂等键和版本校验，重复打款不会产生第二笔记录。

新增 internal/httpapi/affiliate_settlement_test.go 覆盖冻结释放、有限批次不饥饿、事务失败回滚、并发提现超额、驳回回退、pending/available/reserved/paid 四阶段退款冲正、clawback 抵扣、目标脱敏和重复打款。

独立 PG18 _test 库专项通过；./scripts/go.sh test -race ./internal/...、./scripts/go.sh vet ./... 和 API/worker/migrate 编译通过。迁移已应用到开发库，API/worker 已重启，GET /readyz 返回 200。真实支付、外部供应商和实际线下收款未现场验收。
