# 负载与 worker 验证

保留一个可执行的只读负载入口，复用已有开环调度、超时、并发上限、丢弃计数和直方图实现：

```bash
# 显式指定隔离实例；不会启动项目服务
node scripts/load-test.mjs --origin http://127.0.0.1:18088 --seconds 30 --rps 10 --concurrency 20 > /tmp/goidc-load.json
```

入口仅 GET `/api/v1/catalog`，只接受显式 IPv4 回环 origin，禁止跟随重定向，读取完整响应并限制为 1 MiB。
默认每请求超时 5 秒；整轮最多 100000 次请求。报告区分计划、开始、完成、失败、超时及丢弃，并输出 p95/p99 直方图上界估计。有失败、丢弃、取消或零成功时退出非零。
它仅验证目录 HTTP 读取，报告固定 `capacityVerified: false`，不代表财务正确性、队列排空、真实渠道或正式容量通过。

已可执行的经营场景和 worker 验证继续保留：

| 用途 | 入口 |
| --- | --- |
| HTTP 场景执行与调度回归 | `node --test scripts/g09-load-lib.test.mjs scripts/g09-scenario-lib.test.mjs scripts/g09-epay-scenario.test.mjs scripts/g09-workload-report.test.mjs scripts/load-test.test.mjs` |
| 600 任务、两个真实 worker 与独立对账 | `ZJMFGO_G09_COHORT_SMOKE=1 REQUIRE_INTEGRATION=1 ./scripts/go.sh test ./internal/httpapi -run '^TestG09CohortSourceWorkerSmoke$' -count=1 -timeout=15m -v` |
| 供应商与通知 SIGKILL 恢复 | `./scripts/test-worker-recovery.sh` |
| 已封存候选的隔离进程验收 | `node scripts/accept-g09-runtime.mjs`，按其参数要求准备候选、预算及私有夹具 |

涉及数据库的入口都要求独立 PG18 `_test` 库；真实 worker、600 任务和来源场景仍需各自显式 opt-in。测试会创建随机 schema、loopback 供应商及通知端点，不代表外部交付。

未接通的容量 CLI、固定 profile/controller/allocator、投影桥、Go projection loader、离线 capacity fixture 协议及独立 sampler-window 已从活动树移除，原始代码与回归保存在 [封存包](archive/README.md)。它们没有形成可执行的完整容量链路；不要把封存前的单元测试当作目标容量证据。

后续容量工作从实际目标和可运行负载开始：先约定吞吐、延迟、错误率与资源预算，再补经营写入场景、进程/PG 观测及队列排空对账。需要扩展时复用保留的场景、协议 sink、PG observer 和 worker 恢复用例，不重新堆叠无执行路径的预检层。
