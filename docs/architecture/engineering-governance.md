# 工程治理契约

状态：当前生效。本文规定 GoIDC 从需求到发布的共同工作方式；业务规则仍以代码、数据库迁移、稳定领域契约和测试为准。

## 适用范围与事实优先级

GoIDC 是 Go API、Next.js/React、PostgreSQL 18 和 worker 组成的业务系统，外部支付、供应商、provision 和 notification 适配器编译进 Go 二进制。本文约束开发、验收、Agent 协作和发布证据，不替代这些领域的业务契约。

应用文件存储同样编译装配，部署 profile 绑定私有目录或 S3；历史数据库字节保持兼容。`scripts/test-file-storage.mjs` 在 CI 分别执行单元和独立 PG18 集成，拒绝缺失、失败或跳过的入口，验证未知上传、授权下载、延迟回收及 schema166 升级；真实对象服务与生产切换另验。

事实优先级按以下顺序执行：

1. 当前代码、数据库迁移和可重复测试决定已经实现的行为。
2. [文档总入口](../INDEX.md) 和稳定命名的领域契约说明当前约束、差距和入口。
3. 归档快照只说明历史范围，不能覆盖当前代码或当前差距审计。
4. `output/`、截图、日志、原始 JSON 和压缩包是运行或审计工件，不是活动知识文档。

## 生命周期门禁

每个跨模块或面向用户的变更都要留下从契约到证据的连续链路。小型内部修复可以合并步骤，但不能跳过行为测试和风险审查。

| 阶段 | 必须产出 | 通过条件 |
| --- | --- | --- |
| Plan | 范围、负责人、影响领域、非目标、回滚边界 | 明确当前契约和未知外部条件 |
| Design | API/页面、数据库、状态机、错误、权限、幂等、并发和审计规则 | 领域契约或稳定文档已更新 |
| Build | 实现、迁移、测试夹具和必要的兼容适配 | 变更不绕过事务、权限或版本约束 |
| Test | 单元、集成、浏览器、worker、外部依赖中适用的证据 | 只声明实际执行过的范围 |
| Review | diff、数据损失、权限越界、重复写入、失败恢复和边界检查 | 高风险项有明确处理或阻断理由 |
| Document | 入口、当前规则、差距和运行手册 | 不新增日期过程文档替代稳定契约 |
| Deploy/Maintain | 候选构建、健康检查、监控、切换和回退证据 | 可定位提交、配置、数据库和恢复步骤 |

## 契约到测试

行为变更至少要能回答“规则在哪里、由什么测试保护、证据达到哪一级”。具体命令和前置条件见[测试说明](../testing.md)。

| 领域 | 必查行为 | 最低验证 |
| --- | --- | --- |
| 支付、余额、发票 | 金额整数分、账本、未知外部结果、退款和重复请求 | Go 单元 + 独立 PG18 事务/幂等测试；真实渠道另行标记 |
| 订单与服务 | 状态机、版本冲突、交付、续费、变更和取消；批准取消与续费付款互斥、既有外部资金事实保留 | Go 业务回归 + `scripts/test-service-cancellations.mjs --integration` + worker 恢复；外部供应商结果不能用模拟代替 |
| RBAC 与后台 | 身份、角色、资源范围、撤权、页面/API 一致性 | API 权限测试 + 页面契约静态检查；浏览器验收单独记录 |
| 供应商、NOKVM、通知 | 请求协议、超时、重试、凭据、未知结果和人工复核 | 适配器测试 + 隔离运行证据；真实渠道另行验收 |
| 迁移与切换 | checksum、旧数据映射、冻结、回滚和切回 | 独立 PG 演练 + 候选构建；不能只凭静态盘点宣称完成 |
| 前端与 API | 路由、加载/空/错误/冲突状态、响应式和错误码 | lint、typecheck、组件/代理测试；浏览器结果标明尺寸和环境 |

## 证据等级

证据等级用于限制发布声明，不是把静态检查包装成生产证明。较高等级不能掩盖较低等级缺失的行为覆盖。

| 等级 | 含义 | 可以声明 |
| --- | --- | --- |
| E0 | 静态代码、迁移、链接或文档检查 | 结构或来源存在 |
| E1 | 独立 PG18 测试库、隔离进程或可重复单元测试 | 代码在指定测试边界内通过 |
| E2 | 浏览器夹具或真实前端流程 | 指定环境、账户、尺寸和流程通过 |
| E3 | 真实供应商、支付、邮件或旧系统链路 | 指定外部依赖和时间窗口通过 |
| E4 | 候选构建、生产配置、监控、切换和回退均已验证 | 可以进入明确批准的生产阶段 |

没有 E1 的状态机、资金或权限结论不得写成“集成已通过”；没有 E3 的外部渠道结论不得写成“生产可用”。

## Agent、Prompt 与知识库边界

- 根 [`AGENTS.md`](../../AGENTS.md) 只放全局规则；目录级 `AGENTS.md` 只放该目录的可执行约束。
- Agent 从 [`docs/INDEX.md`](../INDEX.md) 开始，按 architecture、domain、admin、migration 下钻；不要一次读取整个 `docs/`。
- 检索上下文只保留当前任务相关的稳定契约。历史快照必须明确标记为历史，`output/` 不作为活动知识来源。
- 若接入 RAG，至少记录 `domain`、`status`、`source`、`source_commit` 和有效期；按领域过滤并限制返回数量，结果必须带来源路径。
- Prompt、模型版本和评测数据要可追踪；修改提示或模型时运行对应回归集，不以一次人工示例代替评测。
- 任何会写入支付、订单、权限或供应商状态的 Agent 操作都必须经过现有 API、权限、幂等、审计和人工批准边界。

## 生产与安全控制

- 密钥只从环境变量或受控 Secret Manager 读取，定期轮换；凭据、Cookie、请求体和外部响应中的敏感字段不得进入公共证据。
- 将 429、503、超时、`Retry-After`、计费/配额错误和未知外部结果分开处理；未知写结果停止自动重发，进入待核验状态。
- 对 API、worker、数据库和外部适配器记录结构化日志、指标和 trace 关联；日志不替代业务审计。
- 发布必须能关联提交、迁移 checksum、构建产物、配置摘要、健康检查和回退步骤；构建不能覆盖正在运行的 Web 目录。
- 取消续费的迁移 169、API 与 worker 成套发布：先停旧 API、worker 和其他旧 writer，再升级 schema/checksum 并启用配套候选。冲突历史仅对未派发 pending 任务安全重开；歧义历史阻断升级，须人工核实资源及资金。新 schema 不支持直接降级旧二进制，详见[交易契约](../transactions.md#历史冲突与发布边界)。
- 长任务只有在确有上下文或并发需求时才使用 background、compaction、异步工具调用或 multi-agent，并保留取消和超时边界。

## CI 门禁

### 架构依赖与存量持久化边界

`internal/archtest` 在 CI 中显式执行，约束核心 `internal` 包不得依赖 `internal/httpapi`，并检查 HTTP 层直接访问数据库的现存债务。它通过 Go 类型、窄接口签名和传递 SQL 字符串的辅助函数识别调用，不按 `tx`、`Pool` 等变量名或 SQL 文本前缀放行。HTTP 入口应调用 Store 导出方法，将数据库事务和持久化规则留在 Store。

[`http-persistence-baseline.json`](../../internal/archtest/testdata/http-persistence-baseline.json) 只登记现存调用的受控上限，不是给旧文件或新领域的持久化许可。条目绑定文件、函数、数据库操作、完整声明的规范化 AST 哈希及引用常量值哈希。新增条目、修改绑定函数体或提高调用数量均阻断；移除调用、迁移到 Store 或删除源码后，必须同步减少或删除基线条目。现阶段以整函数收口：同一函数还有旧数据库访问时，局部修改也会阻断。空行和 Go 排版不影响哈希，变量改名及业务修改可能触发门禁，不能通过重建基线接受；应先把该函数的持久化抽到 Store，或单独评审架构策略变更。

本批扫描边界是已有 HTTP 包布局：新增构建标签、平台专用 Go 文件或 HTTP 嵌套子包直接失败，避免它们被静默漏扫；后续扩展这些布局必须同步设计扫描和验证策略。该检查不是通用的跨构建全程序数据流分析，例如动态 SQL 经可变全局值或自定义结构传递仍需评审；修改守卫、工作流本身也必须评审，不能把基线当作防篡改安全边界。

CI 的 `GOIDC_ARCH_BASE_REF` 与文档校验的 `DOC_BASE` 使用同一提交：PR 使用目标分支基准，`main` 推送使用事件的前一个远端提交，覆盖一次推送内的全部提交；首次推送的全零基准回退到当前历史的根提交。不能解析的基准直接失败，不退化成仅检查最后一个提交。当前基线必须同时满足源码扫描和历史基线只能缩减的约束；基准提交尚未包含基线时，从该提交的源码只读采集允许范围再比较，不直接放行新增债务。

`./scripts/go.sh test ./... -run '^$'` 只编译测试，不执行这些断言；本地和 CI 均须另跑 `./scripts/go.sh test ./internal/archtest -count=1`。测试要求有 Git 历史的源码 checkout 和可用的 Go 导出数据；本地默认与 `HEAD` 比较，跨多提交检查时显式设置 `GOIDC_ARCH_BASE_REF`。架构门禁不连接业务数据库，只证明所检查的依赖和调用边界；它不证明统一幂等、权限、资金、事务或外部链路正确，相关行为继续由领域回归保护。

### 领域与运行门禁

`.github/workflows/quality.yml` 是 Pull Request 和 `main` 推送的静态与独立行为门禁，执行文档/契约校验、Go 编译、报表 JSON 格式边界、渠道通知单元 race、实名认证提供方与支付运行单元 race、真实 API/worker 无网络模块清单，以及 Web lint/typecheck、代理、下载、主题和实名认证组件测试。独立 PG18 job 的通知 runner 强制执行声明的集成入口，覆盖通知进程崩溃恢复、72 小时跨日补偿、上海日界及旧提醒来源升级，并构建 API/worker 验证 160/161/162/172 启动拒绝；runner 逐项核对 Go JSON 的 run/pass，缺失、失败或任何 skip 均阻断。该 job 另执行 8 项站点公开主题、管理权限与维护隔离回归，以及实名认证严格集成 runner，覆盖实例快照、未知结果查询、租约与人工围栏、业务资格和 163 升级。同库另执行供应商严格 runner，验证账户身份/命名密钥、真实任务、未知写结果、worker SIGKILL 与 schema165 升级；另执行支付严格 runner，覆盖第二测试协议、多实例与历史密钥、查询/关单/退款边界和 164 升级，逐入口要求零 skip。另执行验证码严格 runner，覆盖图片/点击/widget、配置绑定、旧挑战兼容和并发单次消费；真实厂商另验。另执行内部调度严格 runner，覆盖目录权限、真实分发、审计故障和日续费全量去重。数据库为任务自有容器，只监听回环并在结束时清理。GitHub 上的实际 job 结果仍须单独记录，本地等价执行不能冒充远端成功。浏览器、真实供应商和生产环境条件继续独立验收。

模块清单验收以 `audit/module-build-profile.json` 为声明源，比较 API/worker 的 profile、契约版本和完整装配集合，并按键检查必需工厂，不固定模块数量或顺序位置。认证设置锁序测试使用有界独立观察连接，业务四连接池仍承受真实锁竞争；测试连接池饥饿修复不能解释为生产锁顺序变更。

独立 PG18 job 还执行 `node scripts/test-service-cancellations.mjs --integration`，在现有 `transactions` 契约下严格验证取消/续费/付款互斥、执行前权益、人工终止保留状态及 schema168→169 历史处理，逐入口要求 run/pass 且零 skip。同一契约的 `node scripts/test-admin-orders.mjs --integration` 验证代下单的统一回执、单次副作用、撤权与客户范围、审计回滚及 schema178→179 历史兼容。仓库共有 183 个 SQL 迁移文件、最大编号 182；文件数不代表业务库已经升级。远端 CI、界面、真实渠道和部署分别记录实际证据，本地验收不替代未执行的线上检查。

本地等价入口如下：

```bash
node scripts/check-doc-governance.mjs
node scripts/check-doc-links.mjs
node scripts/check-contract-gates.mjs
node --test scripts/check-doc-governance.test.mjs scripts/check-doc-links.test.mjs scripts/check-contract-gates.test.mjs
./scripts/check-go-toolchain.sh
./scripts/go.sh test ./... -run '^$'
./scripts/go.sh test ./internal/archtest -count=1
./scripts/go.sh test ./internal/httpapi -run '^TestWriteReportJSONOnlyFormats$'
node scripts/test-compiled-notifications.mjs --unit
node scripts/test-compiled-certification.mjs --unit
node scripts/test-compiled-payments.mjs --unit
node scripts/test-compiled-suppliers.mjs --unit
node scripts/test-scheduler.mjs --unit
node scripts/test-captcha.mjs --unit
node scripts/check-module-binaries.mjs
node --test scripts/module-manifest.test.mjs scripts/compiled-notification-gates.test.mjs scripts/release-runtime-acceptance.test.mjs
npm ci --prefix web
npm --prefix web run lint
npm --prefix web run typecheck
npm --prefix web run test:proxy
node --test scripts/channel-components.test.mjs scripts/notification-center-components.test.mjs scripts/web-push-components.test.mjs scripts/web-push-proxy.test.mjs
node --test scripts/download.test.mjs
node --test scripts/site-theme.test.mjs
node --test scripts/payment-reconciliation-components.test.mjs
node --test scripts/supplier-components.test.mjs
node --test scripts/scheduler-components.test.mjs
node --test scripts/captcha-components.test.mjs
```

配置独立 PG18 测试 URL 后运行 `node scripts/test-compiled-notifications.mjs --integration` 和 `node scripts/test-service-cancellations.mjs --integration`；候选路径与强制启动检查变量见[测试说明](../testing.md#编译渠道与通知运行验收)。全量回归、浏览器夹具、真实外部渠道和候选发布验证仍按[领域验收矩阵](contract-verification-matrix.md)和发布手册单独执行，并将结果写入 `output/` 或外部制品存储。

供应商生命周期迁移 170 同样须与 API/worker 成套发布。不得修改旧迁移 checksum；升级保留旧任务和资金，新增无凭据请求身份保护。宿主能力闭环与本地浏览器不能代替真实供应商执行；默认魔方三类生命周期写能力仍为关闭。具体规则见[供应商接入契约](supplier-module-development.md#生命周期动作)。

自动续费迁移 171 同样须停旧 writer 后成套发布，新增可信来源事实，不按历史 key 回填。供应商与调度严格门禁保护实际到期窗口、付款前意图复核、未付原单不自动追扣和真实 worker 续费强杀恢复；资金入口调整还须执行适用的普通余额/信用/退款回归。真实上游未到期跳过仅证明防止提前扣款，不能替代真实到期自动续费、生产迁移或远端 CI。

供应商无账单到期扫描使用迁移 172，扩展既有不可变生命周期请求封套，不改旧 checksum、不回填历史任务或推断系统暂停来源。旧 writer 停止后配套升级数据库、API 和 worker；新增任务不允许旧 writer 混跑或直接降级旧程序。该分支仍归供应商契约与严格 runner，不新增平行调度或领域门禁，真实上游动作及生产发布另行提供证据。

通知迁移 173 增加旧提醒的不可变精确期限来源，停止来源不明的旧待发、发送中和死信提醒，不从当前资源反推历史。跨日到期补偿、上海日界、发送前失效和真实 schema172 升级归已有通知严格门禁；须停旧 writer 后成套发布，不能将旧验收直接用于新增行为或真实渠道收件。

客户分组定价迁移 180 使用独立不可变行快照和订单出生标记，避免改变外部退款整行证据；同属 `transactions` 门禁，严格 runner `node scripts/test-customer-group-pricing.mjs --integration` 验证定价、退款证据兼容及锁序。先停旧 writer 并成套发布，不能混跑，详见[客户分组定价](../domain/customer-group-pricing.md)。

代下单回执迁移 179 须与对应 API/worker 成套发布，先停止旧 writer。它原样迁移旧请求 hash、结果、时间戳及资源外键；历史关联歧义或统一 scope 冲突会拒绝整次升级，须核实后重试，不覆盖既有结果。成功后旧回执表移除，不能在新 schema 混跑或直接回退旧二进制，详见[交易契约](../transactions.md#管理员代下单与回执)。

## 变更清单

提交前逐项确认：

- [ ] 已更新或确认对应的稳定领域契约。
- [ ] 已增加或调整行为测试，并写明所需数据库、浏览器或外部条件。
- [ ] 已检查权限、幂等、事务、版本冲突、敏感数据和失败恢复。
- [ ] 已运行适用的文档治理、链接、Go、Node、前端和隔离数据库检查。
- [ ] 已在 Review 中区分 E0/E1/E2/E3/E4，未把跳过项写成通过。
- [ ] 已更新运行、发布或回退入口；机器工件写入 `output/` 或外部制品存储。

当前未关闭的替代差距和外部条件集中在[当前差距审计](../replacement-gap-audit.md)及[发布准备契约](../release-readiness-next-contract.md)；状态变化时更新这两个稳定入口，不新建日期快照。

认证迁移 181 绑定 MFA 登录入口，清理无入口证据的未完成挑战。迁移 182 将客户 `users` 与 `administrators` 物理拆分；`account_principals` 仅保存不可变全局 ID/kind，`account_identities` 仅作无秘密的只读资料展示，认证与安全写入选择实际账号表。共享安全/通知记录继续归属全局账号 ID，保留历史 ID、凭据、金额与业务证据，不将旧管理员商务记录转成客户，也不为 bootstrap 或 staff 新建客户购物车。

schema182 发布须先停旧 API、worker 和导入等 writer，再升级数据库并启用配套 API/worker/Web；旧 writer 不得混跑，不能直接降级旧二进制。既有双 Cookie、前台匿名/客户体验与显式代登录契约继续生效，详见[前后台身份边界](../admin-rbac.md#前后台身份边界)。`node scripts/test-captcha.mjs --integration` 是已有认证严格入口，纳入物理分表、跨表标识冲突、独立密码/MFA/API 密钥、bootstrap、共享安全与通知、客户商业边界、显式代登录及真实 schema180→181→182 升级，逐项要求 run/pass 且零 skip。schema182 既有独立库真实 Chrome 验收通过管理员前台匿名、错误入口拒绝、双端独立登录/退出及客户真实下单，并核实管理员未获得客户副本或商务归属。本轮最终代码在隔离 PostgreSQL 18 以 race 模式实际运行严格认证 82 项，全部通过且零跳过；新增覆盖管理员重置客户密码的凭据撤销、锁后撤权与审计回滚，双端换密/MFA 的账号→挑战锁序，以及历史管理员工单升级后的列表、回复和通知兼容。本轮未重跑浏览器、真实通知投递、生产发布与回退；浏览器的既有覆盖边界见 RBAC 契约，前端组件/代理及旧版本证据不能替代未执行项。
