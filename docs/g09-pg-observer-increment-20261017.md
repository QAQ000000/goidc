# G09 只读PG观察辅助器增量

状态：小型helper及opt-in隔离PG测试已实现；真实PG查询尚未执行。当前仅完成无DSN编译/纯配置验证（最新race专项2 PASS、1个明确opt-in SKIP）。不证明SQL运行正确、worker业务排空或容量；不连接PG、不修改常驻服务、不构建应用。证据在 `evidence/g09-pg-observer-20261017/no-pg-concurrency-race.txt`（初轮证据 `no-pg-test.txt` 保留）。

`internal/g09fixture/pg_observer.go` 仅供测试fixture/controller使用，生产入口没有导入本包。`NewPGObserver(pool, config)` 不连接，校验并复制冻结的明确授权配置：精确 `_test` 数据库、`zjmfgo_test_[a-f0-9]{16}` schema、最多512个唯一正数supplier账户ID、1–1000样本和1ms–5秒查询时限；pool配置必须精确匹配数据库/schema search_path且只用IPv4 loopback。调用方负责该池和schema的独占授权，不接收外部任意SQL/表名/DSN参数，不允许带秘密记录器的pool。

`Sample(ctx)` 使用带context deadline、transaction-local statement/lock timeout的REPEATABLE READ READ ONLY事务；每次核验物理session的数据库、schema、完整search_path、loopback和PG18版本，首次成功读取的schema OID在后续采样保持一致。查询/超时/身份/预算/缺失授权账户错误锁定，后续拒绝；公开错误为固定码，不返回原始PG/连接错误。事务回滚单独最多1秒清理。调用方串行调度并持久化样本；helper不调度、不重试、不signal、不回收业务状态。不会清/推进lease、任务或available_at。

采集范围：

- 三队列按真实status计数及pending ready/future/oldest-ready-created年龄。一次sample从服务端 `clock_timestamp()` 取得真实观察时刻，绑定同一时间做全部ready/future划分，不因两次时钟比较跨边界造成重计/漏计。无ready时年龄为nil。成功查询返回的空队列没有status行，代表该队列各状态计数为0。只读取状态/计数/时间，不读取消息正文、收件人、错误文本或凭据。
- 冻结supplier账户逐个输出ID、running_job_id和相应job状态/locked_at；缺失账户即FAIL。该字段是账户保留的owner事实，没有臆造lease到期时间或自动认定锁已释放。fixture-only seed不算API来源或真实经营吞吐。
- PG连接、事务提交/回滚、deadlock与stats_reset来自整个授权database，包含该库其他schema及观察器自身。active/idle-in-transaction/Lock等待仅选该database，不读取pg_stat_activity.query；若任意state被权限隐藏，三项详细值全部nil并明确 `ActivityDetailsAvailable=false/privilege_redacted`。pg_locks等待数只统计database标签匹配的锁，不含database=NULL的cluster锁，不能代称全cluster锁等待。
- WAL records/fpi/bytes/reset来自整个cluster，明确 `whole_cluster_not_schema_or_database_attributable`，绝不能归属本schema或数据库；bytes保留数字文本精度。WAL/PG视图缺SELECT权限时以availability=false、nil及固定原因表达；实际查询异常仍FAIL，不把权限之外的错误吞成零。统计累积计数需结合reset及独立前后样本解释，不自行计算归因或吞吐。

全部观察返回 `CapacityVerified=false`。不证明候选摘要、普通UID/namespace、资金不变量、cohort实体终态、periodic cycle历史或真实通知/供应商效果。MVCC业务快照和PG累计统计不是全系统原子瞬间，观察查询自身有开销。

## 验证边界

当前命令显式删除授权/DSN变量：

```
env -u ZJMFGO_TEST_DATABASE_URL -u DATABASE_URL -u ZJMFGO_G09_PG_OBSERVER_SMOKE ./scripts/go.sh test ./internal/g09fixture -run '^TestPGObserver(PureConfiguration|IsolatedPG18Snapshot)$' -count=1 -timeout=1m -v
```

纯配置用例使用禁止网络的DialFunc并确认dial次数为0，覆盖目标覆盖参数/非loopback/非_test/错误schema/重复账户/预算拒绝及配置切片冻结。首次compile发现availability原因字段声明遗漏，补齐后上述命令PASS；真实SQL仍未验证。

后续仅在主线全量结束且取得独占PG授权后，显式设置 `ZJMFGO_G09_PG_OBSERVER_SMOKE=1` 和授权的 `ZJMFGO_TEST_DATABASE_URL`，运行相同测试选择器。opt-in fixture在任何schema写入前核验PG18/数据库/loopback，随机独占schema、完整生产迁移、有限fixture-only队列及lease seed，退出关闭专属pool、DROP唯一schema并查询确认消失；未授权默认SKIP。测试覆盖ready/future/nil年龄、lease、脱敏、样本预算、锁阻塞超时锁定、物理session search_path不符和取消锁定。代码完成不代表这些PG断言已通过；受限数据库角色的实际availability分支也待授权实测。

## 静态复核后的窄修

fixture账户provider改为真实注册值 `zjmf_finance_v1`，不再使用描述符式简写。lease指向simulation服务的running job仅是只读join样例，不证明真实supplier领取/保留/释放账户租约；需真实worker专项另验。未弱化任何生产迁移trigger，data-modifying CTE seed兼容性仍待独占PG实测。

Sample入口使用非阻塞原子状态机，禁止mutex排队延后timeout：并发调用立即拒绝并锁定失败，原inflight即使已读完也必须通过原子成功发布关卡，否则丢弃结果为FAIL。成功发布与下一次调用有明确原子顺序；不会因更晚、已经不重叠的新调用追认撤回此前成功样本。增加无网络合成DialFunc暂停inflight、并发立即拒绝、失败后无再次dial、样本预算耗尽不dial的race回归。最新测试2 PASS、1 SKIP，所有DialFunc均拦截为合成错误，未建立任何PG/网络连接。
