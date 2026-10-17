# G09 逐来源 cohort sealer / verifier 增量

状态：已实现独立 fixture-only Go 接口及纯 guard 测试；本增量未连接 PostgreSQL、运行 worker、迁移、部署或负载测试。完整600任务实PG证明由 source600 opt-in fixture 在独占授权槽中调用本接口完成，本文不把编译/纯测试称为数据库或容量验收。

`internal/g09fixture/cohort*.go` 提供：

```go
SealCohortSources(records []CohortSourceRecord) (Cohort, error)
(c Cohort) Sources() []CohortSourceRecord
NewCohortVerifier(pool *pgxpool.Pool, cfg PGObserverConfig, c Cohort, baseline CohortBaseline) (*CohortVerifier, error)
(v *CohortVerifier) Sample(ctx context.Context) (CohortVerification, error)
```

`CohortSourceRecord.FlowID/Kind` 为 string；Kind 仅 simulation/supplier/ticket。CustomerID、ProductID、SupplierAccountID、BindingID、OrderID、InvoiceID、OrderItemID、ServiceID、ProvisionJobID、TicketID、EventID、StaffID、DepartmentID、EmailDeliveryID、EmailOutboxID、SMSDeliveryID、SMSOutboxID 均 int64；AcceptedAtUTC 为 time.Time。没有关联的 ID 必须为0。sealer 冻结270 simulation +30 supplier（10账户各3）采购及150 ticket，映射300 create job +150 email +150 SMS。150票客户、员工、部门各不重复，客户可以同时拥有采购。禁止同表实体 ID 或 flow 重复、跨渠道重用 delivery、采购/工单混杂字段、客户与员工身份重叠。Sealer 和 Sources 均复制来源切片。

baseline 在第一笔来源创建前采集：CapturedAtUTC；Customers map[int64]CohortCustomerBaseline，字段 BalanceCents int64、Ledger []CohortLedgerCheckpoint（ID/DeltaCents/BalanceAfter int64，按ID升序完整 checkpoint）；Products map[int64]CohortProductBaseline，字段 Stock *int64（nil 无限库存）、Services map[int64]bool（全量既有serviceID -> inventory_released，空map也必须非nil）。构造器深复制所有 slice/map/指针，并要求 baseline 精确覆盖来源客户和商品、来源 service 不属于既有集合、最新 ledger balance_after 与 opening balance 一致。

Sample 每次使用单个 RepeatableRead + ReadOnly 事务，复用 PGObserver 的显式授权配置检查，不在构造器连接；cfg.SupplierAccountIDs 必须精确等于来源中的10个账户集合。实际会话每次核验数据库名符合 `_test`、PG18、精确 search_path/current_schema、loopback server address，首个 schema OID 冻结后不许替换。上下文及 server statement/lock timeout 都受 cfg.Timeout（1ms至5s）约束，cfg.MaxSamples（1至1000）限制采样数。查询标识符只由固定表名及验证后的schema组成，ID通过绑定参数传入；不查询正文、密钥、密文响应或DSN。

每笔采购独立检查 order/invoice/item/service/create-job 的 ID 与 customer/product/order/unit 关系、1000分、quantity1、paid_at、真实状态、唯一订单item/service/create-job。每账单恰好一条同客户-1000分余额payment，任何其他invoice ledger、在线payment记录、credit_invoice_payments、external_refunds都会失败。逐客户验证完整旧 ledger 未删改、新 ledger 只能来自该客户本cohort付款、每笔 balance_after 连续、当前balance准确、credit limit/used=0。credit通过LEFT JOIN读取；依产品合同，缺可选credit account行表示无信用额度，明确按limit/used=0处理，此已知空状态不等于数据库读失败。逐商品验证既有service集合与新增cohort服务集合完整，库存只扣新增单位并加既有服务首次合法release；无限库存保持NULL。合法非create的periodic/sync job不影响cohort分母。

每张ticket独立检查customer/department/customer_created event/actor、1 event/2 deliveries、唯一staff的双渠道queued决策和准确outbox关联；delivery的用户/偏好版本/verified身份、当前地址与outbox recipient一致，不能拿另一人的消息冒充。再检查每渠道150不同收件身份及150不同规范地址。生产delivery outcome终态仍为queued，真正发送完成由outbox sent证明。SMS sent还必须有同outbox/attempt的唯一accepted数据库证据，HTTP2xx及lease/证据/发送时间有序。没有臆造email对称证据表。

这是首轮无故障profile：job终态succeeded/outbox终态sent，各1次attempt；pending/running/sending合法时 Complete=false，终态要求解除lease并具有完成时间；消息TTL为7天（容忍应用时钟和事务时间1分钟差），过期/重试/failed/waiting_verification不能变成成功。MaxAttempts仅email/SMS存在，provision观察为nil。

结果Status为PENDING/PASS/FAIL；PASS只表示本冻结cohort完成，不表示全局后台队列为空。SQL/identity/不变量/预算/重叠采样错误永久latch FAIL，后续拒绝重采；FailureStage只给begin/identity/purchase/ticket/finance/inventory等固定脱敏阶段码，不外泄原始SQL/driver错误。不可用 Complete、ObservedAt、InvariantsVerified 为nil，Tasks为nil。正常pending不会latch。全局background/periodic需由独立PGObserver观察，不能从cohort PASS推导零队列或容量。

来源声明即使能与真实数据库全部对上，也不能单独证明来源是产品HTTP API；source fixture必须另给真实请求与实体映射证据。SMTP/SMS持久协议journal及supplier create/pay逐job映射、hash还需controller独立核对；数据库sent及SMS证据不替代这些协议层证明，也不声称exactly-once。fixture sealer/verifier没有产品HTTP入口，不修改任何生产状态。

专项检查：`go test ./internal/g09fixture -run '^TestCohortPure' -count=1`。测试覆盖缺失/额外source、保持总数的重复实体、同invoice不同owner、supplier分布抵消、双delivery/部门/员工重用、深复制、非法时间/终态、BeginTx本地拒绝后的latch、重叠状态latch和无连接预算拒绝。网络DialFunc为本地拒绝桩，不建立连接。
