# 受控运行包真实构建与隔离验收（2026-10-17）

状态：进行中。真实依赖安装及修复后的完整运行包封存、搬移核验已通过。首次封存拒绝与后续启动预检拒绝保留如下。当前没有同包运行验收结果，不关闭 G04/G09，不部署或推送，不重启常驻服务，不写业务数据库。本记录延续 [运行包契约](release-runtime-next-contract-20261017.md)。

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
| 修复后的完整候选与搬移核验 | PASS；干净 `5118eac`，3058 artifact 文件，外锚与搬移后源码/字节核验一致 |
| 同包双 migrate / API / Web 页面与 chunks / worker TERM 重启 | 启动预检因 package libvips 被误分类宿主库而拒绝，尚未开始迁移；修复后重验 |
| 写边界、无外网、退出、schema 清理 | 待执行 |

独立空库 `zjmfgo_release_runtime_20261017_test` 创建于本批 PG18 测试实例 `127.0.0.1:55439`，用于后续仅随机 schema 的验收。业务 PG 未操作。105 页台账维持 45 完成 / 60 部分；本批发布验证不改变页面完成数。

## 修复后的真实封存

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
