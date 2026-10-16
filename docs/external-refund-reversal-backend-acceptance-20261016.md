# 充值外部退款冲正后端验收（2026-10-16）

独立分支 `agent/external-refund-reversal-20261016`，基线 `6803e5b`；设计 `3e2df82`、实现 `1ebf2e4`、专项补强 `0a3527f`。本记录只证明隔离后端和 fixture 生命周期，完整前后端、四尺寸和主线全量由集成验收另列，不据此关闭全部 G03。

## 已实现

迁移149新增 `external_refund_reversal_reservations`，绑定原退款、充值账单、客户、金额。活动账单唯一，原关联不可改，`released/applied` 终态不可改；数据库检查原充值/结算/成功支付/正向充值流水的全链关联，确认时检查负向冲正流水的归属、金额和独立键。原始失败结算后由迁移148补账的充值明确不在此次支持范围。

沿用已有三权限和浏览器会话、独立员工复核、原商户身份、凭证摘要、全额与幂等约束。申请在客户资金锁内冻结可用余额；`payInvoice` 以余额减待审预约判断可用金额。驳回释放预约；确认同事务扣余额、追加一次负向余额流水、应用预约、把账单改 `refunded`、写员工审计。原支付 `succeeded`、原收款/`paid_at`、原充值结算和入账事实保留。

审计中的 `external_refund.balance_reversed` 归属复核员工；不伪装成客户本人扣款。旧本地余额退款只重放正向退款流水，猜中外部冲正负向流水的键也不能误报本地余额退款成功。

后台响应可选 `refund.reversal`：`status`（pending/released/applied）、`amount_cents`、`ledger_id`、`applied_at`、`released_at`。原普通人工账单不增加余额流水，仍沿用首增量行为。

## 实际验证

| 检查 | 结果及范围 |
| --- | --- |
| PG18迁移与store | 测试日志实际读取 `server_version_num=180000`；每例随机schema、实际迁移和 `CheckSchema`；清理由测试退出完成 |
| `go test -race -p 1 ./internal/store ./internal/httpapi -run '^TestExternalRefund' -count=1 -v` | PASS，store 36.544s、HTTP 13.388s；浏览器 opt-in 用例未开启，明确 SKIP，不当浏览器通过 |
| 最终store追加race | PASS，`^TestExternalRefundTopup`，4.618s；包含最终负向键重放保护和审计归属断言 |
| 最终HTTP追加race | PASS，`^TestExternalRefundTopupHTTP`，6.261s；含待补账/补账后明确拒绝，无退款/预约残留 |
| Go build / vet | PASS，`scripts/go.sh build ./...`、`scripts/go.sh vet ./...` |
| 浏览器fixture启动/收尾 | PASS，`TestExternalRefundReversalBrowserFixture`，70.822s；READY及fixture.json为0600，真实测试API启动，finish后文件与schema清理；不等于页面浏览器验收 |

store新增真实资金断言：充值400分→登记冻结400→余额消费100被拒绝→驳回释放→真实消费100成功→后台加款100→同真实退款流水补正→审计故障确认整体回滚→同原键恢复确认→余额归零。最终保留原充值流水1条、负向退款流水1条、两条预约历史（released与applied）、一条confirmed退款，确认重放不再扣款。终态改金额/删除被数据库拒绝。

并发断言：真实充值400后，退款登记、余额消费100和管理员加款100同时竞争同客户锁；只允许可用余额付款，冻结额不大于实际余额。随后独立确认，`users.balance_cents=sum(balance_ledger.delta_cents)`，负向冲正仅一条；未用直接改余额伪造资金闭环。

HTTP新增：无登记权限/客户拒绝、真实申请201、同键200、异参409、复核版本409、独立确认201/重放200；退款后原签名成功通知重放不恢复paid，不重复入账。网关收入1234、外部退款1234、现金净额0，负向余额流水不重复计算外部出款。既有HTTP专项覆盖撤权/过期会话/销售范围/自审/不同键并发和审计故障。

fixture提供7张直接入账充值（4空、1pending、1confirmed、1rejected）及1张pending liability；独立 requester/reviewer/reader/limited/denied 和客户，受限员工无客户范围。实际数据均由真实Go API/签名本地协议通知生成，无真实商户出款。

## 已知边界

原结算为pending_liability的充值即使后来补账，仍明确返回 `external_refund_topup_not_credited`。信用、订单/服务、供应商续费、升级、流量、佣金和有效发票申请继续由现有保护拒绝；未把修改paid状态当作复杂业务冲正完成。部分退款、自动原路退款、真实商户到账、公网回调、真实资源销毁均未验收。

此次不修改前端/公共台账，不写业务库、不部署或推送。初次新store测试曾因fixture到账时间早于刚生成收款而失败，修正时间后通过；第二次曾错误读取released历史预约并断言总数1，修正为读取applied及历史数2后通过，不把失败试运行算作验收通过。
