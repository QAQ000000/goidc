# 测试入口

测试按行为和业务域保留，不以测试/生产行数比例作为删除标准。资金、权限、并发、幂等及故障恢复断言是必需的回归保护。

| 范围 | 入口 | 前置条件 |
| --- | --- | --- |
| 编译全部 Go 测试 | `./scripts/go.sh test ./... -run '^$'` | Go 依赖可用；不执行测试 |
| Go 单元及可用集成测试 | `./scripts/go.sh test ./...` | 无测试 DSN 时数据库用例会跳过，不能声称集成验收通过 |
| 强制数据库全量 | `./scripts/test-all.sh` | `ZJMFGO_TEST_DATABASE_URL` 指向独立 PG18、库名以 `_test` 结尾 |
| Node 工具回归 | `node --test scripts/*.test.mjs` | Node；进程、namespace 等专项以各项环境条件为准 |
| worker 强杀恢复 | `./scripts/test-worker-recovery.sh` | 回环 PG18 测试库；实际执行两套 SIGKILL 用例 |
| 浏览器夹具 | `go test` 的具体 `Test…BrowserFixture` | 测试 DSN、新的 ready/finish 路径和外部浏览器驱动 |

浏览器夹具是供浏览器连接的临时 API 环境，单独启动或编译夹具不代表浏览器验收。未显式配置的浏览器、真实外部渠道、旧库及常驻进程专项仍可能跳过。

`internal/httpapi/browser_helpers_test.go` 统一 ready/finish 校验、私有 JSON 写入、等待期限及 worker 退出；各业务文件只维护场景和断言。`stop` 会取消并等待 goroutine，可在断言前调用，也可在清理中重复调用。NOKVM 的测试时钟调整保留在具体场景中。

浏览器夹具常用变量：`ZJMFGO_BROWSER_READY_FILE`、`ZJMFGO_BROWSER_FINISH_FILE`、`ZJMFGO_BROWSER_ORIGIN`、`ZJMFGO_BROWSER_LISTEN_ADDR`。`ZJMFGO_BROWSER_TIMEOUT_SECONDS` 可覆盖等待期限，仍受 Go 测试总期限约束；供应商等专项可有自己的变量。

前端检查仍使用 `npm --prefix web run lint`、`typecheck`、`test:proxy`。构建使用独立目录，禁止绕过运行目录保护；此文档不启动或重启项目服务。

静态审查生成器 `node scripts/route-coverage.mjs` 和 `node scripts/admin-page-contracts.mjs` 将新结果写入忽略的 `output/audit/`。前者仍读取版本化的 `docs/generated/legacy-inventory.json` 与 `docs/audit/route-reviews.json`；生成器所需旧源码及旧页面规范须另行提供。历史生成快照保存在封存包。

幂等回执由 `mutation_operations(actor_id, scope, idempotency_key)` 统一保存。迁移 156 原样搬运旧请求 hash/JSON 结果及已有时间戳，信用冻结从原字段生成等价 hash；原充值操作表没有时间戳，使用迁移时间。13 张回执/批量请求绑定表被移除，关单操作、关单核验请求、服务取消申请继续保存业务状态与外键。

新增写路径在业务事务内先调用 `store.ReplayOperation`，再执行业务和 `store.SaveOperation`；回放前仍须检查当前权限及客户范围。scope 沿用旧表的幂等键域：如 `admin:<operation>`、`affiliate:<operation>`、`order:<action>`，发票申请的创建/审核/调整仍共享 `invoice-application`，不能按 API 动作拆开以绕过旧键冲突。供应商连接测试保留“进行中→完成”的两阶段结果更新。表合并须随对应二进制一起升级；不支持旧二进制继续访问已删除表。

独立 PG18 可运行 `./scripts/go.sh test -race ./internal/store -run '^TestMutationOperation'`，覆盖旧 schema 升级、跨 scope/actor 隔离、首次并发请求、取消等待及回执与审计共同回滚；业务权限/资金回归仍由原测试负责。

存储密文共用 `internal/secure.Seal/Open`，格式保持 `nonce || ciphertext || tag`。调用方继续负责 AAD、JSON/base64 编码、配置密钥长度及业务错误；已有密文无需迁移。`./scripts/go.sh test -race ./internal/secure ./internal/provision` 验证标准向量、旧格式互通、篡改/截断及凭据域隔离，store 和 HTTP 的渠道、支付、MFA、验证码、实名认证及敏感购买字段测试继续验证业务绑定。

9 个设置写接口共用 `internal/httpapi/settings_mutation.go` 的幂等重放、审计、回执和提交流程，版本锁通过共同函数校验；调度保留重放前锁设置行再校验凭据的顺序，登录/MFA、实名认证及合同保留各自权限检查。操作 scope、请求 hash 和响应字段沿用原约定。支付、SMTP/SMS、站点、订单及供应商 API 设置由已有 store 事务负责，继续保留密钥保留/清除、渠道同步、会话复核等不同契约。独立 PG18 用例 `TestSingletonSettingsAuditFailureLeavesNoMutationOrReceipt` 对全部 9 个入口注入审计故障，验证业务/版本/回执一起回滚，随后同键重试成功且重放只产生一次审计。

普通两次查询的分页共用 `internal/httpapi/list_page.go`：调用方保留计数/列表 SQL、投影、筛选和排序，助手绑定分页参数并统一空数组及分页信封。调用方继续选择连接池或已有事务及隔离级别；已有单条 CTE/JSON 列表保持原查询。`TestListPageFiltersBindingEmptyPagesAndErrors` 覆盖筛选、参数绑定、越界页总数、空数组、类型化结果和查询失败。

普通事务共用 `store.WithTx`（基于 `pgx.BeginFunc`），有返回值的 store 操作使用类型化封装；HTTP 的 `Server.withTx` 在提交成功后才发送成功响应，校验失败保持原状态码和错误码并回滚。提交后 Cookie、MFA 失败计数、流式响应及外部调用的分阶段事务继续显式管理。独立 PG18 用例 `TestWithTxCommitRollbackPanicAndDeferredFailure`、`TestHTTPWithTxWaitsForCommitAndPreservesValidationStatus` 验证回调错误、panic、取消及延迟约束导致的提交失败不会遗留写入或误报成功；完整回归使用 `REQUIRE_INTEGRATION=1 ./scripts/go.sh test -race ./... -count=1 -timeout 45m`，同时配置隔离测试 DSN。

请求幂等键统一由 `mutationKey` 解析，必填接口继续要求 8–128 字节；公开工单保留可选请求体键，头/体冲突返回 400。`TestMutationKeyCompatibilityAndBounds` 覆盖旧客户端、空键、修剪及字节边界。

客户端版本写入可用 `store.LockVersion` 保留原行锁与领域冲突提示，再调用 `store.UpdateVersioned` 执行原子 id/version 比较更新。表名和 SET 片段只能来自代码常量，值使用绑定参数；助手不承担权限、审计或回执。worker、批量状态推进和需要额外业务谓词的 SQL 保持显式。`TestVersionedCASConcurrencyBindingAndRollback` 验证并发仅一次成功、过期/缺失版本冲突、参数隔离和事务回滚。

前端共用 `resource-table.tsx` 的表格、分页、详情字段及 `mutation-dialog.tsx` 的确认框；页面继续管理筛选 URL、授权、请求版本和重试键。`data-pages.tsx` 保留原导出入口，业务分别位于报表、订单、服务、账户和通用列表模块；订单展示及开票资料编辑也各自独立。`node --test scripts/resource-components.test.mjs` 验证实际组件的渲染和事件边界，不代表浏览器端到端验收。

G09 来源 HTTP/易支付脚本共用 `g09-source-controller.mjs`，保留旧 CLI、证据文件名、结果字段、精确请求计数及未验证标记；私有清单统一拒绝符号链接、多硬链接和宽松权限。`node --test scripts/g09-source-controller.test.mjs` 验证文件边界，显式 `ZJMFGO_G09_SOURCE_API_SMOKE=1 ZJMFGO_G09_EPAY_SOURCE_SMOKE=1` 的 Go 回归验证真实来源处理链路。运行时验收仍由 `accept-g09-runtime.mjs` 控制、`g09-workload-report.mjs` 汇总；来源冒烟不能证明真实商户、worker 或生产容量。

依赖安装控制流测试 `node --test scripts/prepare-runtime-dependency-input.test.mjs` 在独立 Node 测试进程内 mock `statfs`，按夹具目录实际分配块采样，峰值预算保持 1 MiB。用例验证正常安装、超时及失败清理，2 MiB 写入触发峰值保护，另验证恰好预算/超出一字节、保留空间和 inode 边界。模拟文件系统仅用于夹具，不构成真实磁盘容量证据；生产 `statfs` 和保护逻辑保持原样。

代码量复核以固定提交为边界：`git diff --numstat 3d0b698^ 3d0b698` 为 +2,966/-2,531，净增 435 行；其中 Go/MJS/TSX（含测试）净增 427 行。TSX 从 24,157 增至 24,366 行，前端收益是结构重组与公共流程复用，不能计为代码削减。`git diff --numstat f999984^ 3d0b698 -- '*.go' '*.mjs' '*.tsx'` 合计净减 3,696 行，其中 Go 减 3,089、MJS 减 816、TSX 增 209；单文件缩短不代表全库减量，文档/封存资产另行统计。
