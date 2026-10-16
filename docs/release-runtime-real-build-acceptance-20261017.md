# 受控运行包真实构建与隔离验收（2026-10-17）

状态：进行中。真实依赖安装完成；首次完整 Go/Next 编译通过，但封存被 trace 检查拒绝，未生成可验收候选。当前没有同包运行验收结果，不关闭 G04/G09，不部署或推送，不重启常驻服务，不写业务数据库。本记录延续 [运行包契约](release-runtime-next-contract-20261017.md)。

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
| 强制隔离 PG18 全量 | 运行中；`/tmp/zjmfgo-runtime-final-full-20261017.log`；不能预先登记 PASS |
| 修复后的完整候选与搬移核验 | 待执行 |
| 同包双 migrate / API / Web 页面与 chunks / worker TERM 重启 | 待执行 |
| 写边界、无外网、退出、schema 清理 | 待执行 |

独立空库 `zjmfgo_release_runtime_20261017_test` 创建于本批 PG18 测试实例 `127.0.0.1:55439`，用于后续仅随机 schema 的验收。业务 PG 未操作。105 页台账维持 45 完成 / 60 部分；本批发布验证不改变页面完成数。
