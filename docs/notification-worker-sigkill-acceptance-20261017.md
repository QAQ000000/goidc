# G09 通知真实 worker SIGKILL 恢复验收（2026-10-17）

基线 `7f7e671`。新增 opt-in 测试 `internal/store/notification_worker_sigkill_test.go`，不修改生产路径。测试真实编译并运行 `cmd/worker`，不是调用 Claim/Recover 方法模拟进程崩溃。

## 可复用运行入口

```sh
ZJMFGO_WORKER_CRASH_ACCEPTANCE=1 \
ZJMFGO_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55439/zjmfgo_release_runtime_20261017_test?sslmode=disable' \
./scripts/go.sh test -race ./internal/store \
  -run '^TestNotificationWorkerSIGKILLRecovery$' -count=1 -v
```

不开 opt-in 则 skip；开启但没有合法 DSN 则 fatal。复用已有 runtime fixture 的 URL 与驱动字段校验：显式 IPv4 loopback、带端口、`_test` 数据库、禁止查询参数覆写主机或 search_path；实际连接 PG18 和 loopback 地址验证后才建随机 schema。每例独立 `release_runtime_<24hex>` schema，迁移并检查 schema；结束清理 schema。worker 使用该 schema 的 DSN 和仅含 fixture 配置的白名单环境，不继承业务凭据。只在临时目录编译 worker、保存子进程日志并在结束删除；不启动或停止常驻 API、worker、Web，不运行 Next build。

## 已覆盖断言

1. loopback `generic_http_json` HTTP fixture 校验 POST、假令牌和测试消息，收到完整请求后挂起响应。查询持久数据库证明 `sending / attempts=1 / locked_at非空` 后，对真实子进程 SIGKILL，检查操作系统退出状态确为 SIGKILL。
2. 强杀后持久 claim 原样保留，旧 attempt 尚未存储响应证据。这是发送已被接收但本地未收到回执的真实窗口。
3. 仅在确认子进程已死后将 `locked_at` 老化六分钟。启动两个真实 worker 争抢恢复，验证 `pending / attempts=1 / locked_at清除 / last_error=worker lease expired` 和约一分钟的 `available_at` 退避，外部请求仍为一次。
4. 断言实际退避后，仅将 `available_at` 加速为当前时刻；竞争 worker 完成重试，验证 `sent / attempts=2`、外部收到两次请求、body密文清空、存在 attempt 2 的 accepted delivery evidence。
5. 另一随机 schema 的 `max_attempts=1`：同样真实发送挂起和 SIGKILL，双 worker 恢复直接产生 `dead_letter / attempts=1 / dead_letter_at非空`，不再发第二次请求。
6. 停止竞争 worker 后再次启动真实 worker，观察超过两个两秒 poll 周期，成功行和死信行均不再触发请求。所有子进程均 Wait 回收；日志用文件避免并发 bytes.Buffer 读写竞态。

## 证据与边界

初次正常进程验收 PASS：两例共 27.99 秒；limit 3 的恢复退避剩 59.97 秒，终态 sent、attempts 2、fixture requests 2；limit 1 终态 dead_letter、attempts 1、fixture requests 1。最终增强版本的 race 结果见下方。

这里使用 SQL 加速五分钟租约和一分钟退避，没有真实等待全部时长；原始 claim 和 SIGKILL、恢复与重试本身均为真实 worker 子进程执行。`-race` 检测测试驱动；临时 worker 按正常 `go build` 编译。本次没有验收 SMTP、真实短信服务、真实送达手机、网络分区或网关侧去重。

通知属于至少一次尝试语义。第一次请求已经到达网关，再次发送可能产生外部重复短信；这次 fixture 明确观察到两次接收。本地成功后重启不重复领取不能证明外部 exactly-once。

最终增强版本 `-race` PASS：总测试 24.84 秒（进程退出报告 25.878 秒），limit 3 用时 13.29 秒、limit 1 用时 10.60 秒；恢复退避剩 59.99 秒。每例均启动两名竞争恢复 worker。无 race 报告。日志 `/tmp/notification-sigkill-race.log`（仅当前验收主机）。缺失 DSN 的开启场景预期 FAIL，错误 `fixture requires explicit IPv4 loopback PostgreSQL URL`，在编译 worker、连接或创建 schema 前拒绝。
