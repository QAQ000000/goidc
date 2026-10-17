# 补账后充值整单外部退款专项验收（2026-10-17）

分支 `goal/g03-complex-refund-20261017`，基线 `e49d5d7`；[契约](resolved-topup-external-refund-contract-20261017.md)。实现153校验函数与149触发器共用实际入账证明，支持148已补账充值全额退款，保持原 settlement=pending_liability、resolution 和正向余额流水。前端仅资格和说明变化，接口字段不变。

## 实际验证

使用专用 `zjmfgo_resolved_refund_20261017_test` 库，连接隔离 loopback PG18 55439；每例随机 schema、真实迁移和 CheckSchema，退出仅清理自己的 schema。测试输出 `server_version_num=180000`，未连接业务库、未调用真实商户、无部署。

| 检查 | 结果 |
| --- | --- |
| `REQUIRE_INTEGRATION=1 scripts/go.sh test -race -p 1 ./internal/store ./internal/httpapi -run '^TestExternalRefund' -count=1 -timeout=10m -v` | PASS，store 36.765s，HTTP 30.195s；浏览器 opt-in 明确 SKIP，不作为浏览器通过 |
| 最终追加 `-race -p 1 ./internal/httpapi -run '^TestExternalRefundResolvedTopupHTTPRevocationWhileLocked' -count=1 -timeout=3m -v` | PASS 20.497s，含最终 List 锁序变更，read/create/confirm/replay × scope/permission 8例 |
| `scripts/go.sh build ./...` / `scripts/go.sh vet ./...` | PASS，最终代码 |
| `npm run lint` / `npm run typecheck` | PASS，lint 0错误、7个既有 warning |
| `npm run build` | PASS，Next16.3.8 Turbopack；初次外部 node_modules symlink被Turbopack拒绝，使用本worktree临时依赖副本后通过；副本在收尾删除恢复symlink |
| `git diff --check` | PASS |

生命周期复用直接入账测试并增加已补账分支，真实网关fixture收款→强制原入账失败→ResolveTopupLiability补账→申请冻结→消费拒绝→驳回释放→真实消费成功→余额不足拒绝重新申请→真实管理员补足→重新申请→确认前非法余额消耗故障明确拒绝→审计失败整体回滚→恢复同键确认→确认重放→原回调重放。断言账单 refunded、余额归零、原正向流水1条、负向流水1条、released/applied两条历史，原settlement/resolution JSON前后完全相同。原始和后补账都拒绝部分金额。

并发复用资金竞争用例，后补账完成后同时登记冻结、客户余额消费、管理员加款，限时无死锁；冻结不超过余额，确认后余额与流水净额相等，负向流水仅一次。共享原客户锁，不引入新资金锁。

DB专项在独立事务故障注入正向ledger的客户/金额/账单/引用类型/entry_type错关联，资格函数均false，直接写149预约被23514拒绝；错误独立负向键也不能应用预约。此为测试故障注入，不是合法业务允许改历史。

HTTP覆盖真实权限/独立复核/版本/幂等、未补账明确409无残留、后补账201/确认/重放、原签名回调重复后账单仍refunded、原支付succeeded、外部支出仅一次，收入1234/退款1234/净额0。原异常原settlement保留且补账事实不重写。

员工与范围顺序统一 scope advisory→actor/role→新语句重读scope，避免销售分配反向锁序及员工锁等待后旧snapshot范围放行。8例观测实际 `pg_blocking_pids` 再提交撤销，预期范围404/权限403，完整退款/预约/操作/余额/审计快照不变。

首轮追加并发helper因重复声明ctx编译失败，修正后完整race通过；该失败不计通过结果。无生产迁移。浏览器专项和主线全量由集成任务另列。

## 主线集成验收

产品已以 `d462e49` 集成，浏览器夹具为 `6baf13d`，最终浏览器证据以 `30cd2ac` 集成。主代理对30张截图SHA-256逐一重算，30/30一致；15项检查、四尺寸及320×500、登记/确认两类丢回执恢复、权限/版本拒绝、未补账/余额不足保护与原事实不变见[浏览器专项](resolved-topup-external-refund-browser-acceptance-20261017.md)。同批过期查单恢复的8张截图独立核验，不能混入本批30张。

最终 `ZJMFGO_TEST_DATABASE_URL=<专用PG18测试库> scripts/test-all.sh` 退出0，强制 `REQUIRE_INTEGRATION=1`、`-count=1`：HTTP 1533.853秒、store 159.727秒、legacy-import 173.414秒，其余包通过或无测试。专用 `zjmfgo_payment_final_20261017_test` 位于loopback隔离PG18；结束检查无遗留随机schema、无其他连接后，仅删除该库。日志本地路径 `/tmp/zjmfgo-resolved-refund-full-20261017.log`，SHA-256 `2cbf78df98b18763a90abee0162a5dc334c2252dc7199b6b0ffa817bd76401d3`。浏览器等opt-in测试的跳过不计通过，使用各自专项证据。

全量之后仅追加构建前保护与文档；Node实际进程/恢复测试15/15通过，未因纯构建脚本重复运行Go全量。前端最终独立webpack生产构建通过，主目录不再构建。此前主代理误覆盖常驻构建及旧Web恢复已单独记录在[事故与恢复](live-web-build-recovery-20261017.md)，不能将恢复旧Web说成发布本批功能。

追加由另一名 `gpt-6.1-sol` 子代理只读复核 `f7c4f38` / `d462e49`：核对精确原键/身份及金额版本guard、实际入账链、客户资金锁、scope→actor→scope、授权后幂等重放与独立冲正同事务，未发现有证据支持的新增可行动漏洞。该复核是静态审查，不替代上述运行时竞争/浏览器证据，也不构成真实商户验收。

## 保留边界

未补账、仅状态credited但无完整事实链、已消费导致余额不足均拒绝。订单、服务、续费、升级、流量、信用和佣金关联仍保守拒绝；缺少供应商取消/同步、期限/库存/已交付回收或佣金追偿的原子事实，不能安全沿用此充值路径。部分退款仍拒绝，未设计分摊与多笔退款。真实商户出款、付款人到账、生产切换均未验证，此增量不关闭全部G03。
