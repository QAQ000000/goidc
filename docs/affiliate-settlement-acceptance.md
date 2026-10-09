# 推介返利第二阶段验收

本批完成资金生命周期收口和提现/线下打款审核流。迁移 100_affiliate_settlement_withdrawals.sql 增加分桶式佣金、提现申请/分配、审核、打款、结算租约、冲正待追缴和审计时间线；不修改 users.balance_cents，不调用银行接口。

worker 每分钟抢占结算 run，按 hold_days 释放 pending 佣金，按账单、订单、服务状态和待核验任务复核；失败条目单独记录并支持租约恢复。退款和订单取消在原事务中追加 pending/available 冲正，reserved/paid 生成 clawback，后续 available 佣金优先抵扣。

客户可创建和查询提现；管理员按 affiliates.withdrawals.review 审核，按独立 affiliates.payout 权限登记线下流水和 SHA256 凭证摘要。目标使用提现单 ID 绑定 AES-GCM 加密，列表/详情只返回脱敏快照；完整目标读取需要审计用途并禁止缓存。写操作带幂等键和版本校验，重复打款不会产生第二笔记录。

新增 internal/httpapi/affiliate_settlement_test.go 覆盖冻结释放、有限批次不饥饿、事务失败回滚、并发提现超额、驳回回退、pending/available/reserved/paid 四阶段退款冲正、clawback 抵扣、目标脱敏和重复打款。

独立 PG18 _test 库专项通过；./scripts/go.sh test -race ./internal/...、./scripts/go.sh vet ./... 和 API/worker/migrate 编译通过。迁移已应用到开发库，API/worker 已重启，GET /readyz 返回 200。真实支付、外部供应商和实际线下收款未现场验收。

本批前端已新增 `/console/affiliate` 客户控制台、`/admin/affiliate` 管理工作台和 `/r/{code}` 推广跳转页。客户页面覆盖概览、链接创建/复制/启停、佣金快照、最低提现额/手续费校验、幂等提现申请和提现时间线；管理页面覆盖账户审核、归因查询、冻结佣金来源、二次确认审核及带流水号/渠道快照/SHA256 摘要的线下打款登记。金额只在服务端以分保存，前端采用字符串分拆解析，拒绝超过两位小数。

使用开发库临时夹具完成真实浏览器链路：客户提交 50 元提现，管理员审核并登记线下打款，最终状态为 `paid`。Playwright 桌面/390px 截图位于 `output/playwright/affiliate-customer-1440.png`、`affiliate-customer-390.png`、`affiliate-admin-1440.png`、`affiliate-admin-390.png`；四个页面均验证 `document.documentElement.scrollWidth === window.innerWidth`。Next.js lint/typecheck/build 通过，`zjmfgo-web`、API 已重启并保持 active，`/readyz` 返回 200。夹具仅用于验收，完成后清理。
