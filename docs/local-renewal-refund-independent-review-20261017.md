# 本地续费退款最终增量独立审查

审查对象：固定提交 `5179259`；增量范围 `a1f5bc0..5179259`。审查日期：2026-10-17。结论：本次增量未发现新的阻断问题。这个结论限于下述源码检查与两项真实隔离 PostgreSQL 测试，不等同于整个 G03 完成或浏览器最终验收。

## 增量边界

增量包含四个提交：`f8f2f6b`、`f688184`、`9869b6e`、`5179259`。实际产品代码只修改 `internal/httpapi/server.go` 的错误映射；其余为回归测试和测试夹具修正。迁移155、退款 store、续费逆操作与 Web 组件在该增量中均无修改。本次也对固定快照相关接口与恢复代码作了交叉核查。

| 检查 | 结果 | 源码与结论 |
| --- | --- | --- |
| 409 对应稳定业务码 | PASS | `internal/httpapi/server.go:865` 将 SQLSTATE 23514 且精确消息 `renewal_refund_hold` 映射为 HTTP 409 和固定中文说明；不向客户端返回 SQL、表名或原始错误详情。其他约束仍走通用冲突。 |
| UI 保留原请求 | PASS（静态） | `web/src/components/external-refunds.tsx:261` 恢复时复用原 path/body/key/kind/recordId；`:268` 发送前先持久保存；`:271` 直接发送原 body 和 Idempotency-Key。没有按新 context 重写原版本的恢复路径。 |
| 409 与未知结果分支 | PASS（静态） | 同文件 `:294` 对明确的 400/404/409/422 保存 rejected 标记并保留原请求，要求显式读取最新事实；`:300` 对传输失败、无法验证的成功回执等保留待核实状态。`:303` 只有已拒绝且已读最新事实才可显式结束原请求。刷新、关闭弹窗不会自动发送。 |
| 新增恢复字段与接口相符 | PASS（静态） | 同文件 `:181`、`:185` 接纳并严格校验正安全整数 `expected_renewal_effect_id` / `expected_renewal_service_version`，非续费不得混入；store DTO 在 `internal/store/external_refunds.go:75` 声明相同 JSON 名称。回执在 Web `:277` 校验 `renewal_reversal`，关联原 effect/service/invoice 版本；真实 context 路由见 `internal/httpapi/server.go:546`。 |
| 身份及权限变化 | PASS（静态） | Web 外层以身份及专用权限构造组件 key；请求前及可确认回执后均重读 `/me`。context handler `internal/httpapi/external_refunds.go:104` 使用会话交给 store 核验，未新增跳过专用权限的入口。 |
| 父发票冻结保护仍存在 | PASS（静态；此前独立实测） | `db/migrations/155_local_renewal_refunds.sql:154` 经 application items 找原账单并 NOWAIT 锁定，`:159` 再找服务并 NOWAIT；`:188` 对 invoice_applications UPDATE/DELETE 安装触发器。旧 rejected 申请不能仅改父状态重新生效来绕过 hold。之前独立测试提交 `9c99b59` 已对父 UPDATE/DELETE、驳回释放及反向锁不等待进行真实 PG 验证，本次未重复运行这三项。 |
| 半完成 applying 仍不能提交 | PASS（静态；此前独立实测） | 迁移155 `:199` 限制状态转换，`:240` 校验精确逆操作，`:263` 起安装 deferred 完整性检查；`9c99b59` 的 half applying 提交与无 reversal 手工改服务均拒绝。本次增量未改这些保护。 |
| 冻结例外没有新增权益入口 | PASS（静态） | 迁移155 `:140` 的佣金 bucket/status 例外不允许改身份，且既有佣金本就阻断资格；`:167` 只接纳串行锁保护下的晚到异常证据 INSERT，确认仍复核未解异常。这些例外本增量无变化，不会自行创建续期 effect 或逆操作。 |
| 首次结算与未提交依赖竞争 | PASS（本次真实 PG） | `internal/store/local_renewal_refunds_test.go:529` 在尚无 effect 的服务上先创建未提交 provision job，再并发真实付款回调；结算立即冲突、期限与 effect 数量不变，依赖提交后原回调可重试；随后退款 context 稳定拒绝 `renewal_operation_pending`。 |
| HTTP 冻结与撤权边界 | PASS（本次真实 PG） | `internal/httpapi/local_renewal_external_refund_test.go:235` 验证 hold 下续费请求冲突、auto-renew 返回409及稳定 `renewal_refund_hold`，并验证专用权限撤销后不能继续原退款操作。 |

## 本次执行证据

在独立 worktree `/www/wwwroot/zjmfgo-local-renewal-final-audit`、固定 `5179259` 上执行：

```sh
ZJMFGO_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55439/zjmfgo_release_runtime_20261017_test?sslmode=disable' \
REQUIRE_INTEGRATION=1 go test ./internal/store ./internal/httpapi \
  -run '^(TestLocalRenewalFirstEffectCannotOvertakeUncommittedDependency|TestLocalRenewalExternalRefundHTTPRevocationAndHold)$' \
  -count=1 -timeout=3m
```

结果：store `ok 5.258s`；httpapi `ok 6.558s`。测试使用现有隔离 helper：要求 `_test` 数据库，逐测试创建随机 schema，结束后清理。本次没有改实现、启动或重启常驻服务，也没有运行 Web build 或全量测试。

## 验证边界

- UI 恢复、取消、错误与身份切换的结论是固定源码静态检查；本次没有操作浏览器，四宽真实 API 浏览器证据应由最终浏览器验收文档单列。
- 本次只执行上述两个增量相关测试；未重跑此前已完成的完整迁移、全量、race、dump/restore 和财务矩阵，不把旧结果冒充本次新执行结果。
- 未验证真实商户出款到账、公网回调、生产迁移或部署；协议夹具与本地账本结果不证明真实资金到账。
