# G09 源码 API 与真实 worker 小闭环

状态：独立 opt-in 测试已实现，首次真实 PG 闭环失败，尚未验收通过。本增量不修改生产行为或原 `TestG09SourceAPIScenarioSmoke`，不关闭 G09 容量门槛。

入口为 `internal/httpapi/g09_source_worker_smoke_test.go:TestG09SourceWorkerLoopbackSmoke`。测试以真实 loopback HTTP 调用源码 Handler；现场构建一次 `cmd/worker` 并启动两个真实子进程。测试代码不调用 Claim、Process、Complete 或 Recover 来替代 worker。真实循环负责 claim、租约复查、实例配置解析、生产 SMTP/SMS provider、SMS 响应证据和终态提交。

## 有限来源与证据

1. fixture-only SQL 预建两个身份、一个 simulation 商品及一个工单部门；只开 `ticket_customer_created` 两渠道模板。员工邮箱为 `.test`、手机号为 `+999` 合成身份并已验证；客户保持未验证。账号使用既有 helper 真实登录。渠道与加密密钥由真实渠道 API 配置，偏好通过员工自己的 API 修改。
2. 客户真实 HTTP cart→order→GET invoice 核对归属/1000分/未支付/版本→余额付款，生成一个 pending simulation create job；不直插订单、账单、账本或任务。
3. 客户真实 HTTP 创建工单；从 event/delivery 读取并验证恰好一封邮件、一条短信及对应 outbox，正文包含本次 runId、ticket_no、公开 body、工单 URL。三个具体任务的来源 ID 在启动 worker 前写入私有冻结记录。
4. 两个 worker 使用同一个源码二进制、各自明确携带随机 schema 的 DSN；环境白名单不继承 `.env`、代理、支付/供应商/渠道凭据。核对 PID 的 exe/cwd；等待生产启动日志和这三个 ID 的成功终态，最长45秒。正常生产 scheduler 没有被关闭、删除或改时钟，但本测试不等待 minute tick。
5. TERM/Wait 两个 worker 后核对开通/服务/audit、唯一付款账本、余额10000→9000、库存10→9；核对邮件/SMS sent、各 attempts=1、完成时间及释放租约。SMS 的 encrypted response 用该 outbox/attempt/locked_at 域解密，比对真实 loopback 响应。
6. 协议服务使用 `g09fixture` 的真实 SMTP 与 generic HTTP JSON sinks。journal 必须无失败、恰好两次接受；从源 delivery 字段独立计算收件人和消息 SHA256，按 channel 关联 event→delivery→outbox→协议 sequence。邮件按 SMTP DATA 换行规范化后的 MIME 字节计算，短信按 JSON message 计算；落盘 JSONL 必须等于 snapshot。只证明本次无故障实例各接受一次，不承诺通用 exactly-once。

## 隔离与运行

显式开关未启用时跳过，启用后缺 DSN 直接失败。只接受 URL 形式的 loopback `_test` DSN（含所有 fallback），拒绝 `options`；创建 schema 前只读查询要求 PG18，worker DSN 的 `search_path` 必须精确等于本次 `zjmfgo_test_` 加16位十六进制随机 schema。SQL/API 都限于该 schema；收尾另查 schema 已删除。

API/SMS 仅 IPv4 loopback 随机端口，设置读写超时；SMTP 由既有 fixture 限制 loopback、`.test`、消息大小与连接生命周期。短信只使用 `+999`。没有 supplier、NOKVM、外部支付 intent 或外部目的配置。worker cwd 为临时目录，fallback SMTP 仅127.0.0.1:1；没有外部业务调用。这里没有网络 namespace，不冒充系统调用级外网隔离证明。

所有日志、冻结来源与结果、journal 位于0700临时目录，数据文件0600；日志不自动公开。结果与日志随测试清理，不是永久审计归档。失败也执行 owned worker TERM，超时 KILL 并判失败，Wait 后再清理监听器/库/schema。没有常驻进程或全局 queue=0 断言。

独占执行命令（由主代理安排，本开发回合没有执行）：

```sh
ZJMFGO_G09_SOURCE_WORKER_SMOKE=1 REQUIRE_INTEGRATION=1 scripts/go.sh test \
  ./internal/httpapi -run '^TestG09SourceWorkerLoopbackSmoke$' \
  -count=1 -timeout=10m -v
```

已执行且不访问 PostgreSQL：HTTP 测试包编译、opt-in 默认 SKIP、`TestG09SourceWorkerGuards` 普通与 race 检查通过；guard 包含危险 DSN/schema 拒绝，以及生产 SMTP 实际 loopback 发送后的 ASCII/中文/点转义/换行 hash 比对。`scripts/go.sh build ./cmd/worker` 输出至临时目录后清理，通过。

首次真实执行：`f5d69d2`、PG18、两个实际worker，退出1（47.779秒），SMTP可选用户名省略后错误解析为`<nil>`，邮件失败，冻结来源未全部成功。原始日志及清理核对见[首次失败证据](evidence/g09-tooling-20261017/source-worker-pg-smoke/result.json)；修复与复验另存，不能把默认SKIP或协议guard视为闭环通过。未证明封存 candidate/API 二进制、普通用户 namespace、600任务 cohort、供应商背景、两次 minute tick 稳定性、故障重试、资源预算或容量。API仍是源码 `httptest`，不能称发布候选验收。
