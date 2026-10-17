# G09 供应商源码 API 与真实 worker 小链

状态：独立 opt-in 测试、无 DSN 协议校验及主代理独占 PG18 真实小链均已通过。实际证明范围为源码 API→1个真实 worker→loopback SupplierSink 的1服务/create+API sync 两任务，包含资金/库存不变量、协议落盘 hash、TERM/Wait 和 schema 清理。默认 SKIP 与协议 guard 的历史证据单独保留，不替代真实 PG 结果；容量和发布候选边界仍未关闭。

入口：`internal/httpapi/g09_supplier_worker_smoke_test.go:TestG09SupplierWorkerLoopbackSmoke`，唯一开关为 `ZJMFGO_G09_SUPPLIER_WORKER_SMOKE=1`。不改变现有 source worker opt-in 开关、生产业务、迁移或时序常量；现有 worker helper 保留默认 PUBLIC_ORIGIN，增加一个供本测试显式传 IPv4 loopback origin 的测试 helper。

## 验证的真实来源

1. 只读 PG18 preflight 后，沿用 `testServer` 创建随机独占 schema。fixture 只创建初始客户/管理员身份，禁用初始 mail/SMS 通知渠道和模板；不插入订单、付款、已完成任务或已开通服务充当业务来源。初始 10000 分额度通过管理员余额 API 入账。
2. 真实 loopback HTTP 调用 supplier 创建、catalog、商品 preview、import、publish、binding 查询。导入价格 1000 分、库存 10、成本上限 100 分；publish 携带真实 revision 和 expected_stock。账户密码由生产 API 加密保存。
3. 同一个供应商 loopback listener 在准备阶段只接受精确 method/path 的 login、catalog、product config、user_info 白名单。它复用有 24 请求预算的 SupplierSink 和同一 durable journal；占位资源只满足 sink 构造契约，门禁禁止任何资源操作，必须保持零 settled/paid 资源。不会把占位资源插入业务库。
4. 客户真实 cart→checkout→GET invoice 核对归属、金额、未支付、版本→余额付款，产生一项 pending supplier create job。读取并冻结真实 supplier/binding/product/order/invoice/service/create ID、映射和初始任务状态。计算与生产契约一致的逐服务 HMAC downstream token，并一次性把 listener 切换至仅允许本服务/上游 host 42/上游 invoice 90 的 SupplierSink，预算 48。冻结前违规请求会锁定失败，禁止切换回准备阶段。token、密码、bearer、DSN、请求/响应正文不写日志或结果文件。
5. 现场编译 `cmd/worker`，只启动一个真实子进程；worker cwd 为临时目录，使用环境白名单、明确携带该 schema 的 DSN 和真实 API loopback PUBLIC_ORIGIN。等待生产启动日志和冻结 create ID 成功。测试不调用 Claim、Process、Complete、Recover，不改任务状态/租约/available_at/service 生命周期。
6. create 成功后，通过**管理员** service actions API 请求一个独立 sync job；客户无该动作权限。正常 worker 领取并完成该任务，最长每阶段 30 秒。worker 用生产 `svc-ID` hostname、逐服务 downstream token 和 compiled `zjmf_finance_v1` adapter 走真实 socket 协议。SupplierSink 是受限合成供应商，不代表真实供应商验收。

## 结果不变量和边界

TERM/Wait worker 后核对：两个明确 job 各 attempts=1、succeeded、external_started_at/completed_at 非空、释放任务/账户租约；服务 active、supplier 来源、upstream_id 42、正确 domain/到期日，两个真实 supplier delivery audit 映射；create 结果记录上游账单 90、成本 100 分/CNY。实际客户账单/订单已付，唯一 1000 分付款账本，余额 10000→9000 与两条账本守恒，库存 10→9，没有外部支付交易、信用额消费、邮件/SMS outbox。

以 source job ID 和 journal sequence 边界区分 create、API sync，逐实体核对 9 条响应：clear、add、settle、未付 invoice、Pending host、apply_credit、已付 invoice、Active host，以及后续 sync 的 Active host。预期 JSON 从冻结 ID 和协议常量独立构造并计算 SHA256；每条 recipient hash 必须对应真实 local service ID。校验 sequence 连续、worker 时间窗口、metadata 前缀不变，以及重新读取的 durable JSONL 与 snapshot 完全一致；合成 sink 最终恰好一个 settled/paid 身份。

正常生产 scheduler 保留，每两秒随生产 provisioning tick 运行。API sync pending/running 时正常排除 periodic 候选；本测试完成明确两项来源后立即 TERM，不等待 minute tick 或五分钟重排。若合法 scheduler 产生额外 background job，本 fixture 明确报告“有限两任务预算未能证明”，不把它误报为生产产品缺陷；不会删除背景任务或修改 scheduler/时钟来迁就测试。每阶段超时、sink/journal 错误、失败/待核验终态均使本 fixture 失败。

所有监听器仅 IPv4 loopback，有限 HTTP 超时与 sink 请求预算；账户仅指向当前 sink。进程不继承项目 `.env`、代理或外部凭据，fallback SMTP 为 127.0.0.1:1。没有网络 namespace，因此不宣称系统调用级外网封锁。运行目录0700、日志/冻结来源/结果/journal0600，内容不自动公开，测试结束清除；不是永久审计归档。失败也执行 owned worker TERM，超时 KILL 并判失败，Wait 后再清理 listener、store/schema；最后另查随机 schema 确已移除。

此小链只有1服务、1个 create 和1个 API sync、1个实际 worker，API 仍是源码 httptest Handler。它不证明发布候选/API 二进制、两个并发 worker、600任务 cohort、容量、真实供应商、分钟周期稳定性、故障重试、通用 exactly-once 或资源预算，不关闭 G09 门槛。

## 执行和当前证据

真实执行需要独占隔离 PG18 测试槽；显式开启后缺少合规 loopback `_test` URL DSN 会直接失败，不退化成 skip：

```sh
ZJMFGO_G09_SUPPLIER_WORKER_SMOKE=1 REQUIRE_INTEGRATION=1 scripts/go.sh test \
  ./internal/httpapi -run '^TestG09SupplierWorkerLoopbackSmoke$' \
  -count=1 -timeout=10m -v
```

本开发分支实际执行且未访问 PostgreSQL：HTTP 测试包编译；新 smoke 默认明确 SKIP；`TestG09SupplierWorkerProtocolGuards` 普通和 race PASS。guard 通过真实 compiled supplier adapter 的 socket Product/Create/Sync 验证新 durable hash oracle 和一次性冻结切换，另证未冻结 settle 被拒绝并锁定。race 同时包含既有 `TestG09SourceWorkerGuards`，PASS。另实际编译 `cmd/worker` 成功并清理临时输出，没有启动进程；开启新 opt-in 但清空 DSN 的负向执行按预期失败于 pool 构造之前。归一化执行摘要见 [no-PG evidence](evidence/g09-supplier-worker-smoke-20261017/no-pg-validation.json)，其明确标识非原始 subprocess 日志。上述证据不涉及真实 worker/资金/库存/业务 source API。这是开发阶段未连 PG 的历史记录；后续主代理真实 PG 验收另见下文。


## 主代理独占 PG18 实际验收

主代理于 2026-10-17 05:35:39–05:36:09 UTC，在固定源码提交 `c67bfb2351684e0d58abeeba63d9031d5fe7f13d` 上独占隔离 PostgreSQL 18.0 执行四项顶层测试 `-race` 和 `scripts/go.sh vet ./internal/httpapi`，两条命令均退出0。四项全部实际 PASS，无 SKIP：旧 `TestG09SourceWorkerLoopbackSmoke` 4.74秒（两个真实 worker），旧 source guards 0.01秒，新 supplier smoke 6.67秒，新 supplier protocol guards 0.09秒，HTTP 包12.553秒。

本次 supplier smoke 在 worker 启动前记录6次 durable metadata 接受、零准备阶段资源，真实服务/create ID 均为1；随后实际 create 和管理员 API sync 完成，SQL 来源映射、余额/账本/库存、上游资源/账单 ID、逐实体 durable 协议 hash 及 worker TERM/Wait 均通过测试内断言。有限背景预算断言通过。主代理外部核对 `source_unchanged=true`、随机 schema 集合前后恢复、原有 API/worker/Web 三个常驻进程 PID/start ticks/exe/cwd 不变，API/Web 前后均200。

原始专项测试日志与带命令、版本、源码 hash 和清理/健康核对的结果分别为 [test.log](evidence/g09-supplier-worker-smoke-20261017/real-pg/test.log) 与 [result.json](evidence/g09-supplier-worker-smoke-20261017/real-pg/result.json)。证据无 DSN、密码、token 或业务请求/响应正文。此验收没有改已测代码，也没有部署或推送；仍只证明上述有限源码/loopback 小链，不代表封存 candidate、600任务、容量或真实供应商验收。
