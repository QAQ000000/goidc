# G04 下一批：受控 runtime 依赖与同包启动契约（2026-10-17）

2026-10-17进展：构建器、v2核验及来源绑定已集成，90项联合测试通过，见[集成记录](archive/README.md#f-7842492242e4)。以下保留最初设计基线；其中“未实现”指原记录时点。完整候选构建与同包运行验收仍未执行，G04/G09未关闭。

**状态：设计，未实现、未验收，不计通过；G04 / G09 保持未关闭。** 本文只新增下一批可执行契约，没有修改生产代码、构建器、服务、代理或数据库，没有安装依赖、构建候选、启动进程或执行外部动作。源码审查基线为 `6e4a363a1a8e7fd868b18b51c830a6c4b07fb269`；实现时须重新冻结包含后续修补的完整干净提交，本文基线不是待发布候选。

本批目标是产出带受控运行依赖、可搬移、可核验的完整候选，并在专用 PG18 和独立回环端口上启动同包 migrate / API / worker / Web，验证实际页面、chunk、代理请求及一条无外部副作用的 worker 任务。承接[正式接管契约](release-readiness-next-contract-20261017.md)与[准备首增量](archive/README.md#f-9eea5958b7e3)，通过后只关闭本批准备项。

## 1. 现有实现与本批差异

| 当前事实 | 下一批必须补齐 |
| --- | --- |
| [构建器](../scripts/build-release-candidate.mjs)固定源码构建 api / worker / migrate，封存 `.next`、public、迁移和模板，明确 `runtimeDependenciesIncluded:false` | 同次构建产出的实际运行闭包、受控 Node runtime、启动描述及其摘要；不能仅修改该布尔标记 |
| [Next 配置](../web/next.config.ts)未启用 standalone；构建输入是指向现有共享依赖目录的链接 | 使用下节选定的 standalone 方案，证明 tracing 输入与最终运行文件闭合，不依赖原共享目录 |
| [清单核验](../scripts/release-candidate-lib.mjs)禁止任意 `node_modules` 路径与全部候选软链 | 新版本 runtime 清单与受限路径规则；保持旧 v1 语义，不扩大旧核验器的信任范围 |
| 候选二进制名为 `bin/api`、`bin/worker`；[API](../deploy/systemd/zjmfgo-api.service)与[worker](../deploy/systemd/zjmfgo-worker.service)模板仍指向开发目录的 `bin/zjmfgo-*` | 从发布清单生成一致的启动描述，固定实际二进制名与发布根目录 |
| [Web unit](../deploy/systemd/zjmfgo-web.service)从开发目录 `node_modules` 启动，只有 `.next/cache` 可写；`After=`不等待就绪 | 运行包内 standalone 入口、受控 Node、外置配置、明确可写目录及独立就绪门槛 |
| 首增量封存的是 `de85f5d`、schema153 | 冻结最终提交并重新构建；当前基线已有154，旧候选、清单锚与schema153记录只作为历史证据 |

## 2. 推荐技术方案：同次构建的 Next standalone

选用 **Next `output: 'standalone'`，显式 webpack 构建，封存其实际 tracing 运行闭包**。运行入口使用生成的 `server.js`，不再从开发目录执行 `next start`；不以复制整棵 `node_modules` 或上线时 `npm ci` 补足依赖。Next 配置和脚本变更属于后续实现，本次没有修改。

### 2.1 必须处理的实际 tracing 行为

2026-10-17只读检查旧候选 `/tmp/zjmfgo-sealed-candidate-20261017`：存在148份 `.nft.json`，`next-server.js.nft.json`的22项引用经相对路径指向原 `/www/wwwroot/zjmfgo/web/node_modules`，`server/app/admin-login/page.js.nft.json`还包含 `node_modules` 目录链接。该旧构建的 `required-server-files.json`未设置 standalone。此观察只解释现有产物的依赖形态，不证明当前提交能产生完整 standalone。

Next16.3.8的 `copyTracedFiles`按 trace 所在目录解析文件，再按 `outputFileTracingRoot`计算目标路径，并可能保留源链接；standalone生成过程还可能复制构建期间加载的 `.env`。因此不能直接复制旧 trace 指向的目录、扩大 tracing root 到 `/`，或假定框架输出天然满足本项目的路径与密钥边界。

实现顺序及停止条件：

1. 在干净固定 checkout 内构建；在 checkout 尚存在时读取全部 trace、required-server-files和生成入口，解析实际依赖并建立来源到封存目标的映射。旧候选已移动、原 checkout 已删除，其 trace 相对路径不能从封存目录盲目重新解析。
2. 首选把已有可信锁定安装的依赖作为**受控只读构建输入**，在隔离构建环境内映射到 tracing root 内的真实 `web/node_modules` 路径，避免复制全树；可采用受控只读 bind mount。映射必须局限该构建环境，真实解析路径留在声明根内，不能直接沿用现有向根外逃逸的依赖软链。没有该隔离能力或可信安装来源时先标 BLOCKED，不扩大宿主挂载范围、不修改共享目录；改用独立锁定安装须先满足磁盘预算并在实现评审中明确。
3. `outputFileTracingRoot`只覆盖声明的构建输入根。每个输入必须属于固定源码或已核验依赖；拒绝未映射外部路径、越界目标、缺失文件、链接环与未知类型。trace中的目录/目录链接须解释为受限闭包映射，不能递归复制整个依赖目录作为兜底。无法证明闭包时停止封存。
4. 采用框架生成的 standalone文件集合，按实际应用相对路径补齐 `public` 和 `.next/static`；不得假定 `server.js`总在输出根，必须把真实入口与应用根记入清单。保留必要框架manifest及服务端产物，逐项复核 tracing映射与成品；原生模块、动态加载文件或缺件只能通过明确来源的includes补齐，重新构建验收，不允许启动时在线安装。
5. 构建环境不加载业务 `.env`，成品再次扫描禁止 `.env*`、私钥及受保护配置；不得因为standalone由框架生成就跳过扫描。运行时不依赖 `.ts`配置编译或构建工具自动安装，若仍需某模块，必须纳入已核验闭包。

依赖owner旁的锁文件相同、`npm ls`成功或树摘要前后相同，只证明有限一致性。必须另记录可信锁定安装的来源、锁文件及包完整性校验、Node/npm与平台、输入树摘要和构建窗口不可变控制；未知来源的共享安装不能标为“可信受控依赖”。

### 2.2 Node与系统依赖

推荐包内带受控 Node可执行文件及其许可证，记录版本、SHA256、来源、平台/架构；启动器显式使用它，不依赖PATH碰巧找到同版本Node。只带所需运行文件，不把完整Node工具链、npm缓存或开发依赖一并复制。

Go二进制、Node及原生模块所需的动态加载器/系统库须列为明确宿主基线并核对；不假定Go构建一定全静态，也不承诺跨任意发行版可运行。若宿主基线不满足，应在启动前拒绝。受控宿主基线是外部运行前提，不能被“包内Node”掩盖。

### 2.3 磁盘硬门槛

准备前记录输出文件系统可用字节/inode，测量输入树、tracing闭包、构建中间产物、Node与运行副本的大小，先签本批最大峰值和最低保留余量。估算至少包含：固定源码checkout＋构建临时输出＋standalone所选文件＋封存包＋独立运行副本＋测试PG/WAL与日志；不能只使用最终包大小。

本次只读 `df`显示根文件系统约98%占用、约2.1GiB可用，仅是时点观察，不作为后续空间保证。旧候选约75MiB不包含runtime，也不代表本批峰值。不得复制整棵共享 `node_modules`、在封存后重复留存两份完整 `.next`或为打tar再复制一套包；框架生成standalone期间不可避免的临时重复必须计入峰值。生成后仅封存必要运行集合，证据清单替代重复文件。可用受验证的reflink降低运行副本实际占用，但不得假定文件系统支持或把未来写入成本视为零。

预检不足、预算无法估计或运行中低于保留余量，停止该次构建并报告 BLOCKED；只清理本批独占创建且登记的临时目录。不自动清理其他代理、旧候选、共享依赖、数据库或常驻产物，不借修改运行目录腾空间。

## 3. sealed artifact、mutable runtime与清单v2

建议目录角色如下，具体相对入口以构建实际输出为准，以下不是现有脚本接口：

```text
sealed-candidate/                 # 新建、封存后不再写入
  manifest.json                  # 拟定 zjmfgo-release-candidate-v2
  artifact/bin/{api,worker,migrate}
  artifact/web/                  # standalone实际布局＋public＋static
  artifact/runtime/node
  artifact/migrations/
  launch/                        # 启动描述、模板、配置项契约；无密钥
run-<id>/                        # 与sealed分离的本批独占运行目录
  release/                       # 经核验的运行副本，必要文件只读
  mutable/                       # 明确列举的缓存/临时/状态/日志
controlled-config/               # 外置受控配置，独立权限与配置版本
```

不直接在sealed内预热、启动或创建缓存。运行副本用独立普通文件或经验证的CoW副本，禁止硬链接可变文件，也禁止把整个 `.next`链接回sealed。启动前核对副本不可变文件与清单，运行后再次核对sealed全树摘要；可写路径应从实际写入审计得出，除 `.next/cache`外若框架还需写route/ISR等文件，必须逐项说明，不能凭旧unit假定仅cache可写，也不能直接放开整个release根。

清单v2至少包含以下数据：

| 数据组 | 强制字段与规则 |
| --- | --- |
| 可信来源 | 完整commit/tree、源码文件摘要、构建器/配置/启动器摘要、命令、工具链、平台与架构；继续要求干净固定源码和Go `vcs.modified=false` |
| 依赖 | 可信安装来源记录、输入锁/树摘要、tracing文件摘要及映射、实际选入依赖清单、Node与原生模块/宿主基线；完整性不能由单一布尔字段表达 |
| artifact | 每个普通文件相对路径、字节数、SHA256、可执行位；三个Go二进制实际build info；Web BUILD_ID、入口、应用根、实际rewrite、static/public与全部必要框架文件 |
| 迁移 | 最终候选完整迁移列表及checksum、该候选要求的schema；不固定抄用153或沿用历史清单 |
| 启动 | 包内二进制与Web入口、工作目录、受控Node路径、监听地址、配置版本、超时、允许可写目录、运行副本校验规则 |
| 链接 | 默认拒绝；确需保留的runtime内部相对链接单独列target及解析结果，只能落在完整封存树内；拒绝绝对链接、越界、环、悬空及特殊文件 |
| 可信锚 | 操作者从构建结果另行保存manifest SHA256；保留可信源码比对。目录内清单及其自带SHA不能自行证明可信 |

v1核验继续只接受其原有不含runtime的格式；v2须明确处理依赖路径白名单，不能全局删除 `protectedPath`保护。源码、配置、密钥规则不因允许某个runtime内 `node_modules`子树而放松。核验范围不得有可被任意新增文件绕过的忽略目录；mutable根放在sealed之外。

## 4. 外置受控配置与启动一致性

拟新增启动描述/启动器及模板生成逻辑，全部由同一候选清单驱动；实现后再确定CLI名称，不在本设计中把计划命令写成已存在命令。至少声明：

- artifact根与manifest可信锚、`bin/api` / `bin/worker` / `bin/migrate`实际路径，Web实际入口、包内Node、cwd、普通运行用户和宿主基线；不引用开发目录或全局 `node_modules`。
- 外置配置的受控版本与必需项存在性，不把DSN、Cookie、支付/供应商凭据、加密密钥值放进包、manifest、参数日志或报告。权限不足/配置缺失/非预期配置版本直接拒绝；敏感内容仅由受控文件或进程环境注入。
- 清理继承的 `NODE_PATH`、非允许 `NODE_OPTIONS`及项目 `.env`等隐式来源，只注入明确白名单；Web `NODE_ENV=production`，监听回环；API origin、Web origin、Cookie及来源配置彼此一致。
- 构建时 `API_INTERNAL_URL`须与实际 `routes-manifest.json`、standalone生效配置及启动描述一致。运行时改环境变量不能被当作改写既有rewrite的证明。临时端口候选只作为该隔离目标候选，不能直接晋升正式目标。
- 启动前完整核验artifact/runtime/宿主配置，先执行同包migrate并检查结果，再启动API；有界等待API ready后启动Web/worker并各自验收。失败只停止本批进程，不启动依赖服务“凑齐”结果，不改变已有服务。

`After=`只提供顺序；[API readyz](../internal/httpapi/server.go)目前只Ping数据库，API/worker启动时的 `CheckSchema`也不证明所有业务健康。启动结果必须结合实际实例、schema与worker任务验证。生成systemd模板可作为交付物，本批隔离验收不安装或启用宿主常驻unit；用可控子进程执行相同启动描述。

## 5. 隔离同包启动验收顺序

下表全部为下一批待实现/执行。执行前重新检查路径、端口与数据库身份，所有结果绑定最终候选及独立保存的manifest SHA。

| 步骤 | 操作 | 通过门槛与停止条件 |
| --- | --- | --- |
| A. 固定候选 | 冻结包含最终修补的提交；登记业务范围、工具链、依赖来源、磁盘预算和受控配置版本；在独立构建输入中构建standalone与三Go产物 | 同源、无dirty、无原共享目录运行依赖；缺可信输入或磁盘预算则BLOCKED |
| B. 搬移与核验 | 把封存包搬到新根并建立独立运行副本；运行环境不含源码checkout、共享 `node_modules`、npm/Go构建工具链或隐式模块搜索路径；只保留声明的宿主基线 | v2核验通过；不得用源码目录/原共享依赖补缺；不在sealed中启动 |
| C. PG18与端口 | 使用本批拥有的专用loopback PG18 `_test`库及独立随机schema，确认服务端版本、库名、schema和迁移目标；分配未占用的API/Web回环端口，显式登记 | 子进程DSN必须带隔离search_path；不能仅给测试框架隔离而让worker落到默认schema；避开8088/3088及其他代理端口，不自动终止占用者 |
| D. 同包迁移 | 用包内migrate执行首次迁移、再执行一次，并检查本候选完整迁移checksum及schema | 两次成功且第二次无结构/业务变化；迁移失败不启动三服务；只证明新测试库同候选，不声称业务快照升级恢复 |
| E. API与Web | 从包内入口启动API，检查ready；随后启动Web，从实际页面响应提取同源JS/CSS引用，获取其真实字节；通过Web访问一条真实API只读请求 | API/Web均回环；页面和实际引用chunk为200且字节摘要匹配本候选文件；至少覆盖首页、客户登录、管理登录的不同页面入口及所有这些页面实际引用资源；不是只HEAD固定伪造路径；API直连与Web代理结果一致 |
| F. worker业务 | 按下一节构造安全任务，只启动同包worker，等待实际claim/执行，随后TERM、等待退出，再启动一次验证稳定性 | 对应任务succeeded、结果与审计正确；重启不重复任务/账本/审计；无外部业务调用。不能用started日志或API ready替代 |
| G. 实例与封存核对 | 记录本批PID、exe/cwd、实际Node/Go可执行摘要、Go build info与监听；绑定Web入口、BUILD_ID、HTTP资源摘要；运行后复核sealed | 三个常驻实例和已退出migrate均来自同包；sealed不变；副本变化仅限允许写路径；不输出进程environ或完整配置 |
| H. 结束与清理 | TERM本批PID并有界等待、核对退出与监听消失；检查数据库不变量与连接；只清理本批登记的副本/临时文件及自建测试目标 | 不使用全局kill、模糊目录删除或清理共享PG；本批无残留进程/监听/连接，记录schema处置；封存包与去敏证据按约定保留 |

就绪等待、任务完成和退出均须有显式超时，可先使用30秒/30秒/15秒作为小夹具默认并在报告中记录实际值；这些只是测试终止边界，不是上线SLA或容量承诺。遇到超时、缺模块、chunk404、rewrite错误、混版、越界写或外部调用立即FAIL；缺独立库/端口/依赖来源则BLOCKED，不能改用业务目标绕过。

### 5.1 无外部副作用的worker夹具

选用现有 [ProcessProvisionJob本地simulation分支](../internal/store/services.go)的一条 **resume**任务：在该测试schema内准备已审核的simulation服务，初始suspended、对应合法pending resume任务；金额、服务归属、版本和现有触发器均须满足。可复用现有测试构造器，但不得删除约束或伪造“执行成功”字段；先证明夹具已成功建立，再启动真实包内worker。

启动前逐项证明该隔离库无启用的支付查询渠道、供应商/NOKVM账户、邮件/SMS投递渠道及外部写任务；关闭mock支付入口与其他无关自动任务的触发条件，服务不自动续费，没有到期或推广结算待办。配置不携带真实商户、资源或收件信息；构造器不得借生产数据或复制真实配置。测试进程限制非回环网络出口，并记录限制方式与适用PID；仅把URL配置成loopback不足以证明没有旁路请求。

期望记录：任务由worker从pending进入running再succeeded，结果mode为simulation，服务恢复active，`service.simulation.completed`审计与该job唯一对应；资金、余额、信用、原付款及无关任务保持基线，无新通知待投递。随后TERM并重启同包worker，任务次数、服务版本与业务审计不重复增加。保留任务/服务ID、前后状态和去敏摘要；数据库内构造与状态改变只发生在本批专用测试目标。

如最终源码改变simulation行为，须先按源码修订夹具及不变量，不能以构造失败、任务失败或关闭worker消费冒充成功。该夹具只证明同包worker正常消费与干净重启，不覆盖真实供应商交付、通知送达、远端未知结果、强杀崩溃点、积压排空或负载。

## 6. 实现交付物、负向验收与报告

后续实现批次至少交付：固定源码standalone/runtime构建及v2核验、启动描述与受控配置契约、只操作专用目标的opt-in验收入口、有针对性的负向测试、绑定候选的去敏结果记录。旧v1构建/核验接口与保护测试应保持可解释兼容，文档同步说明v1不是运行包。

| 负向情形 | 必须观察的结果 |
| --- | --- |
| Node/选入依赖缺失或篡改、native模块不匹配、锁或可信锚错误 | 启动前核验拒绝；不得自动下载或回退到宿主模块 |
| runtime外逃/绝对/悬空/循环链接、trace越界、保护文件混入 | 构建或核验明确拒绝，不写出声明根 |
| sealed新增文件/字节/可执行位变化，运行副本静态文件变化 | 完整清单核验拒绝；不能因增加mutable规则而忽略sealed变化 |
| 非预期配置版本、缺必需项、rewrite/端口与构建不一致 | 启动前拒绝；不泄露配置值；不改变共享服务 |
| 非PG18、非专用测试库/schema、占用端口、迁移失败 | 不开始业务进程或停止本批已启动进程，保持其他目标原状 |
| 依赖目录/运行副本磁盘峰值超预算 | 构建前BLOCKED或有界失败；只清理本批独占临时产物 |
| 安全worker夹具失败、外部调用、chunk404、就绪/退出超时 | FAIL并保留去敏证据；不能以进程退出0或页面HTML200改判通过 |

验收报告逐项标记 `PASS / FAIL / N/A / BLOCKED / 未执行`，至少保存：候选commit/tree、manifest可信锚、依赖安装来源与闭包文件数/字节数、实际磁盘峰值及余量、Node/Go与运行实例绑定、PG18/schema/迁移摘要、配置版本、页面与chunk请求结果/摘要、worker不变量、退出及清理证明、失败历史与重跑边界。日志、截图或摘要不得含密码、DSN、Cookie、令牌或真实凭据。只运行必要差异与负向检查，不把默认跳过的opt-in项写为通过。

## 7. 本批明确不关闭的条件

即使本契约全部通过，也不等于正式上线。真实域名HTTPS/代理/端口暴露、维护模式及公网回调、旧页面chunk保留与真实切流/切回、授权业务快照升级恢复、真实支付/退款到账/供应商/通知证据、目标容量与RTO/RPO、执行中强杀及未知外部结果恢复，继续由[正式接管契约](release-readiness-next-contract-20261017.md)单独验收。三服务同包隔离启动不能替代这些项目，G04/G09不因此自动关闭。
