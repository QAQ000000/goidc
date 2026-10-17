# 完整双 Web 隔离切换准备验收（2026-10-17）

状态：**run6 完整双 Web 隔离切换准备 PASS；run1–5 原 FAIL 保留，G04/G09 总体仍开放。** run6 从修复后的干净 `6b95102` 正常构建双包并实际运行，关闭隔离双 Web、统一公共 Origin、SSR/API 会话与 upstream 切换/切回准备子项。本批按[双 Web 契约](release-dual-web-contract-20261017.md)执行，没有部署、推送、重启常驻服务、操作业务库或真实资金/供应商资源；本文整理只读报告并提交文档。105 页台账仍为 45 完成 / 60 部分。

## run1–5 使用的固定候选

两包均从独立干净源码 `16cc54ee52f8039a36265302d13191caeedb1457` 顺序正常构建，包含 API/worker/migrate、Next standalone、受控 Node 和完整迁移。不是把旧封存包临时修改环境后重用。下表只对应 run1–5，后续修复候选另列。

| 项目 | A | B |
| --- | --- | --- |
| BUILD_ID | `maNFvKsuvVTpJSYhoEAXo` | `9yoVtH5AluIgtPNYW7YuS` |
| manifest SHA256 | `e238e03e701bb08cbfa3ba2c88f0d508d192bd771c03a2bb6ef688c25e78c0a9` | `110359e25cfffdcae777f330ed91870ea8ae6a8aaf4518040c6d914bd266d772` |
| 产物文件数 | 3059 | 3059 |
| 内部 API/Web 端口 | 58088 / 53088 | 58089 / 53089 |
| 公共 Origin | `http://127.0.0.1:53188` | `http://127.0.0.1:53188` |

两包各 220 个静态文件，57 个共享 URL 同字节，并集 383 个；这份构建清单不证明公共入口页面资源和切换成功。可信依赖 provenance SHA256 `76fd9fca5468507a9eee8741bb701ad18a7a3869589801b84fb8729e8799ecd1`，受控 Node SHA256 `b2959781cc5a74c357ffa02367efa8a0330cbb1c9cb347732fdfaaaca381cbcd`。两包外锚、观察样点和历史日志摘要见[输入证据索引](evidence/release-dual-web-20261017/input-evidence-index.json)、[A 外锚](evidence/release-dual-web-20261017/a-build-anchor.json)及[B 外锚](evidence/release-dual-web-20261017/b-build-anchor.json)。

A/B 构建均退出 0；Go 三个二进制构建、Next webpack 和内置 TypeScript 检查通过。两次均有 175 个外部空间/内存样点，根盘观察最低分别 869/751 MiB；这是采样观察，不承诺瞬时峰值。构建所用只读 module cache 出现主模块 stat cache 写入被拒提示，构建仍成功，缓存未改为可写。首次 A 构建预检曾误传 npm 工具 provenance，校正为依赖安装 provenance 后才正常构建；首次拒绝日志保留，未计成功。

## 五轮失败与实际推进范围

[证据复核表](evidence/release-dual-web-20261017/run1-5-evidence-verification.json)记录逐轮 runner、原报告 SHA256/字节数、检查条目数和退出证明。本次文档整理重新计算五份本机原报告摘要，均与原摘要一致；未修改原报告或把 FAIL 改为 PASS。

| 轮次 / runner | 停止边界及已证明范围 | 失败结论与后续处理 |
| --- | --- | --- |
| [run1](evidence/release-dual-web-20261017/run1-summary.json) / `43c3085` | 沙箱入口失败，没有内层 isolation、迁移、HTTP 或切换证据。 | 后续安全 no-op 复现空 `Groups` 被跨行解析；`4e2434b` 限制横向空白并保留 wrapper 安全阶段诊断。修复不追认 run1 通过。 |
| [run2](evidence/release-dual-web-20261017/run2-summary.json) / `4e2434b` | 已进入内层，报告停于 `old-web-ready`；旧版失败报告未保存细分 frame/checks，不能据其宣称页面和库映射验证通过。 | 保留原 FAIL。`002879d` 增加白名单 phase/frame、检查和原生加载摘要，后续轮次才有更精确诊断；不以新轮结果补写 run2 原因。 |
| [run3](evidence/release-dual-web-20261017/run3-summary.json) / `002879d` | 四次 migrate 均退出 0、同轮 schema 摘要一致；已有 A 的三页及资源/动态缓存检查，共 32 条检查记录，停于 `old-web-loaded-maps`。 | 实际库映射的允许路径核验拒绝；报告保留安全 frame，未记录具体 mapping。`3b5a9c6` 增加规范路径及安全 mapping 诊断；不声称 run3 已证明双 Web 或切换。 |
| [run4](evidence/release-dual-web-20261017/run4-summary.json) / `3b5a9c6` | 同样完成四次 migrate 及 A 阶段 32 条检查，仍停于 `old-web-loaded-maps`；安全诊断为 `class=unknown, basename=libc.so.6`。 | `bfe9f5d` 仅在基线已锚定 basename 且实际文件摘要匹配时接纳 host/tooling 别名；未放宽为按文件名放行。run4 原 FAIL 保留。 |
| [run5](evidence/release-dual-web-20261017/run5-summary.json) / `bfe9f5d` | 两代四次 migrate、4 个运行身份和5组实际库映射有记录；推进到公共入口 A 的登录/API/SSR，共 72 条检查。尚无 HUP/reload 或持续观察记录。 | 客户会话请求 `/admin` 得到 HTTP 200，契约要求重定向 `/`，因此本轮 FAIL；后台 API 同时保持 403。主线 `dbe347f` 已补后台 SSR 入口保护，需新构建和完整重跑后才能关闭。 |

run5 的关键记录为：客户 `/api/v1/admin/site-settings` 返回 403、客户 `/console/contracts` 返回 200、管理员后台 API 和 `/admin` 返回 200，随后客户 `/admin` 返回 200 触发失败。该失败是 **HTTP 层重定向契约不符**；没有证据证明客户读取后台受保护数据，不能称为权限泄漏。Next 流式渲染中的 redirect 不能直接等同 HTTP 3xx，后续修复在渲染前执行入口判断；原 API 权限保护保留。

五轮均没有切到 B 或执行切回；原报告的 reload/observation 数均为 0。因此不得据其中局部页面、登录或库加载成功，宣称切换、排空、跨代会话连续性及注销反例已验收。

## run1–5 清理与隔离证据的边界

所有原报告均记录外层 `sandboxStop.groupGone=true, forced=false`、`fixtureStop.groupGone=true, forced=false`，fixture 最终 `phase=cleaned, schemaRemoved=true`；双封存与独立副本字节、工具、PG 存储/进程身份和常驻宿主身份保持不变。run2–5 另有 `privatePIDNamespaceGone=true`；run1 在内层证据形成前失败，该字段为 false，不能追认为已取得内层 namespace 退出证明。

run2–5 的内层失败结果仍保留 `allOwnedExited=true`、`listenersGone=false`、`fixtureCleaned=false`；它们不能改写成成功运行的退出证据。外层之后的正常组退出、namespace 消失和 fixture abort/schema 删除单独证明失败清理完成，不使本轮整体通过。本次只复核这些报告字段和摘要，未重跑退出探针或连接数据库。

## 历史测试与中间候选

历史最终控制器/场景集成的 209 项 Node 联合回归及 fixture PG18 四项 race 通过，原日志摘要保留在输入索引；这些结果属于当时提交。后续主线实际执行 `node --test scripts/release-*.test.mjs` 为 214/214，通过的双 Web 定向控制器/场景测试为 37/37，均退出 0；该次输出由工具会话返回，未落日志，本文不编造日志 SHA256，也不据此声称重新执行 Go 全量、前端全量或双 Web 实际运行。

中间源码 `dbe347fb0e43542276dfc0e7ef7081ebf432ad13` 的 A v2 曾正常构建封装：BUILD_ID `bHtkK1VY-1TMB1w3Rnht0`、3059 个产物文件、manifest SHA256 `b7322623106c115ae33e4a776fc07a48d27e2d928e92e9da8c5e9d78da453c46`；它没有运行验收，现已按授权清理，不套用旧 A/B 或 run6 报告。后台入口修复 `dbe347f` 与 console 入口修复 `511034d` 均包含在最终 `6b95102`，run6 使用下文 v3 两包独立验证。

## run6 最终候选与构建

源码及 runner 均固定为干净 `6b951025eb5ad9887b0c9a46dc18dbaaef5f6be3`。两包顺序正常构建，每包均含 3059 个产物；使用同一受控 Node/依赖、相同迁移及独立启动声明。公共 Origin 仍为私有 `http://127.0.0.1:53188`，A 内部 API/Web 为 58088/53088，B 为 58089/53089；这不是公网候选。

| 项目 | A v3 | B v3 |
| --- | --- | --- |
| BUILD_ID | `gVh6-Rk5BjGrBgTCjeQ7r` | `HxIiLKYNudHdPZVX0nl9m` |
| manifest SHA256 | `73cb22adf7d7b8590c07c5b3700a54a8becdbdce26a8a63703968d1c2ebbdf46` | `1b21bcfb71b46a282cb95c4cfe3fe7287f83040862bc45c03da5f2507554a63f` |
| 封存位置 | `/dev/shm/zjmfgo-runtime-dualA-20261017-v3` | `/tmp/zjmfgo-dual-build-rootfs-20261017-v3/zjmfgo-runtime-dualB-20261017-v3` |
| 独立外锚投影 | [A v3](evidence/release-dual-web-20261017/a-v3-build-anchor.json) | [B v3](evidence/release-dual-web-20261017/b-v3-build-anchor.json) |

文档复核再次计算两份 manifest 和各 3059 个产物的实际字节 SHA256，与独立外锚、源码及 run6 报告相同。静态集为 220 + 220、共享 58、并集 382，和 run1–5 的并集 383 不混用。

[构建与测试索引](evidence/release-dual-web-20261017/run6-build-and-test-index.json)保留原路径、日志与资源观察 SHA256。A2、B2 构建均退出 0，外部采样分别为 196/186 点，根盘最低可用 3464790016/3513692160 字节；这些是观察值，不是未采样峰值保证。B1 在记录峰值及保留空间预检被拒（退出 1），原日志 SHA256 `adefa0b2d3dee5795c1534fbc7306f015d574b358daaae94007641fb5ea782f8` 保留为 FAIL；后续 B2 使用独立 rootfs 构建，不覆盖该失败。

最终 Node 回归日志 `/tmp/zjmfgo-dual-final-regression-20261017-v3.log` 为 221/221、0 失败/跳过，SHA256 `91ada81497b9d6325b388803e3bf83d6a102f26b6b3fbde772f76f0961dd573c`；日志内容和摘要在本次只读复核中一致。主代理还执行 Go vet（退出0、空日志）、Web typecheck（退出0）、lint（退出0、0错误/7条既有warning）及隔离PG18四项 opt-in fixture race（退出0）；vet/lint/race日志摘要见同一索引，typecheck仅有工具会话8953输出，不编造日志摘要。本批没有重跑全量Go数据库回归或四尺寸浏览器，页面布局没有变更；本次文档整理只读复核，不重新运行代码测试或构建。

## run6 实际运行与清理

原报告 `/tmp/zjmfgo-dual-web-run6-20261017/report.json` 为 **PASS**，1495485 字节，SHA256 `e636bd14952582e5b744b5b36f72d8307059081d220e89bd3dafe5e7153793c3`。[白名单摘要投影](evidence/release-dual-web-20261017/run6-summary.json)绑定 runner/源码、两个候选锚、plan/config 摘要、退出和检查数组摘要。原始响应头、正文、cookie、凭据及夹具配置均未复制入 Git。本次另重算99份trace及114份mutable证据文件的长度/SHA256，与报告索引逐项一致；原报告字节保持不变。

| 检查 | run6 证据与实际范围 |
| --- | --- |
| 同包迁移及预热 | A/B 各 migrate 两次均退出 0，四份 schema 摘要相同；两代内部真实页面、资源、site 代理和 SSR 标记通过，4 个运行身份及8组库映射记录在原报告。 |
| A→B→A | 真实目标 Nginx 在私有 namespace 内两次 HUP；实际 API/Web upstream 分别为 58088/53088→58089/53089→58088/53088。页面/身份断言与累计 Web→API syscall 连接记录作为组合证据；后者也可能来自 proxy/site/readiness，不能单独归因某一次受保护 SSR。 |
| 旧/新静态资源 | 三阶段均复核 382 个并集资源，共 1146 条 `retained-union` 检查；真实页面有 A/B 独有资源引用，切回继续保留 B 资源同字节。 |
| 会话与权限 | 客户与管理员各真实登录一次，三阶段同会话读取 API 与 SSR；客户后台 API 均 403、客户 `/admin` 均 307，合法客户/管理员 SSR 200。错误 Origin/请求标识写入被拒，匿名/无效会话、转发头及注销后双 API/Web 反例通过。fixture 三次连续性检查均同身份、业务摘要不变，最后活动会话 0。 |
| 有界在途排空 | 两次 HUP 后旧 worker 都在原静态请求完成后自然退出；原请求分别 4011/4005ms、同一 72632 字节资源摘要一致。master 不变，新代接管时旧请求仍在途；这是受控静态传输排空，不是业务任务队列排空或容量证明。 |
| 持续观察 | 450 次离散探针、错误 0，generation 为 old45/next236/restored169；覆盖切换和切回及三代配置响应，观察在注销前停止。这不代表所有请求零停机。包括旧/新独有 chunk、公共 HTML、客户/管理员只读身份请求。 |
| 审计 | 共 1449 条检查记录（其中8条为原生/配置/连接元数据，不等同1449次HTTP请求），99 份 syscall trace；未声明写入尝试及非回环网络尝试均 0。预期 3xx/400/403/404/405 等按契约单列，不把全部非200视为意外失败。 |
| 清理和不变量 | 内层 `ok/allOwnedExited/listenersGone/fixtureCleaned/fixtureSchemaRemoved` 全 true；外层两组退出且无强杀、private PID namespace 消失、fixture schema 删除。双封存/副本、工具、PG 存储身份和常驻宿主身份前后不变。 |

`authenticatedSSR` 是受控 HTTP 状态200及固定title等最小断言，不证明所有受保护业务组件完整渲染。原报告只保存响应摘要、不保存正文或原始headers，因此title、cache和Origin等断言依赖固定源码的执行记录及报告，不能由摘要独立重建；连接trace只作为组合证据。本次PASS限于该契约及实际观察，不扩大为所有浏览器页面、每次SSR请求归因或生产零停机证明。

报告记录运行预算峰值 2GiB、组件和 2046820352 字节，250ms 预算监控独立于采样；观察到 Go 临时/缓存 319614790 字节，不能把本次小夹具时间或内存推导为生产容量。本次文档复核只读报告、日志、锚和实际封存字节，没有操作进程或连接数据库。

## 已授权历史候选清理与证据留存

用户授权的历史产物清理已删除旧 candidate2/3、旧 dualA/B 和未运行的 A v2；[清理摘要](evidence/release-dual-web-20261017/historical-candidate-cleanup-summary.json)绑定既有清理报告 SHA256、旧 manifest/启动描述的本机保留位置和摘要，常驻服务身份保持不变。本次文档复核确认这些旧候选路径已不存在，留存 manifest 字节摘要与原记录一致；没有再次删除任何文件。

旧 manifest 保存在 `/tmp/zjmfgo-cleanup-evidence-20261017`，run1–5 原始失败报告仍保留在各原运行目录，Git 里的历史失败投影不变。旧文档中的候选目录已成为历史位置，不能据路径继续宣称完整包当前存在。run6 最终两包、原报告和本机证据仍不是永久离机归档。

## 验收边界

本批已通过相同源码、同迁移集合、共同隔离 schema 的双 Web 部署代际准备；通过范围不包括历史源码/schema 升级、公网 HTTPS/安全 Cookie、正式静态保留与清理、生产切流、目标容量、积压排空或业务 worker 执行中强杀恢复。G04/G09 的这些缺口保持开放。未用 NOKVM 不阻断当前魔方商品范围。

本机原始证据在 `/tmp` 与 `/dev/shm`，可能随清理/重启丢失；Git 只保留脱敏摘要与外锚，尚非永久离机归档。本次提交排除凭据、cookie、响应正文、依赖、构建产物和业务数据；保存的响应证据仅有固定路径、状态、时间、长度与摘要。
