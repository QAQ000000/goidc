# G09 精确 600 来源与真实 worker 前置增量（2026-10-17）

本增量新增显式 `ZJMFGO_G09_COHORT_SMOKE=1` 的 `TestG09CohortSourceWorkerSmoke`。它是 source Handler + loopback 协议 fixture + 两个新构建 `cmd/worker` 的有限集成入口。它不代表 release candidate、普通用户 namespace、27 分钟混合负载或生产容量已通过，也不补造 1000 客户、2000 服务、10000 账单的完整 seed。

当前状态：主代理固定 `2f32106` 的 `run4` 已在独占隔离 PG18 通过完整600来源、双真实worker、三边界独立对账及两个真实minute周期验收；用例243.64秒，未SKIP。此前run1–3失败均保留。结果与组合回归见[本批验收](g09-cohort-acceptance-20261017.md)。最初独立开发阶段只做无DSN守卫；以下条件已由本次实际入口验证，但不扩大为候选或容量证明。

## 来源与准备边界

`g09_cohort_source_worker_smoke_test.go` 按 prepare → purchase/ticket → seal → drain helper 分阶段；已有三个 source/supplier smoke 文件不改动。

准备在正式来源计时之前完成：

- 独立随机 schema 和迁移；fixture SQL seed 身份、cart、150 部门、每部门一名只有 `tickets.view/tickets.manage` 的员工、唯一已验证 `.test` 邮箱与 `+999` 合成 E.164 手机号；身份不计注册能力。
- 每身份经实际 HTTP login 获取 session，检查所有 session 剩余时间超过 15 分钟。登录使用各自 `127.1.x.y` loopback socket 来源，避免把 301 个独立身份当成同一来源触发生产每分钟 30 次登录限制；该准备过程不计来源窗口或登录吞吐。
- 员工用各自 session 经真实通知偏好 API 开双渠道。除 ticket_customer_created 的两模板外均停用；SMTP/SMS 各只有一启用实例，渠道通过真实 API 保存并使用生产 secret 加密。正文和邮件 subject 带 runId/ticket_no。
- 150 客户各经真实余额 API 入账 10000 分，保存完整 opening ledger checkpoint、余额与 11 个商品的 opening stock。没有服务、订单、账单或任务的 SQL seed。
- 1 个 simulation 商品库存 270，单价 1000、setup fee 0；10 个 supplier 账户各通过真实账户/预览/导入/发布 API 导入一个商品，库存各 3，冻结上游 product 7 的 revision、monthly 与空必填配置。每账户独立 metadata gate、sink、journal 与预算。

`admin_ticket_department_allowed` 对 super_admin 直接放行，不能靠部门范围或未验证地址排除其额外 skipped delivery。因此 bootstrap 在管理准备完成后转为无工单权限的 fixture admin 角色；正式来源阶段恰好 150 个合法员工，contacts 为空。

正式 run 前先执行独立 schema 的 ticket pilot：保留完整 150 员工/部门身份，但只用一个客户真实建票。查询该票**全部** delivery 决策，必须恰好两条 queued，分别映射一个 email/SMS outbox，不能只筛选正确收件人而漏掉额外通知；pilot schema 清理完才新建正式 schema。

## 固定 120 秒正式 source 窗口

worker 此时从未启动，source 请求并发上限为 1，不存在无界 goroutine；每个客户独占自己的 cart。当前选择串行以保持来源可审计，预算仍严格 120 秒，不能因执行缓慢抬高。

300 次 quantity=1 的真实 cart → order → GET invoice 核验归属/金额/版本 → balance pay：270 simulation + 30 supplier，10 账户各 3，150 客户各 2 次采购。再由 150 客户各对唯一部门经真实 API 建一张 ticket，每票恰好产生一个 email 和一个 SMS。总计 300 create + 150 email + 150 SMS = **600 个队列任务**。

每 mutation 原始 payload/hash/key 在发送前写入私有 fsync ledger，成功 response 也写入同 ledger；请求未知/错误/超时立即 FAIL，保留原链，不换 key/version 重买。HTTP 和 source mapping SQL 共享固定 deadline context；最后接纳时刻必须在开始后 120 秒内。完整来源结束后停止 source deadline，再做只读 seal 检查，不把 seal/worker build/fixture seed 算入来源耗时。

冻结记录逐 flow 保存 customer/product/supplier/binding/order/invoice/orderItem/service/createJob 与 ticket/event/staff/department/emailDelivery/emailOutbox/smsDelivery/smsOutbox ID。校验每张表的正式 ID 不重复，跨表同值合法；每来源绑定真实实体；300 create 与两 outbox 集合在启动前全部 pending、attempts=0、无 lease。source 列表写入一次性私有文件后才允许启动 worker。

supplier metadata 阶段不允许实体写；来源完整后以实际 service ID 推导生产合同的逐服务 downstream token，切换一次到冻结的三资源 sink，每账户独立 worker 请求预算 240。HostID=`420000+serviceID`、upstream invoiceID=`900000+serviceID`，仅 loopback fixture 使用。

## 两 worker 排空与持久效果

测试执行两个真实 `cmd/worker`，保留生产 2 秒/10 秒/1 分钟 timers 与每 loop batch 常量；从 worker 开始到 drain、稳定性核对结束的总预算最多 10 分钟。测试不调用 Claim/Process/Complete，不改任务/service 的状态、available_at、租约、due、scheduler 时间。

成功只针对冻结的 600 ID：create succeeded、outbox sent；failed、waiting_verification、expired/dead_letter 不能算成功。初次成功后逐来源核对付款/账本/订单 unit/库存、服务开通/audit、ticket source 决策、SMS 加密响应证据以及 SMTP/SMS 实际协议 hash。每用户最终余额 8000、完整账本 3 条且净额吻合；11 商品库存均为 0。

以只读稳定投影保存首次成功后的任务/服务/账单 paid_at/余额 ledger/stock/SMS evidence digest，之后每秒核对不变。正常 worker 的四类 minute scheduler 在首次成功后，各须产生至少两个 succeeded、affected_count=0 的真实审计 run，最早/最晚 started_at 间隔至少 55 秒；不能用两个 worker 同一时刻各跑一次冒充两个 minute ticks。结束前再次检查相同实体/协议和 digest。

periodic supplier sync 保留生产循环，独立按 ID/service/action/status/attempts/updated_at 采样，与冻结 600 分开；合法 background 出现不会直接 FAIL。它们只允许这 30 个 supplier 服务的 sync、至多 30 个可重用 periodic row。supplier 协议 oracle 逐服务检查最初八次 create/pay 操作及响应 hash，后续 Active host/header 归于 periodic read；不把可重用 job ID 当成完整 attempt history，不声称 periodic exactly-once。公开结果明确 `backgroundAttemptHistoryComplete=false`。

两 worker 均需正常 TERM/Wait，最终账户 running_job_id 为空。cleanup 只 drop 本测试随机 schema，并用独立只读连接核实 schema 已删除；共享 PostgreSQL/常驻服务不动。

## 隐私与验证命令

run 目录 0700，mutation ledger、冻结 manifest、opening baseline、worker logs 和协议 journal 为私有 0600。公开日志仅阶段、耗时、数量、边界，不输出 DSN、cookie、token、原始 body 或手机号/邮箱。测试临时目录遵循 Go testing cleanup 规则，不是永久归档接口。

无 DSN守卫（本次开发已通过）：

```sh
scripts/go.sh test ./internal/httpapi \
  -run '^TestG09(CohortSourceGuards|CohortSourceWorkerSmoke|SupplierWorkerProtocolGuards)$' \
  -count=1 -v
```

其中重型测试应明确 SKIP；guard 检查精确混合、缺漏/重复 ID、跨渠道 delivery ID、单账户预算、客户关联、accepted time，并经真实 compiled supplier adapter 对三资源完成 create/pay + 多次合法 sync，验证一账户三资源 oracle，完全不需要 PG。

独占授权 PG 槽执行命令（主线run4已加 `-race` 实测）：

```sh
ZJMFGO_G09_COHORT_SMOKE=1 REQUIRE_INTEGRATION=1 scripts/go.sh test -race ./internal/httpapi \
  -run '^TestG09CohortSourceWorkerSmoke$' -count=1 -timeout=20m -v
```

DSN 必须由私有环境提供 URL 形式的 loopback PostgreSQL 18 `_test` 库，不允许 options；worker 使用唯一精确 search_path。

## 主线独立对账桥接

已接入独立 `internal/g09fixture` immutable sealer/verifier 与只读 PGObserver：启动 worker 前必须逐600任务 pending、attempt0；首轮排空和跨两个真实 minute tick 并正常停 worker 后都须逐实体 PASS。排空期间每次采样结束后至少间隔2秒，记录实际 ObservedAt 与耗时，不能称严格固定频率采样。事务只读，不推进任务时间或生产调度。

三个边界的600任务观察投影保留在测试日志；FlowID 原本关联 mutation key，公开时逐条 SHA256，其他公开字段仅实体ID、状态、时间与计数，无原始请求/凭据/地址。中间样本保留队列和明确可用性/null语义。最终只读负例仅改 manifest 的 invoice owner，必须在真实 purchase SQL 核验失败并永久 latch；不修改数据库事实。worker 的10分钟总预算同时检查循环起点、成功时和正常停机后，避免在超时边界靠先break而误过。

上述桥接在run4实际PG18执行中通过：sealed_pending/first_drained/final分别逐600任务PENDING/PASS/PASS，wrong-owner只读负例拒绝且FAIL latch。预检及结果经独立同模型复核；容量和候选仍未验证，见[最终验收](g09-cohort-acceptance-20261017.md)。

## 首轮实际 PG18 FAIL（保留）

固定 `8962873`，独占授权PG18强制 opt-in race 退出1，用例140.58秒；pilot PASS，正式600来源完成封存，独立只读样本逐600任务全部pending/attempt0、不变量通过。排空后原 `verifySources(false)` 的某附加SQL布尔断言失败，旧helper只输出nil错误，无法定位具体谓词，不记为600成功或容量通过。原始日志、sealed边界与失败结果留在 `evidence/g09-cohort-20261017/run1/`，schema集合恢复、常驻三服务身份未变、API/Web前后200；父代理随后确认两个owned worker PID均不存在，未追认缺失的正常退出证据。

复跑前仅增加脱敏query SHA256/数值实体参数、失败队列/采样投影及owned TERM/Wait记录；不公开SQL、正文、地址或driver错误。新增数据库deadlock计数变化/不可用/重置即FAIL守卫。原来源120秒和worker总10分钟门槛不变，不降低逐实体检查或修改生产实现。

诊断 `run2` 固定 `5a2db1a` 退出1，用例143.03秒。SQL hash `0426390b…` 精确对应通用purchase关联断言，service/create ID271已有正常sync行；失败快照实际是300 create succeeded +150 mail sent +150 SMS sent（各attempt1），另29个正常sync succeeded。断言的`j.id=$9`写在SELECT布尔表达式、JOIN却仅service_id，QueryRow可能取到sync行。独立分支窄修将冻结create ID同时放进JOIN，保留全部原谓词及计数，不改生产代码或周期背景。

run2两个worker均正常TERM/Wait、PID消失，32个数据库样本deadlocks均0，schema和常驻核对通过，FAIL原证据不覆盖。另记录1次job claim的`23514`：错误hash与已知`renewal_refund_hold`守卫字符串完全匹配，任务领取事务回滚后由正常轮询继续；不把该事件抹成无错误日志，也不调整154/155保护。后续成功/失败结果均保留脱敏worker错误数量/hash/SQLSTATE，可用性不明不当0；不声称零worker ERROR。


## 第三轮 FAIL 与 SQL 提前预检

`run3` 固定 `f7dd58a`，强制PG18 race退出1，用例141.68秒；pilot通过，正式600任务封存后全部排空，但供应商附加断言解析失败，不记整轮PASS。SQL hash `8eb497ca…` 对应同一参数先 `$3::text`、后与bigint比较，PostgreSQL推断text后拒绝bigint=text；独立只做会话PREPARE的复现同样失败。将首处改为 `$3::bigint::text` 后PREPARE通过，保留全部业务谓词。失败快照为300 create succeeded、150邮件sent、150短信sent，全部attempt1，另27条合法sync succeeded。两个worker正常TERM/Wait后PID消失，schema恢复、常驻身份不变、API/Web200；保留一条23514保护日志，不称零ERROR。

原始结果、日志、边界及参数推断复现留在 `evidence/g09-cohort-20261017/run3/`，不覆盖先前FAIL。新增 `g09_cohort_oracles_test.go` 将实际使用的10条附加验收SQL提取为共享常量，在每个schema迁移后、身份/来源/worker启动前，用5秒预算的只读事务Prepare同一SQL；不执行终态写、不改数据库事实。解析错误输出query SHA256和SQLSTATE，屏蔽driver原文与字符串参数。实际验收仍执行同一完整断言，预检不能替代最终逐实体对账。
