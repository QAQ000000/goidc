# G04 发布准备首增量集成验收（2026-10-17）

结果：独立候选构建、封存校验及当前schema代表性备份恢复通过。**G04/G09仍未关闭，没有部署本轮功能、启动候选、切流或执行真实商户/资源动作。** 这批由两个 `gpt-6.1-sol` 子代理在独立分支提交，主代理审查集成后实测。

## 固定源码候选

构建器提交 `87895d0` 已以 `de85f5d` 集成，见[构建器契约](release-candidate-builder-20261017.md)。主代理从干净独立worktree选择完整提交 `de85f5db1688c246d2154f624f73f6d5052b5b78`，构建器再创建自己的固定checkout执行所有构建。没有从主仓拷入已有 `.next`，没有复制或安装依赖。

| 项目 | 实测 |
| --- | --- |
| 源码 | tree `5381106f302bcb751feab92af3e4aa1d3ac7870e`，1259个跟踪文件 |
| 工具链 | Go1.25.14 linux/amd64、Node24.16.0、npm11.16.0 |
| 二进制 | api、worker、migrate均实际构建；主代理再次读取三份实际Go build info，与清单逐字匹配，revision相同、modified=false |
| Web | Next16.3.8，显式webpack生产构建及其TypeScript阶段PASS；BUILD_ID `8NG3KzbMx_z16UloA4xjx` |
| rewrite | 实际routes-manifest指向本次指定的 `http://127.0.0.1:58088/api/:path*`；该地址仅为隔离候选目标，没有启动该候选或将其晋升运行包 |
| 迁移与模板 | 155个SQL文件，最新 `153_resolved_topup_external_refunds.sql`；与固定源码及校验和绑定 |
| 封存文件 | 1064个普通文件，合计72,989,671字节；不含可重建 `.next/cache`、node_modules和manifest自身；磁盘目录约75MiB |
| 依赖 | 只读受控复用；32749项树摘要前后相同，不等于可信安装证明或OS只读挂载；不包含runtime依赖，不能称离线运行发布包 |
| 清单可信锚 | `590f64b5d4d44677771f41223a8bf8c957dac4ad2b49add18d80e544503bc4cc`，从构建结果另行记录，完整清单留在本地候选目录 |

本地候选：`/tmp/zjmfgo-sealed-candidate-20261017`。构建退出0；日志 `/tmp/zjmfgo-candidate-build-20261017.log` 的SHA-256为 `a8d11921d7adc2fa849575b6a5c623b246c28d78f51171fe9771496c52345968`。去敏索引见[集成结果](generated/release-preparation-integration-20261017.json)。候选是该精确提交，不宣称包含随后新增的恢复测试或验收文档。

主代理在真实候选上分别修改迁移SQL字节、添加额外文件、添加软链接，verify均明确拒绝；每例恢复原文件或移除探测项。探测结束后再次使用独立保存的清单SHA及可信干净源码执行CLI核验，退出0。Node夹具/真实进程保护/支付恢复合并测试28/28通过，覆盖缺件、篡改、清单锚、来源dirty、越界和链接等。这里的故障注入只发生在未启动的隔离候选，不触及业务文件。

## 当前schema代表性恢复

恢复测试提交 `5a0b629` 已以 `4f06178` 集成，见[专项范围](schema153-representative-backup-restore-20261017.md)。主代理在两个新建的专用PG18 `_test`库上复验：

```text
REQUIRE_INTEGRATION=1 ZJMFGO_BACKUP_RESTORE_ACCEPTANCE=1
scripts/go.sh test -race ./internal/store
  -run '^TestSchema153RepresentativeBackupRestore$' -count=1 -timeout=3m -v
PASS: test 11.28s, package 12.318s
204 tables / 280 rows / 134 sequences / 6036 catalog records / 155 migrations
archive SHA256 da8dcb6e01a51bf40da13638ed26db9c477808ba95a7b944996c3aa7e9275d0c
snapshot SHA256 8a5b748cbc7e43176461e611144385e8ebd86b85dfc1f96e495686b19115eaf9
```

实际custom导出/恢复、全表及结构摘要一致；两次重复Migrate/CheckSchema保持不变。恢复后冻结仍拒绝消费、自审拒绝、独立确认成功且原键重放不重复写，最终两条独立退款负向流水，原收款/失败结算/补账/未知关单/迟到异常保持。仅证明**同一schema153的合成代表性夹具**，不是旧版本升级、业务快照、生产角色/密钥、执行中worker恢复或RTO/RPO证据。

日志 `/tmp/zjmfgo-schema153-restore-mainline-20261017.log` 的SHA-256为 `e4dc80db5a811b333b15090a785e877e2f2f69ee7310f537ddfd59a7cd099250`。退出后两个库均用户schema0、其他连接0，主代理仅删除这两个自建库及自己的连接配置文件，复查库名残留0。没有删除共享PG实例或其他数据库。

## 集成边界

本批新增发布脚本和opt-in恢复测试，未改Go经营逻辑或页面。主线 `scripts/go.sh build ./...`、`scripts/go.sh vet ./...`均退出0；28项Node测试和上述race通过。未重复约26分钟Go全量，最近一次强制隔离全量已在[支付集成记录](resolved-topup-external-refund-acceptance-20261017.md)记录；不能把opt-in默认跳过视为已运行，本批已显式启用。

候选构建期间及结束后，常驻API/worker/Web的PID分别保持 `608393`/`608413`/`1647696`，三个服务active，常驻Web BUILD_ID保持 `0iI9JDdJluwxA2Y10gqnW`。没有覆盖其构建或重启服务。本批之前发生的误覆盖及旧Web恢复见[单独记录](live-web-build-recovery-20261017.md)，不能用本段掩盖该恢复操作。

接管剩余项保持[正式契约](release-readiness-next-contract-20261017.md)：受控runtime依赖/完整发布物、授权业务快照升级恢复、真实目标HTTPS/来源/端口核验、旧chunk保留、三服务同包运行与切回、真实外部经营证据、目标容量及执行中崩溃恢复。正式发布不能直接使用本次测试rewrite目标，也不能仅凭manifest或小数据恢复时间放行。
