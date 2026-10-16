# 迟到收款责任退回后端验收

日期：2026-10-17。对应实施契约 B 与迁移 152；实现提交 `14cfdd2`、`b5fedfa`。本记录仅证明后端、隔离数据库和并发边界；浏览器验收由集成批次另记，未调用真实支付商户或实际出款接口。

## 行为

- 原迟到收款事件不改不删，按原支付与精确上游交易号分组，重复事件不增加责任金额。原商户身份从原加密凭据核验，数据库保存不可变的非密钥关联；API 不返回商户 scope、密钥、凭据摘要。
- 退回登记从原支付导出客户、金额、渠道与交易号；空号、不可核验号不可登记。申请人与复核人独立，超级管理员同样禁止自审。组版本变化返回 409，重读后可继续原申请。
- 普通退款和异常退回共用出款认领表，跨表唯一，待审保留认领、驳回释放、确认不可释放。同一原交易也不能被不同支付各自登记退回；原交易锁与数据库守卫同时保护先退回后回调、先回调后退回的竞争。
- 异常退回不改原账单、支付状态、客户余额、销售收支或交付。普通资金汇总保持独立；仅完整关联的已确认退回解除对应异常组，其他交易继续冻结。
- 保护条件使用 `payment_close_anomaly_unresolved`，历史事件总数仍保留，另提供未解决总数；写操作和回执原子落库，审计失败全部回滚。

## 已执行验证

使用专用测试连接与随机 schema，`REQUIRE_INTEGRATION=1`；数据库 `server_version_num=180000`。测试夹具逐次执行迁移、再次迁移和 schema 检查。没有连接业务数据库。

| 检查 | 结果 |
| --- | --- |
| `scripts/go.sh test ./internal/httpapi -run '^TestPaymentAnomaly' -count=1` | PASS，25.595 秒，含当时 8 项专项测试 |
| `scripts/go.sh test -race ./internal/httpapi -run 'TestPaymentAnomaly\|TestPaymentClose\|TestExternalRefund\|TestOnlineTopup\|TestAdminRBAC' -count=1` | PASS，147.794 秒 |
| `scripts/go.sh test -race ./internal/httpapi -run '^TestPaymentAnomalySameKeyConcurrencyAndRevokedReplay$' -count=1` | PASS，3.009 秒 |
| `scripts/go.sh test ./internal/store -run 'PaymentClose\|ExternalRefund\|OnlineTopup\|AdminRBAC' -count=1` | PASS，44.968 秒 |
| `scripts/go.sh build ./...` | PASS |
| `scripts/go.sh vet ./...` | PASS |

专项覆盖真实 API 的创建、重放、独立复核、403/404/409、客户范围、API Key 拒绝、组版本更新、同交易重复事件去重、新交易继续冻结、8 路异键与同键并发、撤权/会话过期后的重放拒绝、原账单改价后的原金额退回、普通退款跨表出款竞争与驳回释放、审计故障回滚，以及原商户绑定/凭据摘要/出款认领的直接 SQL 篡改拒绝。

反向竞争专项覆盖 pending、confirmed 与并发三种顺序；同一原交易在异常退回与另一支付成功中至多一方生效，SQL 直接改成功同样拒绝。成功回调被拒绝时，不留部分结算或回调已处理记录。

追加范围撤权竞态复核：销售人员设置会更新员工客户范围而保留会话。在数据库事务中先将员工范围从全部收窄为本人客户，保持员工行锁，再发起 HTTP 请求；通过 `pg_blocking_pids` 确认请求已经通过初始范围检查并等待员工授权行锁，随后提交范围变更。修复前创建和确认错误返回 201，重放和读取错误返回 200；确认可越权解除责任冻结。修复在锁定员工并核验权限后再次检查客户范围，四条路径统一返回 404，不新增登记或复核记录。修复后全部 `TestPaymentAnomaly*` 专项 race 通过（79.666 秒）。独立日志为 `/tmp/anomaly-scope-wait-red-20261017.log` 与 `/tmp/anomaly-scope-wait-fixed-race-20261017.log`。

机器日志位于 `/tmp/anomaly-refund-reverse-20261017.log`、`/tmp/anomaly-refund-race-regression-20261017.log`、`/tmp/anomaly-refund-same-key-race-20261017.log`、`/tmp/anomaly-refund-store-regression-20261017.log`；日志及隔离连接配置不纳入 Git。

## 验收边界

登记的是人工核实的实际外退证据，系统不会据此声称已调用商户退款。真实资金到账、商户退款协议与公网异步通知仍需独立验收。此分支没有新增前端，也未单独宣称完成四尺寸浏览器验收或全项目全量测试。
