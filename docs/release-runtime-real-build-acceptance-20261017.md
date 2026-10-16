# 受控运行包真实构建与隔离验收（2026-10-17）

状态：本批受控构建与同包隔离运行验收 PASS。最终 candidate3 真实封存、搬移核验、双迁移、API/Web/worker 启停重启及退出清理均通过；首次拒绝和 run3 审计误报保留如下。G04/G09 仍开放，本批不部署或推送，不重启常驻服务，不写业务数据库。本记录延续 [运行包契约](release-runtime-next-contract-20261017.md)。

## 已冻结输入

- 初次构建源码：`7013c61df6d55a295cc969ad42063c01859759e3`，tree `caa1b0f8a7bf0e41b4586e4291e1e53a82ae9b8b`。后续修复须重新冻结、构建；初次结果不得套用到新候选。
- 官方 Node `v24.16.0` / npm `11.13.0`，归档 `https://nodejs.org/dist/v24.16.0/node-v24.16.0-linux-x64.tar.xz` 经同目录官方 HTTPS `SHASUMS256.txt` 核对；归档 SHA256 `d804845d34eddc21dc1092b519d643ef40b1f58ec5dec5c22b1f4bd8fabde6c9`。记录证明本次下载与官方 HTTPS 摘要一致，不宣称另有离线签名验证。
- Node 二进制 SHA256 `b2959781cc5a74c357ffa02367efa8a0330cbb1c9cb347732fdfaaaca381cbcd`，许可证 SHA256 `4573185d56580da2b890ba34a85a409257640f1c5632eade4300137266194d18`。外置 Node 来源 JSON SHA256 `d5b3ecb606d8b99a46369560e91fbe5b6e992c0c06f67dde5b65db3228db3526`。
- npm 完整 1874 文件来源 JSON SHA256 `dce20b3d3c3b42d1d6943d193bd7891a5400e05e25d942752cca7f997fc979e7`，输入树 SHA256 `9b477fbf393ebe7d846fc3e1fecf4fee1ce4b5a62cb291c92ef285047bcf69c1`。
- 在独占目录执行真实 `npm ci --include=dev --ignore-scripts --no-audit --no-fund`，553 个 lock 条目全部为官方 HTTPS tarball + SHA512；安装 474 个包。禁生命周期脚本、继承代理/令牌/Node 选项、独占配置与缓存，成功后删除本批缓存，依赖去写权限，后续构建以私有只读 bind 输入。
- 真实依赖树 32749 文件、普通文件合计 484131223 字节，输入树 SHA256 `98adfd448eb575e1bda90f0f14c0b2a041197aaa5e8ef66afc220f037b543efd`；外置证明 JSON SHA256 `76fd9fca5468507a9eee8741bb701ad18a7a3869589801b84fb8729e8799ecd1`。来源为本次可信安装，不是已有共享依赖的自述。

以上工具及安装证据保存在本机 `/tmp/zjmfgo-node-provenance-20261017.json`、`/tmp/zjmfgo-npm-provenance-20261017.json`、`/tmp/zjmfgo-runtime-trusted-dependencies-20261017/`；不提交二进制、依赖树或凭据。

## 首次执行与失败边界

1. 直接输出 `/dev/shm/...` 被既有 v1 helper 的 `/tmp` 输出白名单拒绝，尚未编译。日志 `/tmp/zjmfgo-runtime-build-execute-20261017.log`。
2. 在外层独占私有 mount namespace 中将 `/dev/shm` 映射到 `/tmp/zjmfgo-runtime-memory-20261017`，输出使用 `/tmp` 路径；宿主挂载未变化，未修改 helper 白名单。内层仍独立挂载只读依赖。三个 Go 二进制成功且 build info 绑定干净源码；Next `16.3.8` webpack 编译、TypeScript 和页面构建完成。
3. 封存检查拒绝 standalone 中缺失的 `next/dist/compiled/next-server/server.runtime.prod.js`。源码核对发现 collector 遍历所有 nft，而框架 standalone 不使用 `next-minimal-server.js.nft.json`。失败候选由构建器仅清理本次独占输出；未伪造 manifest 或跳过依赖验证。
4. 独立审查同时发现 Go 临时目录仍落磁盘 `/tmp`，原构建器仅监控候选 tmpfs，故本次不能证明所有编译临时文件均纳入预算。`717791b` 已将 Go/Node 临时目录定位到受监控候选 `.scratch/tmp`，下一次完整构建必须使用新冻结源码。

第二次尝试日志 SHA256 `8d9ea944ceec57a879cdcbb93551f8d209e2a563a4041b2c88bcbf515ba31416`，资源采样 SHA256 `3c805e4b521157097d7aed23905bad311d379a808bc915ea2d36ee6a12f22756`。43 个 2 秒时点采样：磁盘最低可用 1662517248 字节、tmpfs 最低可用 3442110464 字节、MemAvailable 最低 4390272 KiB。采样是观测，不等于配额或未采样瞬间保证。执行记录 `/tmp/zjmfgo-runtime-build-attempts-20261017.json`。

## 当前检查

| 检查 | 结果与边界 |
| --- | --- |
| Go vet | PASS，`/tmp/zjmfgo-runtime-final-vet-20261017.log`，退出 0 |
| 临时目录集成与既有 builder/verifier 保护 | PASS 91 项；依赖 fixture 的两项在共享磁盘被隔离 Go 全量写入干扰后，独占 32MiB 私有 tmpfs 中全部 10 项 PASS。保留首轮失败，不将其改写为全通过 |
| 强制隔离 PG18 全量 | PASS，退出 0；HTTP 1777.307 秒、store 192.263 秒；Go/迁移和候选一致；日志 SHA256 `d1e05ed249747cf81c04ac7dde9ed90f8a3dd27d45426789d3f6a7d9e082dcdb` |
| 修复后的完整候选与搬移核验 | PASS；最终干净 `949ffe3`，3058 artifact 文件，独立外锚及运行后全树核验一致；`5118eac` 是旧候选 |
| 同包双 migrate / API / Web 页面与 chunks / worker TERM 重启 | PASS；candidate3 + runner `ce19107` 的 run4，三页面200、21份实际资源与封存字节一致 |
| 写边界、无外网、退出、schema 清理 | PASS；独立namespace、真实uid/gid1000，66份trace重审，无未声明文件写入或非回环网络尝试，进程/监听/schema清理通过 |
| 前端检查 | typecheck、lint 退出0（7条既有warning）；最终真实webpack构建完成并封存 |

独立空库 `zjmfgo_release_runtime_20261017_test` 创建于本批 PG18 测试实例 `127.0.0.1:55439`，用于后续仅随机 schema 的验收。业务 PG 未操作。105 页台账维持 45 完成 / 60 部分；本批发布验证不改变页面完成数。

## 旧 candidate2 的真实封存（历史结果）

源码 `5118eac4d9d298574a6523af8f574381c5603ff2`，tree `6419eec414d7d10fa2ebb98b33b2c67b05b0f285`；Go 源码、go.mod/go.sum 与迁移和已启动全量回归基线 `7013c61` 相同。`5956b69` 主线为 `5118eac`：使用同构建四份页面/middleware/functions manifest 与 requiredFiles 确定选集，verifier 独立重算；构建器输入增至 10 个。未知 trace、选中项缺失/篡改、未映射依赖继续拒绝。

- 清单外锚 SHA256 `c691d84bb79ccfb148a8668763d173f934973d9d0f3c5b2b25b546da39cac27a`，独立保存 `/tmp/zjmfgo-runtime-candidate2-independent-anchor-20261017.json`；封存根 `/dev/shm/zjmfgo-runtime-candidate2-20261017`。
- Web BUILD_ID `u1mUUKZCuz0qx7Se0VcGF`；3058 artifact 文件，普通文件合计 251713685 字节；2003 个选中依赖文件，147 份选中 trace、1 份 minimal trace 明确排除并留原文/摘要；完整迁移文件 156 个（不按数量冒充 schema 版本）。
- 构建日志 `/tmp/zjmfgo-runtime-build-execute3-20261017.log` SHA256 `2fa75f68a338616d93030def046976937ca176c3223f7cfe8d6572f36a67d88e`；搬移后独立 CLI 返回 `verified:true`，记录 `/tmp/zjmfgo-runtime-candidate2-relocation-20261017.log`。
- 新构建预算外锚 `6fdabaf445638e89f2b206dbd7da8f30c7f491b56612018ee33d3e03895fa6ef`：峰值 3 GiB、最低保留 512 MiB，六项之和 2885681152 字节。185 个 2 秒资源采样最低磁盘可用 1120837632 字节、tmpfs 可用 3245989888 字节、MemAvailable 4481876 KiB；采样窗口内 tmpfs 消耗约 909 MB。采样不覆盖未观察瞬间，不替代构建器的 250ms 预算检查。
- 来源与全部字节已封存核验；真实完整 standalone 构建已执行，此结论不等于三服务已启动。

## 启动预检首轮

主线 `2e4a088` 对应已独立审查的控制器，108 项集成测试通过，日志 `/tmp/zjmfgo-runtime-controller-integrated-20261017.log`。真实 `--execute` 在创建 run root 前因 `BLOCKED: required target system library missing` 拒绝，未启动夹具、未创建随机 schema、未执行 migrate。

只读 `readelf` 核对：sharp 原生模块需要 `libvips-cpp.so.8.18.7`，其 `$ORIGIN` RPATH 指向已封存并哈希的包内依赖。当前控制器仅查宿主 ldconfig，误将包内库当作缺失宿主库。后续仅修控制器解析包内 ELF 路径和间接依赖，不修改候选字节、不安装系统库或跳过 NEEDED 检查。还需在真实 namespace 中加载 sharp 并记录实际包内库映射；静态解析本身不证明加载成功。

最终 Go 全量为 `scripts/test-all.sh`，强制 `REQUIRE_INTEGRATION=1`，独立 PG18 `_test` 数据库。890 个资源观测点最低磁盘可用 887152640 字节，整体退出 0，未触发最低余量停止。常驻 PID `608393` / `608413` / `1647696` 与原启动时间均未变化；本批没有服务重启。

## 启动契约再次冻结

在完整运行前，主代理发现 `site-server.ts` / `session.ts` 的服务端请求独立读取 `API_INTERNAL_URL`，原 candidate2 启动描述仅有正确 rewrite 和 HTTP_ADDR，仍可能回退到8088。已修构建器同时固定 `API_INTERNAL_URL`，verifier 对缺失/不同值拒绝，并补两个负向案例。116 项联合测试 PASS（`/tmp/zjmfgo-runtime-ssr-origin-integrated-20261017.log`）。candidate2 保留为旧封存证据，不编辑其清单或由 runner 临时覆写；最终同包验收改用重新构建的 candidate3。

ELF 控制器修复主线 `d00f52c` 已独立复核，6 项真实 cc/readelf 边界、14 项定向与此前114项联合通过；静态基线细分为6个ELF、1个包内libvips和24条宿主依赖。native-only隔离探针生成91字节PNG并核对实际加载文件dev/ino/SHA；这仍不替代完整迁移/API/Web/worker运行。candidate2始终保持0700，运行副本根允许755通行，所有封存字节保持不变。

## 最终 candidate3 与独立外锚

- 源码 `949ffe367f0bfac0567609d924386dfd17329871`，tree `b6026fca2f685f93a38e59699ddc5ece298e8da5`；封存位置 `/dev/shm/zjmfgo-runtime-candidate3-20261017`，Web BUILD_ID `K05HtaIAi1FMtQb7i_2m4`。
- manifest SHA256 `5a4228c5223d01fe0bd7c0c951c106b70276055b78fa6ab6b0b6fb2b2415745a`；3058个artifact，普通文件251713881字节；2003个选中依赖文件，147个选中trace及1个明确排除的minimal trace；156个迁移文件，schema版本154。文件数不是schema版本。
- [独立外锚](evidence/release-runtime-20261017/candidate3-anchor.json)保持构建时 `SEALED_NOT_RUNTIME_ACCEPTED` 原文，不回填为运行成功；后续运行结论由另一个固定提交的报告证明。Go源码、go.mod/go.sum、迁移与已通过强制PG18全量的基线相同。
- 三个Go二进制、官方Node及完整Next standalone来自同次受控构建；启动描述同时绑定rewrite、HTTP_ADDR和服务端 `API_INTERNAL_URL`。构建器10个来源输入及依赖/工具来源已核验，未靠运行时临时覆写补齐旧包。
- 最终构建日志 `/tmp/zjmfgo-runtime-build-execute4-20261017.log` SHA256 `3e79e33d393c0c3bb1ec918a7ac2d9d8c7cf8b27ea3696363aafa2df51640ad8`。152个资源样点最低磁盘可用1353666560字节、tmpfs可用2976641024字节；采样JSON SHA256 `2c3d8d74eae12b6110d8f3ea8877e5878c4ad21204d971f733d454758d4c6c16`。构建预算仍为峰值3GiB/最低保留512MiB；Go与Node临时目录进入同一受监控候选scratch。

## run3 原失败与 runner 修复

run3 已完成双迁移、三服务、页面/资源、worker任务与TERM重启，但最终网络审计将Go临时绑定的 `::ffff:127.0.0.1` 误报为非回环，故原报告保持 FAIL，不能将其追认为整轮PASS。[原报告](evidence/release-runtime-20261017/run3-report.json)及其SHA保留在证据索引。

独立分支 `92e6841` 主审后以 `ce19107a37f2781c850dcc6342d8253acf5067cc` 合入：仅允许标准IPv6 loopback和精确映射的127.0.0.1，仍拒绝wildcard、127.0.0.2、兼容IPv6、zone及外部地址。9项定向测试通过，额外独立边界审查通过；仅修改runner，不改封存包。run3的65份trace经新规则复审无其余拒绝项，该复审与后续完整run4分开记录。

## run4 同包真实隔离运行

从干净runner worktree执行 `scripts/accept-release-runtime.mjs --execute`，候选固定为949ffe3、runner固定为ce19107、manifest固定为上述外锚。使用独立PG18 `_test`库的随机schema、私有namespace和仅允许指定PG Unixsocket的回环桥；原源码、共享node_modules及Go工具链对运行包不可见。

| 检查 | 实际结果 |
| --- | --- |
| 包内原生加载 | sharp生成91字节PNG；实际加载的Node、sharp、libvips及8个宿主库与静态解析的文件dev/ino/SHA一致 |
| 双迁移 | 包内migrate执行两次，摘要均 `0a52ad9d511a11ab8b6e11d83142b2bc2e8a59ff5608d585ec23785de207720f`；此摘要属于run4随机schema，不与run3摘要混用 |
| API/Web | `/`、`/login`、`/admin-login`真实200；21份实际JS/CSS逐字节匹配封存清单；Go直连site与Web同源代理结果一致 |
| 运行身份 | namespace PID API52、Web65、worker80及重启worker92，实际exe摘要匹配同包，execve证明启动参数；身份uid/gid1000 |
| worker业务 | suspended→active的模拟resume仅一次，attempts1、version1→2、业务审计1、历史1；TERM重启后保持相同结果及无关业务摘要。此处为模拟任务，不能证明真实供应商写入或强杀恢复 |
| 系统调用 | 66份trace、6525非空行（split统计6591含末尾空行）；19个network calls、1个允许的原生探针证据文件创建；未声明写入/非回环尝试均0 |
| 清理 | 所有自有进程退出、监听消失；schema删除；主代理另行只读SQL复核临时schema0、测试库其他连接0（[查询记录](evidence/release-runtime-20261017/primary-cleanup-check.json)）；release/control/go-tmp及fixture配置自动删除 |
| 不可变输入 | 运行副本及封存包清单均未变；运行后CLI和独立全树inventory重算通过 |

[run4原始安全报告](evidence/release-runtime-20261017/run4-report.json) SHA256 `277db48728678210cb423421785d026f8833d20d6fde742bdcf8ffad07d551b2`；报告顶层 `checks: []` 是未使用的预留字段，实际结论证据位于 `runtime/isolation/audit/mutable/sealedUnchanged/fixtureSchemaRemoved`，不可按该空数组宣称额外测试。

主代理独立核对21个资源、4个运行身份和66个trace文件摘要；`gpt-6.1-sol` 子代理重算runtimeInventory、auditTraceFiles及mutableAudit，与报告逐字段一致，并核对实际TERM/execve、迁移和任务事实。验收日志 `/tmp/zjmfgo-runtime-acceptance-execute4-20261017.log` SHA256 `407775fd59e8ebab444657844ae5fe47b297003f03088e3d38a3e1ac256a80af`。

运行预算峰值1GiB、最低保留512MiB，预算SHA256 `069dc261c1e846fce2235d4953db06024fd15765e45770462b7f019499ff42aa`，与构建预算分开。16个[资源样点](evidence/release-runtime-20261017/run4-resource-observations.json)从控制器启动后开始，最低磁盘可用1290375168字节、tmpfs3331215360字节；不覆盖启动前预检或未采样瞬间，不替代250ms预算监控。

安全报告、phase和无payload的系统调用metadata已从易失tmpfs归档至本机 `/tmp/zjmfgo-runtime-safe-evidence-20261017`，173文件、3029237字节，[完整摘要索引](evidence/release-runtime-20261017/local-archive-index.json)入Git；未归档凭据、fixture配置或运行副本。`/tmp`仍非永久审计存储，重启/清理后可能丢失；Git保留小报告和摘要，完整运行包及原始trace不入Git。

## 本批边界与后续

本批关闭“可信完整运行包及同包隔离运行”的准备增量；不关闭G04/G09。公网HTTPS/正式代理与维护、旧chunk保留及切流/切回、授权业务快照升级恢复、明确目标容量/积压排空、worker执行中强杀恢复和真实外部验收仍待完成。三页面的HTTP/资源校验也不替代105页四尺寸浏览器验收。

常驻API608393（10-16 09:39:45）、worker608413（10-16 09:39:47）、Web1647696（10-17 01:58:14）启动时间复核未变；本批没有部署、推送、业务库写入或真实资金/资源动作。105页仍45完成/60部分，总体目标继续进行。

后续[真实Nginx静态保留准备](release-static-retention-acceptance-20261017.md)已单独通过：固定两封存静态集完成220→382→382激活/恢复，不启动旧Web。此证据不属于本报告run4，也不关闭完整双Web/公网切流、正式保留窗口或G04/G09。
