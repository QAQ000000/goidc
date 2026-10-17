# 完整双 Web 隔离切换准备验收（2026-10-17）

状态：**run1–5 全部 FAIL，完整双 Web 切换准备尚未通过；G04/G09 保持开放。** 本文记录固定候选的五次失败及其脱敏证据，不将后续修复或新候选封装计为运行通过。本批按[双 Web 契约](release-dual-web-contract-20261017.md)执行，没有部署、推送、重启常驻服务、操作业务库或真实资金/供应商资源；本文整理只读报告并提交文档。105 页台账仍为 45 完成 / 60 部分。

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

## 清理与隔离证据的边界

所有原报告均记录外层 `sandboxStop.groupGone=true, forced=false`、`fixtureStop.groupGone=true, forced=false`，fixture 最终 `phase=cleaned, schemaRemoved=true`；双封存与独立副本字节、工具、PG 存储/进程身份和常驻宿主身份保持不变。run2–5 另有 `privatePIDNamespaceGone=true`；run1 在内层证据形成前失败，该字段为 false，不能追认为已取得内层 namespace 退出证明。

run2–5 的内层失败结果仍保留 `allOwnedExited=true`、`listenersGone=false`、`fixtureCleaned=false`；它们不能改写成成功运行的退出证据。外层之后的正常组退出、namespace 消失和 fixture abort/schema 删除单独证明失败清理完成，不使本轮整体通过。本次只复核这些报告字段和摘要，未重跑退出探针或连接数据库。

## 测试记录与后续修复候选

历史最终控制器/场景集成的 209 项 Node 联合回归及 fixture PG18 四项 race 通过，原日志摘要保留在输入索引；这些结果属于当时提交。后续主线实际执行 `node --test scripts/release-*.test.mjs` 为 214/214，通过的双 Web 定向控制器/场景测试为 37/37，均退出 0；该次输出由工具会话返回，未落日志，本文不编造日志 SHA256，也不据此声称重新执行 Go 全量、前端全量或双 Web 实际运行。

主线 `dbe347fb0e43542276dfc0e7ef7081ebf432ad13` 的 A v2 已正常构建封装：BUILD_ID `bHtkK1VY-1TMB1w3Rnht0`、3059 个产物文件、manifest SHA256 `b7322623106c115ae33e4a776fc07a48d27e2d928e92e9da8c5e9d78da453c46`。本次只读核对该 manifest 的实际摘要、源码与 BUILD_ID 一致；**A v2 尚未运行验收**，不能套用旧 A/B 的原报告。console 入口补丁仍在另一独立分支进行；后续须先冻结最终修复源码，再按契约正常构建合法双候选并完整重跑。

## 验收边界

这是一批相同源码、同迁移集合、共同隔离 schema 的部署代际准备；即使后续通过，也不证明历史源码/schema 升级、公网 HTTPS/安全 Cookie、正式静态保留与清理、生产切流、目标容量、积压排空或业务 worker 执行中强杀恢复。G04/G09 的这些缺口保持开放。未用 NOKVM 不阻断当前魔方商品范围。

本机原始证据在 `/tmp` 与 `/dev/shm`，可能随清理/重启丢失；Git 只保留脱敏摘要与外锚，尚非永久离机归档。本次提交排除凭据、cookie、响应正文、依赖、构建产物和业务数据；保存的响应证据仅有固定路径、状态、时间、长度与摘要。
