# G09 run4 与组合回归独立证据审查

结论：本次只读证据复核无阻断，支持固定源码 `2f321067b472e49be9765477a628b29585b6c06c` 的 **source Handler 600任务前置夹具 PASS**；不构成 candidate、普通用户 namespace、27分钟负载或生产容量通过。审查未连接PG、启动进程、重跑测试或修改生产/测试代码，仅独立重算现存文件并核对固定提交源码。

## 直接重算

- `evidence/g09-cohort-20261017/run4/index.json` 的3项字节数/SHA256全部一致：test.log `0d8d634866891ea763fde49cfdc2fbe705971022b34e77980b970afd33611404`；boundaries.json `a621924284811aae86f6802c9e684195712a183e57566405f0c9da5dac0b0f97`；result.json `aa5d6e16f905fe29b6898c05d794ce4fddbccc5f38994a6728e8e3e9a6190866`。result.source_sha256列出的8份源码，与固定提交blob及审查时主工作区文件逐一一致。
- 从原始test.log重新解析的3条 `G09_COHORT_VERIFICATION` 与boundaries.json完全相等；唯一 `G09_COHORT_RESULT` 与result.cohort完全相等。原始日志显示用例PASS（243.64秒）、包PASS（244.677秒）、两次实际10条SQL Prepare预检，以及wrong-owner只读SQL拒绝并latch FAIL的明确通过行；没有SKIP。
- 三边界均600个不重复任务键、450个64位十六进制FlowID哈希。sealed_pending为PENDING/Complete=false：270 simulation、30 supplier、150 email、150 sms全部pending/attempt0；first_drained和final均PASS/Complete=true：300 create succeeded、300通知sent，全部attempt1、无lease且完成时间非空。采购300个独立flow，工单150个flow各对应email+sms一对。first_drained与final的600条完整任务投影逐条完全一致。
- result与原始结果的source窗口为24.671784829秒；worker启动到首次排空约77.514472秒，首次排空到停机约102.969678秒。77条保存的数据库样本deadlocks均为已知0、StatsReset均为null；不是把不可用计数补0。后台6份变化快照共30个不同sync job，最后保存状态均succeeded，`backgroundAttemptHistoryComplete=false`保持明确。
- 原始结果保留两个owned PID及 `workersExitedViaTERM=true`，runner结果另记PID消失；前后schema集合、三常驻PID的exe/cwd/start_ticks和API/Web 200记录逐项一致。这是对留存证据的比较，未重新实施现场进程/健康检查。
- workerLogs明确available=true、errorCount=2；两条SQLSTATE均23514，同error SHA256 `03f5a20504651b773780a4b8bbba531c8961571f53b1bce67c2c3335a9a55a0b`。独立重算该值与字面错误 `ERROR: renewal_refund_hold (SQLSTATE 23514)` 一致；message哈希与源码 `job claim failed` 一致。不能称零worker ERROR，哈希也不能单独定位155迁移中的具体触发点。
- `combined/index.json` 的7项字节数/SHA256全部重算一致。直接从未修改的Node spec日志按 `ℹ` 解析：72 tests、72 pass、fail/cancelled/skipped/todo均0，并数得72个通过标记；无需重跑Node。fixture-race原始日志有21个顶层PASS，包含实际PG18 snapshot与超时/身份负例；http-race有7个顶层PASS（4条source小链、3个guards），均无SKIP/FAIL/race告警；包耗时分别3.720秒和17.196秒，与派生结果相符。
- build/vet/diff日志为空，其成功退出0来自runner记录，空日志本身不证明退出状态。组合结果同样固定2f32106，前后schema/常驻/health证据一致；组合小链不是第二次600任务重跑。

## 依固定源码及测试通过记录成立的检查

150个客户、每客户2采购+1工单、10供应商账户各3采购、付款1000分、余额/全量ledger/库存/credit/退款/外部付款不变量、通知收件身份及协议正文哈希，由固定源码的seed循环、纯sealer和逐实体真实SQL/私有协议oracle在本次PASS路径中验证。公开600任务投影不含customer/ledger/正文，故这些项目不是本审查从公开JSON重新查询或独立重算的原始业务事实。

两个真实分钟tick由固定源码查询4类scheduler_runs各至少2次成功、间隔至少55秒且affected_count=0确认，最终结果记录true；公开证据未逐行导出scheduler_runs，本审查不把102.97秒停留本身当作tick证明。正常TERM/Wait、wrong-owner拒绝及latch也依据对应源码路径与原始通过记录，不把结果布尔值扩展为额外现场验证。

10条实际断言在迁移后、业务来源/worker前使用相同常量进行ReadOnly Prepare，保持全部业务谓词；supplier参数仅统一为bigint后再转text。预检只验证解析/列名/类型，不执行终态断言。采样为顺序独立cohort/observer快照，各有ObservedAt；约2秒加采样耗时的间隔不是严格2秒时钟。数据库deadlock统计范围为整个授权库，WAL范围为整个集群，均非本schema独占归因。

## 脱敏与尚未证明的范围

逐字段核对公开投影并扫描run4原始日志/两个派生文件及组合原始日志，未发现DSN、Cookie/Authorization、邮箱、E.164手机号或已知fixture明文secret；FlowID在输出前SHA256，错误仅保留hash/SQLSTATE/已知可用性。此为当前证据检查，不保证所有未来日志内容安全。

私有mutation/baseline/oracle/journal未公开，本审查未独立读取或重新计算私有协议字节；source API使用进程内Handler，worker为真实cmd/worker。candidate摘要一致性、普通UID/network namespace、完整27分钟负载、全量后台attempt历史及正式容量/SLO均未验证；所有candidateVerified/capacityVerified仍为false。保留run1至run3 FAIL及run4两条23514，不以本次前置夹具PASS覆盖历史异常或扩张验收范围。
