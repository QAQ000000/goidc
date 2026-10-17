# G04 runtime 构建与核验基础集成记录（2026-10-17）

状态：实现准备增量通过单元及负向测试；完整 standalone 构建、封存候选、同包迁移/API/worker/Web 启动仍未执行。G04/G09 保持开放。范围承接[受控运行包契约](release-runtime-next-contract-20261017.md)，不改变[订单退款本批](order-external-refund-acceptance-20261017.md)已经冻结的最终全量源码范围。

## 本次集成

两个 `gpt-6.1-sol` 子代理分别在独立工作分支完成集成审查和独立源码复核，主代理审阅后移植提交：

| 主线提交 | 实现 |
| --- | --- |
| `439838d`、`cabbdc7` | opt-in standalone 构建器、固定源码/依赖/Node 输入、独立磁盘预算、私有只读挂载、实际 tracing 映射、可搬移启动描述和路径修正 |
| `c117f45` | 独立 v2 清单及运行闭包核验；v1 格式和保护规则保持原义 |
| `32289f8` | 真实 HEAD 提交树必须包含所有 builder/verifier 输入，防止只测试手工拼出的 fixture 而漏选兄弟分支实现 |
| `e10e6d1` | 补齐实际导入辅助脚本、Go wrapper 与构建前运行目录保护的来源绑定；现为9个输入，运行代码与选定提交不一致时在读取外部证明和编译之前拒绝 |

`web/next.config.ts` 仅在显式设置 `ZJMFGO_RUNTIME_BUILD=1` 时启用 standalone。CLI 默认仅预检，完整构建需显式 `--execute`。启动描述是数据契约，尚无完整运行验收控制器，不能把它视为已经启动三服务。输入证明结构及命令见[构建器说明](release-runtime-builder-20261017.md)。

## 已执行核验

| 检查 | 结果 |
| --- | --- |
| 独立审查分支真实源码证明 | PASS：干净 `4c217ed`，1285个源码文件及9个构建/核验输入；不依赖手工补缺文件的 fixture |
| 主代理独立分支联合测试 | PASS：90项，0失败、0跳过 |
| 主线 `e10e6d1` 集成后联合测试 | PASS：90项，0失败、0跳过，20.354秒，命令退出0 |
| 集成前端 typecheck | PASS：`npm run typecheck --prefix web`退出0 |
| 私有 mount namespace 能力探测 | PASS：独立 `unshare --mount --propagation private true`退出0；此项没有挂载构建输入或启动服务 |
| 完整 Next standalone 构建及真实 tracing 闭包 | 未执行 |
| 搬移候选、同包迁移和三服务启动 | 未执行 |
| PG18、页面/chunk/代理、worker任务及退出/写入审计 | 未执行；订单退款的PG18全量与浏览器不替代本项 |

联合命令：`node --test scripts/release-runtime-builder.test.mjs scripts/release-runtime.test.mjs scripts/release-candidate.test.mjs scripts/check-web-build-runtime.test.mjs`。主线最终日志本地 `/tmp/zjmfgo-runtime-integrated-20261017.log`，SHA-256 `bfc11739a803bfd66284b7fa91b50a0ca56a5b5d5a5047d2537663486884f5f5`。

## 下一步及边界

完整构建前仍须取得可核验的锁定依赖安装与Node来源，并测量包含checkout、构建临时文件、standalone、封存、运行副本和测试PG的真实峰值预算。本次根文件系统可用约2.0GiB，仅为时点观察，不能当作满足预算的证明；未复制整棵共享依赖、安装新依赖或清理其他工作区。预算由执行者在既有授权下记录，不是新增用户审批步骤。

之后应冻结包含最终业务代码与全部构建器的干净提交，按契约构建并执行隔离同包验收。90项小型测试只证明已覆盖的构建/核验拒绝规则和路径语义，不证明可发布或可生产切换。本次没有部署、推送、重启常驻服务、写业务数据库、执行真实退款或供应商动作。
