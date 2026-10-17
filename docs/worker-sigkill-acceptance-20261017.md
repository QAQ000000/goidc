# G09 worker 执行中强杀恢复专项（2026-10-17）

**专项 PASS，G09 整体保持开放。** 代码截点 `d72156e030301b834ff80691ad8f0c058b7bff17`，与 `7f7e671` 相比只增加测试、验收入口和说明，没有修改生产 worker、业务代码、迁移或前端。两个 `gpt-6.1-sol` 子代理在独立分支实现、提交；第三个同模型代理只读审查，主代理集成后通过统一入口实际重跑。

## 实测范围

| 场景 | 已观察结果 |
| --- | --- |
| 供应商只读 sync 已发出、回执未收到时 SIGKILL | DB 保留真实 running/dispatch/账户租约；新 worker 自行恢复后 succeeded、attempts=2，详情请求两次，原购买/付款仍各一次，账户租约释放。 |
| 供应商 create 的远端 settle 已执行、回执未收到时 SIGKILL | waiting_verification、attempts=1，派发标记和账户租约保留；settle 一次、付款零次，不自动重发。同账户第二任务实际进入账户忙分支，pending/attempts=0、无 lease/dispatch，带未来退避时间。 |
| SMS 网关已收到请求、回执未收到时 SIGKILL | sending/attempts=1 保留且无旧 attempt 响应证据；重启恢复 pending，并先断言真实的一分钟退避，再加速到期，完成 sent/attempts=2；网关共收到两次，attempt 2 accepted 证据及密文清理通过。 |
| SMS max_attempts=1 同窗口 SIGKILL | 新 worker 自动恢复 dead_letter/attempts=1、死信时间存在，网关仍只收到一次。 |
| 终态与冻结状态再次重启 | 所有参与观察的 worker 先等待 ready 日志；终态/冻结状态及副作用计数保持不变。供应商第二任务仍在三十秒退避内，不宣称再次触发了账户忙分支。 |

共启动 **18 个真实 cmd/worker 子进程、4 次 SIGKILL、4 个随机 schema**；恢复由新 worker 的生产循环执行，测试没有直接调用 Recover 替代进程恢复。供应商两轮各有两个 ready worker，短信恢复有两个 ready worker；不据此声称每个进程都成功领取过同一任务。

隔离范围为回环 PG18 专用 `_test` 库、每例随机 schema、本地 HTTP 协议夹具。子进程只接收白名单 fixture 配置，工作目录为临时目录，不读项目 `.env`。没有生产渠道请求、业务库写入、部署、推送或常驻服务重启。

## 运行与证据

```sh
# 先设置 ZJMFGO_TEST_DATABASE_URL：显式 IPv4 loopback、端口、PG18、_test 库
scripts/test-worker-recovery.sh
```

入口强制开启 opt-in、REQUIRE_INTEGRATION 和 harness race，禁止缓存，并核对两套顶层测试实际 PASS。缺 DSN 会退出 1；在测试尚未集成时，Go 返回“no tests to run”及退出 0，入口仍正确拒绝。门禁反例保留，不改写成通过。

| 检查 | 结果 |
| --- | --- |
| 主线统一真实强杀验收 | 两套顶层、四个子例 PASS。store 23.894s / httpapi 27.344s（包时长，不能相加当并行执行的墙钟耗时）。 |
| 既有租约、退避、死信、旧 attempt fencing、证据与只读/写恢复 race 回归 | 9 个顶层测试 PASS，httpapi 33.979s；这是持久状态夹具回归，不另计真实强杀场景。 |
| 最终主线 `scripts/go.sh vet ./...` | 退出 0，无输出。 |
| PID/schema/临时目录收尾 | 日志中的 18 个 PID 均已消失，4 个 schema 查询均不存在，两套测试临时目录均不存在。 |
| 常驻运行状态 | API、worker、Web 的 PID、exe、cwd、启动 tick 与清理前一致；healthz 与 admin-login 均 200。 |

实际日志与收尾记录已保存到 [证据目录](evidence/worker-sigkill-20261017/index.json)，包含源文件及日志 SHA256。统一 race 日志 SHA256 为 `4fcbf19faeede9aa32c2459570fad147f2c9a528ad287c5f8668d5d365ad82d9`。专项实现及先前逐次运行记录见[供应商](supplier-worker-sigkill-acceptance-20261017.md)、[通知](notification-worker-sigkill-acceptance-20261017.md)。独立审查没有重复运行测试，最终执行与收尾由主代理完成。

## 仍未关闭

- 五分钟租约及短信退避均由 SQL 加速：先证明真实进程死亡，再改变时间字段；不证明实际恢复耗时、RTO/RPO 或等待时长。
- 仅覆盖网络请求已送达而响应未持久化的窗口。claim/dispatch 提交前后、收到响应后本地提交前、业务 outbox 入队前后、DB/宿主机故障仍需独立验收。
- 通知属于至少一次尝试。首次发送已到网关，重试可能产生重复短信；本次明确观察两次请求，不能称 exactly-once。
- 没有测试 SMTP、真实手机收件、真实供应商、其他供应商动作或人工核验流程。没有验证生产容量、稳态/峰值、积压排空或公网部署。
- race 覆盖测试进程与 fixture；子 worker 是普通构建。本批没有重跑全量 Go 集成、前端检查/构建或四尺寸浏览器；没有 UI 或生产路径变更，不借用旧全量报告冒充本次执行。
