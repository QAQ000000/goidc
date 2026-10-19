# 测试入口

完整的开发生命周期、证据等级和领域到测试的对应关系见[工程治理契约](architecture/engineering-governance.md)及[领域验收矩阵](architecture/contract-verification-matrix.md)。本文只维护具体测试入口、前置条件和不应过度解释的边界。

编译模块的字段/凭据/schema、默认装配、前端组件和候选清单验证见[编译模块契约](compiled-module-architecture.md#验证入口与发布边界)。配置 `ZJMFGO_TEST_DATABASE_URL` 和 `REQUIRE_INTEGRATION=1` 才能执行其中数据库专项；模块清单/组件测试通过不代表实际渠道已连通。

应用文件存储使用 `node scripts/test-file-storage.mjs --unit` 和 `--integration`；后者要求独立回环 PG18 `_test` 库，逐入口核对 run/pass 并拒绝 skip。覆盖固定 profile、私有目录/S3 本地协议、五类 API、历史字节、权限撤销、Range、审计回滚、未知上传、回收围栏及真实 schema166 升级。配置、备份和验证边界见[存储契约](architecture/file-storage.md)。

测试按行为和业务域保留，不以测试/生产行数比例作为删除标准。资金、权限、并发、幂等及故障恢复断言是必需的回归保护。

| 范围 | 入口 | 前置条件 |
| --- | --- | --- |
| 编译全部 Go 测试 | `./scripts/go.sh test ./... -run '^$'` | Go 依赖可用；不执行测试 |
| Go 单元及可用集成测试 | `./scripts/go.sh test ./...` | 无测试 DSN 时数据库用例会跳过，不能声称集成验收通过 |
| 报表 JSON 查询 | `./scripts/go.sh test ./internal/httpapi -run '^TestWriteReportJSONOnlyFormats$'`；集成入口 `TestReportEndpointsJSONOnlyFormatsAndPermissions`，金额、权限和分页见[报表回归](credit-cash-reporting.md#回归入口) | 格式解析可独立验证；接口权限/范围仍须独立 PG18 回归 |
| 服务取消与续费 | `node scripts/test-service-cancellations.mjs --integration` | 显式回环 PG18 `_test` URL；串行 race、逐入口 run/pass 零 skip；API/资金兼容、执行围栏及真实 schema168→169，规则与部署边界见[交易契约](transactions.md#服务取消与续费互斥) |
| 内部调度任务 | `node scripts/test-scheduler.mjs --unit` / `--integration`、`node --test scripts/scheduler-components.test.mjs` | 集成须显式回环 PG18 `_test` URL，逐项 run/pass 零 skip；目录/取消/审计和日续费回归，真实 worker 与浏览器另验，见[调度契约](architecture/scheduler.md) |
| 强制数据库全量 | `./scripts/test-all.sh` | `ZJMFGO_TEST_DATABASE_URL` 指向独立 PG18、库名以 `_test` 结尾 |
| Node 工具回归 | `node --test scripts/*.test.mjs` | Node；进程、namespace 等专项以各项环境条件为准 |
| 浏览器文件保存与下载请求 | `node --test scripts/download.test.mjs` | Node 与 Web 依赖；执行真实 helper/组件及 DOM/fetch 夹具，不启动浏览器 |
| 站点主题展示 | `node --test scripts/site-theme.test.mjs`；后端 `TestSiteContentVersionedCRUDAndPermissions`、`TestSiteThemeValidationPermissionsAndReplay` | Node/Web 依赖；后端用例须隔离 PG18；SSR/组件夹具不等于真实浏览器视觉验收 |
| 实际模块二进制 | `node scripts/check-module-binaries.mjs` | Linux、strace；真实构建 API/worker，在空应用配置下拒绝并检查全部网络 syscall |
| 渠道与通知单元门禁 | `node scripts/test-compiled-notifications.mjs --unit` | 38 个指定入口，race；不继承应用配置或测试 DSN |
| 渠道与通知集成门禁 | `node scripts/test-compiled-notifications.mjs --integration` | 回环 PG18 `_test` URL；86 个指定入口，串行 race，任一 skip、缺失或失败均阻断 |
| 实名认证提供方单元门禁 | `node scripts/test-compiled-certification.mjs --unit` | 提供方工厂、旧人工兼容、独立 worker 取消与日志隐私；race |
| 实名认证提供方集成门禁 | `node scripts/test-compiled-certification.mjs --integration` | 回环 PG18 `_test` URL；串行 race，指定入口任一 skip、缺失或失败均阻断 |
| 实名认证前端 | `node --test scripts/certification-components.test.mjs` | 实例选择、不可用原因、处理状态及稳定幂等键；组件夹具不代替浏览器验收 |
| 支付运行单元门禁 | `node scripts/test-compiled-payments.mjs --unit` | 注册/协议/历史流水身份、无网络链接生成、快照加密与版本拒绝；race，不继承业务配置 |
| 支付运行集成门禁 | `node scripts/test-compiled-payments.mjs --integration` | 显式回环 PG18 `_test` URL；第二测试协议、双实例/历史凭据、资金/退款/关单边界及真实 schema164 升级；逐入口 run/pass，零 skip |
| 供应商单元门禁 | `node scripts/test-compiled-suppliers.mjs --unit` | 配置/身份/能力声明、命名凭据及旧协议本地校验，race |
| 供应商集成门禁 | `node scripts/test-compiled-suppliers.mjs --integration` | 显式回环 PG18 `_test` URL；第二测试提供方、账户隔离、真实交付/续费/未知结果、worker SIGKILL、生命周期能力/人工核验/付清恢复及 schema165/169/170 升级；逐入口 run/pass，零 skip |
| 支付查询组件 | `node --test scripts/payment-reconciliation-components.test.mjs` | 通用方法能力、权限、查询状态及失败重试；组件事件夹具不代替浏览器 |
| worker 强杀恢复 | `./scripts/test-worker-recovery.sh` | 回环 PG18 测试库；实际执行两套 SIGKILL 用例 |
| 浏览器夹具 | `go test` 的具体 `Test…BrowserFixture` | 测试 DSN、新的 ready/finish 路径和外部浏览器驱动 |

浏览器夹具是供浏览器连接的临时 API 环境，单独启动或编译夹具不代表浏览器验收。未显式配置的浏览器、真实外部渠道、旧库及常驻进程专项仍可能跳过。

`TestDefaultProfileMatchesCompiledModules` 按 `audit/module-build-profile.json` 比较实际 API/worker 的 profile、契约版本和完整模块清单，逐项检查描述摘要，并按模块键验证 Telegram 与内置 PNG 的必需工厂；不固定模块数量或清单位置。清单必须随装配 profile 变更，不把某次历史模块数当作长期断言。

实名认证规则见[领域契约](domain/certification.md)。认证 runner 核对实际执行的 Go JSON 事件和包结果，覆盖实例及密钥快照、未知提交只查询、过期租约与人工决定围栏、查询上限、审核事务、设置锁顺序、回执等待后撤权、提供方通过后的下单/API 密钥资格，以及从真实 163 起点升级。测试提供方只在测试中注册；默认发布清单仍只有人工提供方。租约和查询间隔可由测试 SQL 加速，不证明实际恢复耗时或真实认证厂商的协议。worker 单元夹具验证独立循环及取消，未强杀完整生产认证进程。

认证锁竞争用例用独立直连观察 `pg_stat_activity`，连接、整轮观察和每条查询均有超时，不从被测业务池借观察连接。设置锁序用例将业务池固定为四个连接，保留 blocker、领取、新申请、设置更新的真实等待，并断言业务池已占满后释放阻塞；观察连接避免测试自身池饥饿，不改变生产锁顺序或取消真实竞争。

取消续费 runner 同样核对 Go JSON 和包结果，拒绝无 DSN、非法连接覆盖、缺失入口及任何 skip。HTTP 回归覆盖待审可付款、批准后各付款方式/自动续费拒绝、真实未决支付阻审批、回调后最新权益、worker 延期及人工确认；并以 `pg_blocking_pids` 确认审批真实等待服务锁，验证重叠的余额、信用和在线付款立即冲突、不写资金，释放锁后审批可完成。store 回归从从未运行 169 的实际 schema168 造旧冲突，验证资金/意图/权益不改、原任务及审计保留、新审批 generation、歧义历史整次回滚与数据库入闸。时间及历史状态由隔离夹具控制；远端 CI、浏览器、真实支付/NOKVM 和生产迁移切换各需独立证据。

支付接入规则见[支付模块开发契约](architecture/payment-module-development.md)。支付 runner 覆盖第二个仅测试装配的独立协议、两项命名密钥、指定实例与跨实例幂等、轮换/停用后的原交易、手动/worker 查询、可信关单及独立只读核验、迟到资金事实、退款商户与共享 payout claim、审计回滚和 164 升级。时间与查询计划可由测试 SQL 加速；没有真实商户扣款或退款网络请求，也没有远程预下单实现。能力组件验收不证明浏览器视觉，远端 CI、真实渠道和候选切换继续单列。

供应商接入规则见[供应商模块开发契约](architecture/supplier-module-development.md)。`scripts/test-compiled-suppliers.mjs` 强制设置 worker 崩溃恢复开关，验证第二测试提供方双账户/双密钥、身份冻结与重复身份、schema/CAS、审计回滚、原魔方密文兼容、真实 store 任务与生产 worker 子进程。上游为本地模拟器，真实资源/扣费/网络没有因此获得外部验收。`node --test scripts/supplier-components.test.mjs` 验证 schema 表单、密钥保留/清除、权限与数量型规格成本，以及人工生命周期确认、未知响应原键重试和等待状态隔离，不代表真实浏览器。

供应商批量导入回归另覆盖 174→175→176 原样保留历史/checksum、全批回滚、同键重放及并发、读取期间撤权/注销、全部支持账期和阶梯/购买字段复制；可配置服务按各自选择验证真实上游 payload、敏感字段加密、缺失/跨订单项选择拒绝派发，以及按所购账期的人工/自动续费。新增回归验证普通 `0-ID` 配置根节点、半年及多年账期、试用/自定义时长省略提示、区间单价与累进算法的差异，以及半年/两年/十年付的结账、开通、人工/自动续费和不足整周期的拒绝。前端组件覆盖跨页选择、比例、预览版本、原请求恢复和只读规格；真实浏览器单独验证完整交互，证据写入忽略的 `output/`。可售账期和不支持类型见[供应商批量导入契约](supplier-protocol.md#批量导入与客户可选规格)。

自动续费严格入口另覆盖上游未来期限只同步不扣款、建单后关闭/变更期限/代次/取消及供应商停用/版本变化的付款围栏、人工同 key 缺少可信来源、余额不足保留原单且充值后不追扣、报价/上游账单变价的已知失败与原单退款，以及付款与完整期限确认之间的通知边界。`TestSupplierRenewalWorkerSIGKILLRecovery` 在真实 worker 的 `host/renew` 回执无 checkpoint 和 `apply_credit` 生效后回执丢失两个位置强杀进程，前者冻结不重发，后者只读核验原账单再确认期限；传输为本地模拟器，不代表真实上游故障恢复。`TestAutoRenewalUpgrade170PreservesHistoryAndDoesNotInferSource` 从实际 schema170 升至171，核对原资金、服务、任务及租约不改、历史相似 key 不获自动来源、重复迁移及 checksum。

## 编译渠道与通知运行验收

Web Push 归同一严格通知 runner：RFC8291 解密与 VAPID 验签使用本地 TLS 模拟器，独立 PG18 覆盖多浏览器去重、加密、当前会话/公钥及资源归属、410 定向失效、显式 429、未知结果不重发、173→174 保留升级和仅 Web Push 的真实提醒扫描。只读详情无需开启站内信，按事件及当前资源权限过滤。前端另执行 `node --test scripts/web-push-components.test.mjs scripts/web-push-proxy.test.mjs`；真实浏览器的系统后台收件仍需 HTTPS、厂商网络与独立证据。

`.github/workflows/quality.yml` 的独立 PG18 job 使用任务自有容器及测试库，强制执行上述 86 个入口。runner 固定 `-race -count=1 -p=1 -parallel=1`，逐项核对 Go JSON 的 run、pass 和包结果；无 DSN、非法连接覆盖参数、测试改名或任何 skip 都会失败。普通 CI 不启动等待人工操作的浏览器夹具或子进程 helper。

`TestNotificationCenterProcessRecovery` 在父测试拥有的子进程中运行生产 `RunNotificationCenter`、Store 和适配器，仅将 Telegram HTTP 传输重定向到回环模拟器。它在平台接受响应后确认生产完成事务正在等待真实行锁，再 SIGKILL，验证重启后 unknown 不重发、站内信一次性、双进程领取及慢 Telegram 期间旧邮件/短信链独立推进。旧链由伴随测试循环驱动；这不等于强杀完整 `cmd/worker`。租约由 SQL 加速，不证明实际租约等待耗时或生产 RTO。

`TestNotificationCenterUpgradeFrom160` 从从未创建过 161/162 对象的真实迁移 160 起点写入合成旧密文、付款回执和待发邮件/短信，再由生产 `Migrate` 升级。它检查 checksum、幂等、旧值及凭据 AAD、待发任务重试元数据、默认关闭和历史付款回放不补发。普通运行只验证 `Store.CheckSchema`；实际候选启动门禁需要同时设置以下变量，CI PG18 job 会构建并强制提供：

```bash
ZJMFGO_UPGRADE_BINARY_ACCEPTANCE=1 \
ZJMFGO_UPGRADE_API_BINARY=/absolute/candidate/bin/api \
ZJMFGO_UPGRADE_WORKER_BINARY=/absolute/candidate/bin/worker \
node scripts/test-compiled-notifications.mjs --integration
```

同时配置 `ZJMFGO_TEST_DATABASE_URL`。实际进程必须因 160/161/162/172 schema 不满足而退出，配置错误或连接失败不能替代该断言。此验证不证明旧二进制降级、生产备份恢复、真实外发或正式切流；候选完整运行仍按发布手册单独验收。

`TestNotificationLifecycleUpgradeFrom162` 从实际 162 保留已启用路由、已编辑模板、付款订阅版本和待发正文，再升到 163；旧偏好仍仅付款，原消息仍能写入收件箱，新增事件默认关闭且不补历史。`TestNotificationLifecycle*` 与 `TestLifecycleBusiness*` 还覆盖 11 事件目录、事件变量白名单、显式业务订阅、Telegram-only 提醒、真实下单及确认状态、精确截止点变化、上海日期、关闭路由、限量并发排空和事务回滚。NOKVM 覆盖结果写入与人工核验，协议和供应商回调使用本地夹具，不证明真实平台交付。

跨日补偿与旧提醒来源回归归同一通知严格 runner：`TestNotificationCatchup*` 在 UTC 会话验证上海日界、精确期限后 72 小时内补扫与超窗不补、固定阶段去重、并发限量排空、已有 `unknown`/`skipped` 不重建，以及期限后新增路由、模板、订阅、绑定或实例不追溯授权。`TestNotificationLegacyDeadline*`、`TestNotificationLegacyBilling*` 另验旧邮件、短信的上海日期、期限已过及续费/付款/阶段/收件地址变更后的发送前失效；旧日期记录的前后一日服务去重、账单 `created_at` 上海采样日及新精确期限无容差单独保护。从实际 schema172 升级到 173，要求来源不明的旧待发、发送中、死信提醒停止后续发送，已发历史及非提醒业务不改。时间和历史资格由隔离夹具控制，不代表生产实际停机时长或真实渠道收件。

`internal/httpapi/browser_helpers_test.go` 统一 ready/finish 校验、私有 JSON 写入、等待期限及 worker 退出；各业务文件只维护场景和断言。`stop` 会取消并等待 goroutine，可在断言前调用，也可在清理中重复调用。NOKVM 的测试时钟调整保留在具体场景中。

浏览器夹具常用变量：`ZJMFGO_BROWSER_READY_FILE`、`ZJMFGO_BROWSER_FINISH_FILE`、`ZJMFGO_BROWSER_ORIGIN`、`ZJMFGO_BROWSER_LISTEN_ADDR`。`ZJMFGO_BROWSER_TIMEOUT_SECONDS` 可覆盖等待期限，仍受 Go 测试总期限约束；供应商等专项可有自己的变量。

前端检查仍使用 `npm --prefix web run lint`、`typecheck`、`test:proxy`。构建使用独立目录，禁止绕过运行目录保护；此文档不启动或重启项目服务。

静态审查生成器 `node scripts/route-coverage.mjs` 和 `node scripts/admin-page-contracts.mjs` 将新结果写入忽略的 `output/audit/`。前者读取被忽略的 `output/audit-inputs/legacy-inventory.json` 与 `output/audit-inputs/route-reviews.json`，也可用 `GOIDC_LEGACY_INVENTORY`、`GOIDC_ROUTE_REVIEWS` 指向受控输入；生成器所需旧源码及旧页面规范须另行提供。机器输入与活动文档分离，历史生成快照保存在封存索引或外部构建产物中。

幂等回执由 `mutation_operations(actor_id, scope, idempotency_key)` 统一保存。迁移 156 原样搬运旧请求 hash/JSON 结果及已有时间戳，信用冻结从原字段生成等价 hash；原充值操作表没有时间戳，使用迁移时间。13 张回执/批量请求绑定表被移除，关单操作、关单核验请求、服务取消申请继续保存业务状态与外键。

新增写路径在业务事务内先调用 `store.ReplayOperation`，再执行业务和 `store.SaveOperation`；回放前仍须检查当前权限及客户范围。scope 沿用旧表的幂等键域：如 `admin:<operation>`、`affiliate:<operation>`、`order:<action>`，发票申请的创建/审核/调整仍共享 `invoice-application`，不能按 API 动作拆开以绕过旧键冲突。供应商连接测试保留“进行中→完成”的两阶段结果更新。表合并须随对应二进制一起升级；不支持旧二进制继续访问已删除表。

独立 PG18 可运行 `./scripts/go.sh test -race ./internal/store -run '^TestMutationOperation'`，覆盖旧 schema 升级、跨 scope/actor 隔离、首次并发请求、取消等待及回执与审计共同回滚；业务权限/资金回归仍由原测试负责。

存储密文共用 `internal/secure.Seal/Open`，格式保持 `nonce || ciphertext || tag`。调用方继续负责 AAD、JSON/base64 编码、配置密钥长度及业务错误；已有密文无需迁移。`./scripts/go.sh test -race ./internal/secure ./internal/provision` 验证标准向量、旧格式互通、篡改/截断及凭据域隔离，store 和 HTTP 的渠道、支付、MFA、验证码、实名认证及敏感购买字段测试继续验证业务绑定。

9 个设置写接口共用 `internal/httpapi/settings_mutation.go` 的幂等重放、审计、回执和提交流程，版本锁通过共同函数校验；调度保留重放前锁设置行再校验凭据的顺序，登录/MFA、实名认证及合同保留各自权限检查。操作 scope、请求 hash 和响应字段沿用原约定。支付、SMTP/SMS、站点、订单及供应商 API 设置由已有 store 事务负责，继续保留密钥保留/清除、渠道同步、会话复核等不同契约。独立 PG18 用例 `TestSingletonSettingsAuditFailureLeavesNoMutationOrReceipt` 对全部 9 个入口注入审计故障，验证业务/版本/回执一起回滚，随后同键重试成功且重放只产生一次审计。

普通两次查询的分页共用 `internal/httpapi/list_page.go`：调用方保留计数/列表 SQL、投影、筛选和排序，助手绑定分页参数并统一空数组及分页信封。调用方继续选择连接池或已有事务及隔离级别；已有单条 CTE/JSON 列表保持原查询。`TestListPageFiltersBindingEmptyPagesAndErrors` 覆盖筛选、参数绑定、越界页总数、空数组、类型化结果和查询失败。

普通事务共用 `store.WithTx`（基于 `pgx.BeginFunc`），有返回值的 store 操作使用类型化封装；HTTP 的 `Server.withTx` 在提交成功后才发送成功响应，校验失败保持原状态码和错误码并回滚。提交后 Cookie、MFA 失败计数、流式响应及外部调用的分阶段事务继续显式管理。独立 PG18 用例 `TestWithTxCommitRollbackPanicAndDeferredFailure`、`TestHTTPWithTxWaitsForCommitAndPreservesValidationStatus` 验证回调错误、panic、取消及延迟约束导致的提交失败不会遗留写入或误报成功；完整回归使用 `REQUIRE_INTEGRATION=1 ./scripts/go.sh test -race ./... -count=1 -timeout 45m`，同时配置隔离测试 DSN。

请求幂等键统一由 `mutationKey` 解析，必填接口继续要求 8–128 字节；公开工单保留可选请求体键，头/体冲突返回 400。`TestMutationKeyCompatibilityAndBounds` 覆盖旧客户端、空键、修剪及字节边界。

客户端版本写入可用 `store.LockVersion` 保留原行锁与领域冲突提示，再调用 `store.UpdateVersioned` 执行原子 id/version 比较更新。表名和 SET 片段只能来自代码常量，值使用绑定参数；助手不承担权限、审计或回执。worker、批量状态推进和需要额外业务谓词的 SQL 保持显式。`TestVersionedCASConcurrencyBindingAndRollback` 验证并发仅一次成功、过期/缺失版本冲突、参数隔离和事务回滚。

前端共用 `resource-table.tsx` 的表格、分页、详情字段及 `mutation-dialog.tsx` 的确认框；页面继续管理筛选 URL、授权、请求版本和重试键。`data-pages.tsx` 保留组件重新导出的兼容入口，业务分别位于报表、订单、服务、账户和通用列表模块；订单展示及开票资料编辑也各自独立。`node --test scripts/resource-components.test.mjs` 验证实际组件的渲染和事件边界，不代表浏览器端到端验收。

G09 来源 HTTP/易支付脚本共用 `g09-source-controller.mjs`，保留旧 CLI、证据文件名、结果字段、精确请求计数及未验证标记；私有清单统一拒绝符号链接、多硬链接和宽松权限。`node --test scripts/g09-source-controller.test.mjs` 验证文件边界，显式 `ZJMFGO_G09_SOURCE_API_SMOKE=1 ZJMFGO_G09_EPAY_SOURCE_SMOKE=1` 的 Go 回归验证真实来源处理链路。运行时验收仍由 `accept-g09-runtime.mjs` 控制、`g09-workload-report.mjs` 汇总；来源冒烟不能证明真实商户、worker 或生产容量。

依赖安装控制流测试 `node --test scripts/prepare-runtime-dependency-input.test.mjs` 在独立 Node 测试进程内 mock `statfs`，按夹具目录实际分配块采样，峰值预算保持 1 MiB。用例验证正常安装、超时及失败清理，2 MiB 写入触发峰值保护，另验证恰好预算/超出一字节、保留空间和 inode 边界。模拟文件系统仅用于夹具，不构成真实磁盘容量证据；生产 `statfs` 和保护逻辑保持原样。

代码量复核以固定提交为边界：`git diff --numstat 3d0b698^ 3d0b698` 为 +2,966/-2,531，净增 435 行；其中 Go/MJS/TSX（含测试）净增 427 行。TSX 从 24,157 增至 24,366 行，前端收益是结构重组与公共流程复用，不能计为代码削减。`git diff --numstat f999984^ 3d0b698 -- '*.go' '*.mjs' '*.tsx'` 合计净减 3,696 行，其中 Go 减 3,089、MJS 减 816、TSX 增 209；单文件缩短不代表全库减量，文档/封存资产另行统计。
