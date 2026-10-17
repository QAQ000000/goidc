# 供应商真实 worker SIGKILL 恢复验收（2026-10-17）

本专项使用 `TestSupplierWorkerSIGKILLRecovery`，仅在 `ZJMFGO_WORKER_CRASH_ACCEPTANCE=1` 时运行；开启后缺 DSN 直接失败。DSN 要求显式端口、IPv4 loopback、`_test` 数据库，禁止除 sslmode 外的连接参数覆盖，创建 schema 前检查 PG18。各子例由现有 fixture 创建随机 `zjmfgo_test_*` schema，执行真实迁移与 CheckSchema，退出删除自己的 schema。未修改生产路径。

使用专用库 `zjmfgo_release_runtime_20261017_test`，隔离 PG18 `127.0.0.1:55439`。供应商全部为 loopback HTTP fixture，固定伪密钥不代表实际账户。测试通过 scripts/go.sh 构建真实 cmd/worker 临时二进制，每例启动五个实际 worker 进程；临时二进制由 Go 测试清理，不生成候选发布包。

验收链：

1. worker 自己 claim、提交 external_started_at 和账户租约；loopback 接口执行 fixture 操作并扣留响应。测试从独立 DB 连接证明 running / attempts=1 / external marker / running_job_id 后调用 Process.Kill，WaitStatus 必须为 SIGKILL，死后 DB 仍 running。
2. 仅将这个已真实死亡任务的 locked_at 老化六分钟，跳过五分钟等待；不伪造 claim、dispatch 或死亡状态。启动两个 worker，让生产 RecoverStaleProvisionJobs 自行恢复。
3. 只读 sync 成功，attempts=2，账户租约释放，目标详情请求共两次（死进程一次、恢复一次），已有购买/付款副作用各一次。
4. create 已在 fake settle 产生一次副作用，但响应未交付：冻结 waiting_verification，attempts=1、external marker 与租约保留，settle 总一次、付款零次。同账户另一个真实购买任务保持 pending / attempts=0。
5. 两个恢复 worker TERM 退出，再启动两个 worker，跨三个两秒轮询后断言不变。清理先退出 worker，再释放挂起响应、关闭供应商，最后删除 schema。

运行命令（DSN 由受控环境提供，不在日志或本文写入凭据）：

```sh
ZJMFGO_WORKER_CRASH_ACCEPTANCE=1 REQUIRE_INTEGRATION=1 scripts/go.sh test ./internal/httpapi -run '^TestSupplierWorkerSIGKILLRecovery$' -count=1 -v
ZJMFGO_WORKER_CRASH_ACCEPTANCE=1 REQUIRE_INTEGRATION=1 scripts/go.sh test -race ./internal/httpapi -run '^TestSupplierWorkerSIGKILLRecovery$' -count=1 -v
```

证据边界：实际 SIGKILL 发生在网络请求已送达、DB claim/dispatch 已提交之后；没有覆盖 claim 提交前、dispatch 提交前、外部请求送达前、响应收到后本地提交前、outbox、DB 重启、宿主机断电。lease 时间由测试加速，未证明真实五分钟时间流逝。仅覆盖 sync 与 create 的代表路径，不替代其他写动作独立验收、人工核验、真实供应商兼容性、生产容量或跨主机部署证明。race 检查覆盖 Go 测试与 loopback fixture；子进程为普通生产 worker 构建，不能声称子进程启用 race。

实际执行记录：普通集成验收退出 0，日志 `/tmp/zjmfgo-supplier-sigkill-20261017.log`。`-race` 实际验收退出 0 的首轮日志 `/tmp/zjmfgo-supplier-sigkill-race-20261017.log`；补充有效连接端点预检后的最终 race 同样退出 0（26.194 秒），记录为 `/tmp/zjmfgo-supplier-sigkill-final-race-20261017.log`。日志保留随机 schema、PG版本、进程PID、SIGKILL确认、状态/attempt/request计数，不含 DSN 或凭据。
