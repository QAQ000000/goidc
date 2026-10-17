# 常驻供应商自动续费进程验收

执行（仅显式启用）：

```sh
ZJMFGO_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55439/zjmfgo_refund_local_test?sslmode=disable' \
REQUIRE_INTEGRATION=1 ZJMFGO_WORKER_ACCEPTANCE=1 \
go test ./internal/httpapi -run '^TestSupplierResidentWorkerAutoRenewal$' -count=1 -v -timeout 4m
```

要求 loopback PostgreSQL 18、数据库名以 `_test` 结尾。复用现有 fixture 为每个场景创建独立随机 schema，迁移并预置测试用户、余额、供应商及服务；worker DSN 显式携带该 schema 的 search_path。只构建当前源码 `cmd/worker`，不修改生产 worker，不调用自动续费处理函数代替 scheduler。

每个场景同时启动两个真实 worker：启动扫描必须在25秒内产生续费结果（早于一分钟 timer），核对一张续费账单、一个 renew 任务、唯一一次余额扣款以及唯一上游建单/付款。随后 TERM、重启两个 worker，等65秒跨过真实分钟 tick，再核对所有唯一性；完成后再次重启核对。成功场景由 worker 自行确认期限并释放占用；未知期限场景保持 waiting_verification、本地期限不变及占用冻结，跨分钟和重启均不能重复远端操作，再经现有管理员核验 API 确认远端已延长才更新期限/释放占用。

子进程只传白名单环境：测试 DSN、loopback PUBLIC_ORIGIN、测试加密密钥、禁用 mock 支付及 loopback SMTP。工作目录为临时目录，不读取生产 `.env`；fixture 内唯一供应商为 httptest loopback 魔方协议，凭据均为测试值。无生产外发、真实资金或资源操作。子进程受测试 context 控制、TERM 收尾并 Wait 回收，测试临时目录/二进制和 schema 随 cleanup 删除；不停止共享 PostgreSQL cluster。

边界：最初服务购买/开通使用现有 fixture 直接处理 create，不计作常驻采购验收。未知结果只覆盖“远端账单已付款、期限尚未确认”的现协议冻结路径；恢复刻意走既有人工核验入口，不声称后台会自动恢复未知结果。成功链路的续费建单/本地支付/排队/远端续费/远端付款/期限确认均由实际 worker 完成。现有 RunDailyAutoRenewals 持事务 advisory lock 跨越整次续费扫描，这是业务现状；验收自身不额外持事务/锁调用远端。

2026-10-16 实际执行结果：定向测试 PASS 146.694s，正常确认场景72.85s、未确认冻结场景73.83s；两个场景共12次真实worker启动，均正常TERM退出。运行中检查/proc，子进程环境只有上述7个白名单键。完成后检查无任何本验收活进程，采样PID/临时二进制均不存在，采样独立schema查询计数为0。没有运行全量测试，也未停止共享PG实例。
