# schema153 代表性夹具备份恢复验收（2026-10-17）

本记录证明：在两个专用、回环连接的 PostgreSQL 18 测试数据库间，当前 schema153 的代表性夹具可以按单个随机 schema 导出、恢复，且恢复后的退款复核、冻结和幂等规则仍可运行。**这不证明旧版本升级到 153、真实业务快照恢复或上线切换成功，不关闭 G04。**

## 代码及隔离边界

- 基线：`bc535b45037b9a2a3711bce8d9f08be19caf35d3`。只增加 `internal/store/backup_restore_acceptance_test.go` 和本文；没有业务代码、新迁移或前端修改。
- 测试：`TestSchema153RepresentativeBackupRestore`。未设置 `ZJMFGO_BACKUP_RESTORE_ACCEPTANCE=1` 时跳过；已验证默认执行编译通过并显示 `SKIP`。
- 显式连接变量：源 `ZJMFGO_TEST_DATABASE_URL`；恢复目标 `ZJMFGO_RESTORE_TEST_DATABASE_URL`。两者均需预先准备、专用于本次验收，数据库名以 `_test` 结尾且不同。测试校验连接端点及服务器地址均为回环地址，服务器主版本必须为 18。
- 目标必须没有用户 schema、public 对象和非标准扩展；不会以 `--clean` 覆盖已有内容。源复用 `isolatedTestStore` 创建随机 `store_topup_*` schema；仅导出该 schema，在另一数据库恢复同名 schema。
- 使用 `/www/server/pgsql/bin/pg_dump`、`pg_restore`，本轮均为 18.0。使用 custom 格式、`--no-owner --no-privileges`，恢复时使用 `--exit-on-error --single-transaction`。不调用数据库创建/删除，不复制数据库级角色、权限或全局对象。
- 工具参数不含完整 DSN 或密码；清除继承的 `PG*` 路由/选项配置及 `ZJMFGO_*`，密码只经工具进程环境传入，错误输出脱敏。临时归档位于 Go 测试私有临时目录，结束时删除。
- 源和目标只清理本测试的随机 schema。没有真实网关调用，没有触及业务/开发数据库、服务、worker 或常驻前端构建目录。

## 代表性夹具及恢复后继续操作

| 项目 | 备份时事实 | 恢复后验证 |
| --- | --- | --- |
| 500 分在线充值 | 模拟首次正流水写入故障，保留 `pending_liability` 收款结算事实；经真实 `ResolveTopupLiability` 补入余额 | 原始结算仍失败、补账事实和正流水都保留；schema153 认可该信用证明 |
| 400 分在线充值 | 同样经故障、真实补账；外部退款已由另一管理员确认 | 只有一笔关联负流水，原收款及补账事实不变 |
| 500 分外部退款 | `pending_review`，冻结余额 500 分，尚无负流水 | `PayInvoice` 返回 `balance_reserved`；本人复核返回 `external_refund_self_review`；另一管理员通过真实 `ReviewExternalRefund` 确认，余额及冻结归零，新增且仅新增关联负流水 |
| 退款确认重放 | 恢复前已有一笔确认，另有待确认退款 | 恢复后确认待复核退款并重放同一键，`Replayed=true`，JSON 回执一致，全表/序列/目录摘要不变 |
| 未知关单 | 真实 `BeginPaymentClose` 后记录 `unknown`，未解除保护 | 未解决谓词保持为真；新关单返回 `payment_close_in_progress`，取消充值返回 `topup_payment_protected` |
| 关单后迟到收款 | 真实确认关单后处理成功通知，生成异常收款及 ignored webhook 事实 | 未解决异常保持冻结；取消充值被保护；退款继续操作不改变原异常事实 |

金额均为测试夹具。故障由仅位于该随机 schema 的触发器注入；故障触发器在真实补账前移除。恢复后的 Store 使用测试常量加密密钥，不代表生产密钥备份或密钥恢复验收。

## 数据与结构核对

逐表读取规范化 `to_jsonb(row)` 文本，按 C 排序规则排序，并以长度前缀逐行输入 SHA-256；比较每张表的行数与摘要（含 `schema_migrations` 全行及校验和）。快照事务使用 UTC、ISO 日期和固定浮点输出设置。比较每个序列的参数、`last_value` 和 `is_called`，不会调用 `nextval`。

结构核对包含完整约束定义、验证/延迟标记、索引、触发器及启用状态、函数定义、列类型、非空/identity/generated/default。约束使用 PostgreSQL 的 `pg_get_constraintdef(..., true)` 规范展示：重新解析 CHECK 会把等价的嵌套 AND 括号整理，不能直接用非 pretty 文本判定不一致。没有删除约束内容或任意剥除括号。重放按 JSON 回执比较，避免将 `time.Time` 内部时区指针差异当作回执变更。

恢复后先比对全部快照，再执行 `CheckSchema`；连续两次执行 `Migrate` 和 `CheckSchema`，每次均重新比对全部快照，证明本次重复迁移没有改变数据、校验和、序列或所核对的结构。退款继续操作完成后再核对原收款、失败结算、补账、关单及异常表摘要，并证明源 schema 完全没有被恢复后操作改变。

## 本轮实测结果

2026-10-17（Asia/Shanghai），最终完整运行通过：

```text
PASS schema153 representative restore:
  tables=204 rows=280 sequences=134 catalog_records=6036 migrations=155
TestSchema153RepresentativeBackupRestore: PASS (5.76s)
package github.com/codex/zjmfgo/internal/store: PASS (5.766s)
archive_sha256=c5ec4a13cf5a431dc1df859c092ff50c41f208b39d871a42c2b5ad6b85d2372d
snapshot_sha256=c38798b13ac7381013e670c7e5e8e8785711ed1dfede86c28557d68c1c4ef1f6
```

`schema153` 指最新文件为 `153_resolved_topup_external_refunds.sql`；仓库共 155 个嵌入 SQL 文件，版本文件名并非一号一文件。测试比较实际嵌入文件数及最新文件名，并由 `CheckSchema` 核对每个文件校验和。随机 schema、时间和随机操作标识会使每次归档/快照 SHA-256 不同；上述值仅标识本轮产物。

先前探测失败不计为通过：回环地址 `inet::text` 带 `/32`、系统目录内部 `char` 拼接、误把最新编号当文件总数、CHECK 等价括号，以及 `time.Time` 内部表示导致的回执比较差异，均为验收测试实现问题并已修正。失败与最终成功运行均只操作自有随机 schema。

成功退出后另行只读检查两个测试数据库，`store_topup_*` 剩余 schema 数均为 **0**；临时归档由 `t.TempDir` 清理。数据库本身交还调用方清理。`git diff --check` 通过。

## 复现与未覆盖项

由调用方在进程环境提供上述两个专用测试 DSN（勿把密码或完整 DSN粘到命令行），运行：

```bash
REQUIRE_INTEGRATION=1 ZJMFGO_BACKUP_RESTORE_ACCEPTANCE=1 \
  scripts/go.sh test ./internal/store -run '^TestSchema153RepresentativeBackupRestore$' -count=1 -timeout=3m -v
```

这次是**同一 schema153 的合成代表性数据恢复**。未验证真实业务备份完整性、旧版本向 153 的升级、生产角色/ACL/全局对象、对象存储/附件、生产密钥恢复、执行中事务/worker/租约重启、真实网关重试、备份保留与加密策略、RPO/RTO、完整回归或上线切换。5.76 秒仅为该小型夹具的测试时长，不是业务恢复时间目标。G04 仍需真实业务快照及升级/回滚/切换演练证据。

主线集成后，主代理显式开启opt-in并追加 `-race` 复验通过（测试11.28秒、包12.318秒），再次核对两个库无schema/连接残留后仅删除本次专用库，详见[集成验收](release-preparation-integration-acceptance-20261017.md)。
