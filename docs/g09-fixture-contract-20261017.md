# G09 真实 API、真实 worker 容量夹具实施契约

状态：设计，未实现、未执行、未验收。源码基线 `02706ad`；参考 `29e9d7c:docs/g09-capacity-drain-plan-20261017.md`，该计划文件不在本分支基线，不能声称已随基线存在。本轮只读源码及编写本文，不运行 PG、迁移、构建、负载或服务。本契约供后续实现独立 opt-in fixture 与 Node controller；下面新增动作名不是现存产品 API。

## 固定首轮口径

沿用原计划2分钟预热、10分钟稳态、3分钟峰值、最多10分钟burst排空，以及原读写速率/延迟门槛；在峰值结束与排空之间**新增独立最多2分钟cohort准备阶段**，最大计时长度27分钟，启动/seed另计，不能把准备流量藏入峰值2事务/s。负载库接口改用 `maxOperations`，不能把业务事务数写成 HTTP 请求数。首轮尚未实测，本节是执行前修订。

**首轮验证有限 burst cohort 排空及正常周期背景有界，不要求全局队列永久为零。** 冻结 cohort 为 300 个 provision create job、150 个 email outbox、150 个 SMS outbox，合计 600 个预期成功任务。所有任务必须保留来源和 ID，全部进入成功终态；后台 supplier periodic sync 继续正常运行，独立统计创建/执行速率、最老 ready 年龄与全局积压。不得删除/终止成功服务、改生产 scheduler 或重写时间来制造零队列。完整系统停写后永久零队列需要独立停止调度来源能力，当前没有，不在本轮宣称完成。

原计划最老 ready ≤15 分钟、停止新 burst 后10分钟成功排空、跨两个真实 minute tick 副作用不变保留。固定全局背景门槛：drain 最后两个完整 minute 窗口 ready+running/sending 每次采样≤100；pending 且 available_at 在未来的总量≤100；最老 ready≤15分钟；无新增非指定 waiting_manual/waiting_verification/dead_letter/failed/expired。阈值必须进入最终 profile，不能运行后再改。cohort 延迟任务不能用背景门槛豁免；600 个任务都必须成功。供应商账户竞争的30秒延迟按真实时间计入。

正常写负载仍40%付款/回调、30%本地续费、20%采购、10%工单；明确把20%采购再分成50%本地 simulation、50%供应商 loopback。未知写是另1个 supplier create 负例，不计600预期成功及SLO分母，单独供应商账户，保持 waiting_verification/租约冻结，不自动重发或远端退款删除。

## 可复用代码和不能复用的证明

- `internal/httpapi/integration_test.go:testServerConfig/user/product`：随机 schema、真实 Handler、登录和会话。`product` 直接建 simulation 商品，只能算 seed；`user` 直接建账号可用于预建会话，不能称注册吞吐。
- `internal/httpapi/local_renewal_external_refund_test.go`：真实 cart→订单→余额付款→本地交付→renew→epay签名结算及逐实体断言。其 seed 直接调 `ClaimProvisionJob/ProcessProvisionJob`，测量期不能借此绕过真实 worker。
- `internal/httpapi/supplier_worker_acceptance_test.go`：真实 cmd/worker、显式带 search_path 的 DSN、白名单环境、TERM/Wait、两 worker。原测试自行 build 且有特定场景，不能直接当 release candidate 容量入口。
- `internal/httpapi/suppliers_test.go:supplierSetup` 等 supplier fixture、`notification_worker_sigkill_test.go`：loopback 协议计数/未知响应/租约围栏模式；强杀用 SQL 提前 lease/available_at 的部分不得进入计时排空。
- `internal/httpapi/ticket_notifications_browser_test.go` 内自建 goroutine Claim/Complete 的 sink 不能复用为容量 worker；只复用业务收件设置/来源关联。容量必须实际 SMTP/SMS 网络协议，由 cmd/worker Claim/Send/Complete。
- `internal/store/release_runtime_fixture_test.go` 的 action 白名单及 candidate 脚本用于控制协议参考；原财务不变断言、禁通知约束、短超时不可原封不动照搬。

## 数据和资金预算

首轮1000 customer、2000已本地完成服务、测量前共10000账单（1000张quantity=2的新购已付单、8000张历史未付单、1000张业务待付单）；另150个部门、各1个部门范围的通知员工、2个财务员工、1个bootstrap super_admin、10个正常供应商账户及1个unknown专户。员工不计入1000客户。历史基数不计API吞吐，不伪造 succeeded payment/effect/ledger。8000历史账单为明确标记的未支付手工历史分页数据，due_at设未来30天，排除所有 worker 候选；逐张通过`POST /api/v1/admin/invoices {"user_id":U,"due_at":"RFC3339-now+30d","items":[{"description":"g09-history-runId-N","amount_cents":1000}]}`创建（`invoices.manage`员工、真实session/CSRF/key），每客户8张，另每客户1张同结构的1000分manual业务待付单，放入payable池；此准备耗时单列，不能直插10000已付单冒充真实结算。

种子本地商品 monthly=1000分、setup=0、无promo、有限stock=20000；2000基线服务由真实订单/付款及生产开通函数准备，准备过程单列。每客户通过 `POST /api/v1/admin/customers/{id}/balance` 加入100000分，留真实账本：总授信余额100000000分（100万元），不是现金收入。初始每客户2服务耗2000分，测量前余额98000分，历史手工账单不付款。credit limit/used均0，不使用信用；后续可单独固定 credit profile，不临时改变资金池。

最多业务事务：预热24+稳态600+峰值360=984；cohort采购300，未知采购1，额外退款保护哨兵最多4笔。即保守每一事务都新购或续费，最大新增消费1289000分，远低于资金预算；controller仍逐客户限额，单客户测量写≤50笔且每笔≤1000分，不能用总额掩盖单客超支。商品总stock在seed前20000，2000基线+最多1289新增服务有余量；不得运行中补款/补库存。供应商fixture配置与本站商品价格独立，上游报价也固定1000分，按商户账户维度计create/pay；本轮无真实外部资金。

热点固定前200客户承担20%已计划操作，其余800均摊80%；每客户cart链只能有1个在途事务，同一service只允许1个在途续费。某客户正在忙不能静默改分配其他客户或变闭环，记controller allocation_drop为失败；预分配足够独立客户/service槽避免冲突。10000账单分页至少覆盖offset0/5000/9975（员工跨客户页；客户页仅本客10张，不用客户offset5000伪装中段），limit25，返回ID和总量校验，不用不存在的分页路由。

## seed实施顺序与协议夹具

1. 独占PG18 `_test`库中的随机schema，完成匹配候选迁移。只此阶段允许迁移；记录checksum。150部门/员工范围、1000客户/加密密码和明确verified合成地址由fixture SQL准备（复用user helper行为），不得说成已有批量注册API；创建真实sessions应通过登录，不直插session。
2. 本地商品通过`POST /api/v1/admin/products`：`{"name":"g09-local","slug":"g09-run-local","billing_cycle":"monthly","price_cents":1000,"setup_fee_cents":0,"stock":20000,"active":true,"provision_mode":"simulation","approval_required":false}`，需products.view/manage。seed可选生产helper直接建商品，但必须在manifest标seed-only。每客quantity2 cart/order/pay生成1000已付单/2000服务；计时前可由生产store Claim/Process完成seed，并记录没有真实worker吞吐证明。
3. 10+1 supplier账户用`POST /api/v1/admin/suppliers {"name":"g09-N","provider":"zjmf_finance_v1","base_url":"LOOPBACK","username":"PRIVATE","password":"PRIVATE","active":true}`；provider已核对`internal/supplier/types.go:ProviderZJMF`与`suppliers_test.go:206`。catalog/import链为`POST /api/v1/admin/suppliers/{id}/catalog {}`→GET产品详情→`POST /api/v1/admin/suppliers/{id}/import`，body严格使用`SupplierImportInput`的upstream_product_id/expected_supplier_version/expected_upstream_revision/name/slug/billing_cycle/monthly price1000/setup0/stock20000/active/config_options/custom_fields/maximum_cost_cents1000；required配置来自真实loopback目录响应。记录productId→supplierId固定映射；不能将任意产品SQL改supplier冒充采购接入。
4. SMTP sink需真正支持EHLO、MAIL FROM、RCPT TO、DATA、QUIT及逐封250回应，测试配置`tls_mode=none`且仅loopback；不跳过worker的ResolveSMTP/NewMailProvider/Send。SMS使用现`generic_http_json`协议，接收JSON `{recipient,message,kind}`，验证Authorization Bearer及JSON后持久记录，再回200；`genericHTTPSMS.Send`成功契约就是HTTP 2xx，非2xx用于注入失败，不杜撰响应body状态码协议。渠道密钥加密由现settings/channel生产方法生成，不直接写明文到密文字段；100%目的地址为fixture合成身份。
5. 准备10000账单、sessions、偏好及baseline，seed队列全部完成；之后测量不直写业务表、不调用Claim/Complete/Recover代替worker。所有source均有实体注册表。创建supplier正常服务只在测量及cohort链发生，避免seed worker提前启动每日锁或隐藏周期背景。

## 真实请求链（路径均已在 server.go 注册）

除显式支付回调外，写JSON `Content-Type: application/json`、`Origin=manifest.publicOrigin`、`X-Requested-With: zjmfgo`、Cookie `zjmfgo_session=...`；GET也带所属session。不能复用默认3088 Origin到新端口。每次 mutation key=`g09-{runId}-{flowId}-{step}`，保存原payload/hash，超时未知不自动换版本重发。`server.go:sessionAuth/auth` 的CSRF分支以真实实现为准，不声称另有CSRF token端点。预先真实登录获取session，计时前验证有效期覆盖整轮；不在稳态高频bcrypt/login。

| flow / unit | 精确调用与请求体 | 权限 / 完成定义 |
| --- | --- | --- |
| read / http_request | `GET /api/v1/catalog/page?limit=25&offset=0`；`GET /api/v1/invoices/page?limit=25&offset=N`；`GET /api/v1/services/{id}`；`GET /api/v1/admin/invoices/page?...`；`GET /api/v1/admin/jobs/queue?...`；另Web HTML/静态资源 | 匿名catalog；客户仅自有资源；员工 `invoices.view` / `jobs.view`（admin_permissions.go已映射，不默授super）。Web静态/动态分别计量。 |
| balance_pay / business_transaction | 选seed提供的本客未付、非历史分页账单；GET账单核对id/user_id/amount_cents/status/version；`POST /api/v1/invoices/{id}/pay {"expected_version":V}` | customer；manual账单强制版本号。金额必须在付款前核对且携版本写入，不能扣超额后才发现回执不匹配。201/幂等200只表示API接纳，SQL验证paid、唯一账本及order/job；历史分页账单不参与付款。 |
| external_settle / business_transaction | `POST /api/v1/invoices/{id}/payments {"provider":"epay_alipay","expected_version":V}`；解析checkout_url到loopback epay fixture，按现epayNotification规则签名；`POST /api/v1/payments/epay_alipay/webhook`（epay_test.go:105已核准） | customer创建intent；回调独立MD5签名、form编码，成功body为`success`；重复同一回调另计HTTP。原payment保持succeeded、invoice paid_at一次、不得创建第二effect。 |
| local_renew / business_transaction | `POST /api/v1/services/{id}/renew {}` 得invoice_id；GET账单核对归属/金额/状态/版本；`POST /api/v1/invoices/{id}/pay {"expected_version":V}`；GET服务并登记实体ID | customer；当前服务active/suspended，simulation、原due未来、无配置/未付续费。无promo时before_used=after_used=0。至少一次本地effect且due真正延期。 |
| purchase_local / business_transaction | GET cart得version；`POST /api/v1/cart/items {"version":V,"product_id":P,"quantity":1,"billing_cycle":"monthly","option_ids":[],"option_quantities":{},"purchase_fields":{}}`；GET cart取最新version/item；`POST /api/v1/orders {"cart_version":V2,"item_ids":[I]}`；GET账单核验后`POST /api/v1/invoices/{id}/pay {"expected_version":V}` | customer；商品simulation、approval_required=false。HTTP接纳与worker开通成功分别计；不能controller手动Complete。cart不支持Idempotency-Key回放，未知结果必须冻结原链。 |
| purchase_supplier / business_transaction | 同cart/order/pay链，商品为已绑定且公开的supplier商品，必填upstream配置来自冻结manifest，不传臆造空配置 | customer；10个正常账户，逐job记录create/pay调用。未知专户另分配单个job；禁止自动解除冻结。 |
| ticket / business_transaction | `POST /api/v1/tickets {"subject":"g09-{runId}-{flowId}","body":"fixture public body","priority":"normal","department_id":D,"idempotency_key":K}`；可单列回复 `POST /api/v1/tickets/{id}/replies {"body":"fixture reply","version":V,"idempotency_key":K}` | customer创建；回复客户或 `tickets.manage` 员工。创建201、回复200；以ticket_notification_events/deliveries绑定outbox，不以HTTP成功猜消息数。 |
| notification_preferences（seed） | `PATCH /api/v1/me/ticket-notification-preferences {"email_enabled":true,"sms_enabled":true,"expected_version":V}` | 该通知员工自己的session；SQL验证地址/身份只属seed，不能冒充客户验证API吞吐。 |

支付回调签名/字段复用`epay_test.go:epayNotification`，不得猜算法；任何新增fixture控制动作都不暴露到产品API。真实授权/owner/401/403/409负例单列确定性哨兵，不计正常SLO成功。混合事务任一步非2xx均失败，不能用最后GET200覆盖先前409。

## 600任务的有限真实入队

峰值前三分钟结束后停止正常业务写、TERM并wait两worker，controller在独立最多120秒cohort准备窗口创建300笔quantity1采购：270 simulation，30 supplier（10账户各3）；真实API余额支付生成300个create job，记录invoice/order/service/job ID；准备窗口预算120秒，超时就FAIL；普通读探针保持10/s，cohort准备用单独计数和延迟，不并入三分钟峰值或steady门槛。恢复worker后才计cohort drain开始；所有已接纳来源的最后commit时刻也记录，报告同时列准备耗时/恢复耗时。

另150个客户各对唯一部门建1张ticket。为保证每事件恰好1mail+1SMS，各部门只有对应staff具有该部门 `tickets.manage`，员工自己的email/phone已验证且开启双渠道偏好；150个staff使用150不同合法E.164合成手机号、150不同`.test`邮箱，不把150条消息发同一staff/phone。bootstrap super_admin及其他员工必须无有效订阅/未验证地址，且确认无额外合法已验证recipient；不能假定部门范围排除super_admin。contacts为空。只启用ticket_customer_created对应双渠道模板，模板正文带runId及`{{ticket_no}}`，SMTP subject也带ticket_no，短信正文≤4096。channel_instances各只1个enabled、路由顺序固定；每通知TTL为7天（`ticket_notifications.go:204`），不使用SMS test endpoint（5分钟TTL）或SMTP test endpoint替代；后者QueueSMTPTest未显式expires_at，不能误称同为5分钟TTL。

seed/pilot必须先实际创建1ticket，通过生产SQL核对2个queued delivery、对应1mail/1SMS及不同身份，然后新schema正式run。pilot不足/多出则不得称150API消息入队成立。可选择另一个明确profile：fixture调用现生产 `QueueSMSWithID` + 事务写入email_outbox的现有生产入队链，记录返回ID和来源，标为worker-only cohort；API票据吞吐分母不得纳入这部分。不存在通用公开“批量发150通知”API，也没有已实现通用QueueEmail公开方法；不得发明。首轮优先真实ticket链。

## 正常timer与背景

`cmd/worker/main.go` 每2秒串行最多10 SMS→10 email→20provision，网络占时可延长整个loop；付款对账10秒独立，通知/暂停分钟循环，auto-renew启动立即且之后分钟循环并每日成功锁。两个worker各Store MaxConns10，不改生产并发常数。

8000历史账单及1000业务待付单due未来；local服务due未来30天、auto_renew=false；expiry/billing通知模板禁用，只ticket指定模板开启，正常timer仍跑且无新通知候选。如此通知源真正有限。首轮manual续费不需篡改due。若另测真实auto-renew，必须独立新schema在首个worker启动前完成seed与arm；不能在已成功空跑的每日锁之后靠SQL清scheduler_runs触发，更不能声称提供现存HTTP修改next_due_at接口。可复用生产store准备服务并在**计时前**显式fixture-only SQL布置due，公开边界，不计API能力。

supplier `EnqueuePeriodicSupplierSyncs` 无独立enabled API：活跃服务每5分钟可重置同idempotency key的已完成sync job。保留正常循环与活跃服务；所有periodic job及其每次attempt计背景，不放进冻结600 IDs。固定30个供应商cohort服务+测量期有限新增supplier服务，使候选规模有上界。由于periodic同一job ID复用、attempt重置，不能用ID集合推出“只执行一次”；snapshot采样updated_at/attempts/状态加loopback请求序号重建cycle。未提供持久attempt-history全链路时，报告证据缺口，不伪造exactly-once。

通知退避真实1/2/4/8/16/32分钟、发送lease5分钟；10分钟drain容纳不了任意连续失败，本轮正常fixture成功、可另设每渠道最多1个first-attempt明确失败（1分钟退避）有界故障profile，不能SQL提前available_at。supplier账户忙30秒、未知写等待核验原样保留。无论何种重试，不把expired/dead_letter当成功排空。

## 私有manifest与控制协议（待实现）

0600 O_EXCL、绝对私有run目录、父目录0700，拒绝symlink及已有文件。公开报告只发布脱敏副本及文件hash，不索引私有cookies/密钥/DSN/phone/address。形状冻结如下：

```json
{
 "schemaVersion":1,"runId":"...","sourceCommit":"...","apiOrigin":"http://127.0.0.1:PORT","publicOrigin":"http://localhost:WEBPORT",
 "database":{"schema":"g09_RANDOM","privateDsnRef":"db.env"},
 "candidate":{"manifestPath":"ABS","sha256":"...","apiBinarySha256":"...","workerBinarySha256":"..."},
 "accounts":[{"id":1,"role":"customer","cookie":"PRIVATE","cohort":"normal","budgetCents":98000}],
 "resources":{"productIds":[],"supplierAccountIds":[],"serviceIds":[],"historyInvoiceIds":[],"payableInvoiceIds":[],"departments":[]},
 "flows":[{"id":"...","category":"local_renew","customerId":1,"serviceId":1,"maxUses":50}],
 "cohorts":{"provision":[],"email":[],"sms":[],"expectedUnknown":[]},
 "control":{"requestFile":"ABS","responseFile":"ABS","finishFile":"ABS","deadlineSeconds":2400},
 "protocolFixtures":{"smtpOrigin":"loopback","smsOrigin":"loopback","supplierOrigins":[],"epayOrigin":"loopback"}
}
```

cohort数组在真实创建后写入 `{id,sourceKind,sourceId,acceptedAt,expectedStatus,flowId}`；只有全部600逐ID关联完成才seal；启动前manifest不冒称已有任务。manifest更新用原目录同用户临时0600+atomic rename；公开digest注明版本/时刻。expectedUnknown独立record不冒用正常cohort。

新增fixture控制动作仅白名单：`prepare`（一次，仅seed及schema）、`start-workers`（owned slots0/1且不能已有PID）、`stop-workers`（TERM已核验PID、wait，记录exit，禁止任意PID/信号）、`snapshot`（只读已允许投影）、`seal-cohorts`（检查ID/来源/数量，不写业务状态）、`verify-drain`（只读，不能recover/complete）、`finish` / `abort`（owned进程、listener/schema清理）。请求含runId、递增sequence、action、slot（只有worker动作）；不接受shell、SQL、路径、环境变量、外部URL或任意signal参数。未知动作即拒绝。fixture控制通道是私有文件/socket，不新建生产admin endpoint。

正常峰值暂停先stop-workers确认退出，再定义峰值首60秒窗口，之后恢复worker直至峰值结束；cohort准备阶段再次TERM是另一次明确的停启，单独记录。不能SIGSTOP在持锁事务里。每worker cwd私有空目录，环境白名单仅DATABASE_URL（带专属search_path）、PUBLIC_ORIGIN、加密key和loopback渠道配置；不继承.env/.mimosa。异常不能自然TERM按预算KILL owned PID并标FAIL，不把强杀当正常恢复PASS。finish必须TERM停止worker后再drop**唯一**schema，核对端口/PID/cwd/exe及退出码；共享PG、常驻8088/3088/worker不动。

## Node库与controller分工

与负载库代理协商：`runG09Load({profile,perform,record,clock?,signal?})`；profile流必须 `{category,unit:"http_request"|"business_transaction",ratePerSecond}`，同category跨phase unit不变；`maxOperations/maxConcurrency/requestTimeoutMs/maxScheduleLagMs/drainTimeoutMs` 有界。perform接`requestId/streamSequence/phase/category/attempt:1/plannedAt/startedAt/signal`，返回`{status,accepted?,succeeded?}`。任何非2xx都不成功，混合事务controller遇子请求失败返回succeeded=false；库统计work units，controller另流式记录每个实际HTTP子请求与实体关联。

record started/finished/dropped只含脱敏meta，不回传headers/body。直方图p95/p99是上界，不能冒称精确样本分位。库drain仅清inflight；controller随后按冻结600 IDs查询业务drain及独立背景指标。超并发/迟到/allocation忙均显记drop，不补发、不自动重试；未知HTTP结果保留原payload/key用于确定性恢复哨兵，不能自动重买。fixture controller不可将bootstrap/cohort准备算steady throughput。

## 可执行SQL核对合同

所有SQL连接到唯一_test库且startup search_path精确等于manifest.schema；先核验current_database/current_schema/server_version_num。参数`$1`等通过pgx绑定typed ID数组，不拼接用户输入。schema名只能fixture生成并标识符转义。baseline保存每客户余额及全量ledger ID/净额、每product stock、每service状态/due/used/gen、各表计数与cohort注册表；下面差额需读取baseline JSON，而不是新建/修改业务表。

```sql
-- 每客户：由baseline计算 opening_balance + (closing_delta-opening_delta)=closing_balance。
SELECT u.id,u.balance_cents,coalesce(sum(l.delta_cents),0)::bigint AS ledger_delta
FROM users u LEFT JOIN balance_ledger l ON l.user_id=u.id
WHERE u.id=ANY($1::bigint[]) GROUP BY u.id,u.balance_cents ORDER BY u.id;
-- 每账单的已接纳付款：余额付款仅1个负账本，external仅1个succeeded，不能抵消错客。
SELECT i.id,i.user_id,i.status,i.amount_cents,i.paid_at,
 (SELECT count(*) FROM balance_ledger l WHERE l.reference_type='invoice' AND l.reference_id=i.id AND l.entry_type='payment') balance_debits,
 (SELECT coalesce(sum(-l.delta_cents),0) FROM balance_ledger l WHERE l.reference_type='invoice' AND l.reference_id=i.id AND l.entry_type='payment') balance_paid,
 (SELECT count(*) FROM payment_transactions p WHERE p.invoice_id=i.id AND p.status='succeeded') external_receipts
FROM invoices i WHERE i.id=ANY($1::bigint[]) ORDER BY i.id;
-- 库存：closing stock=opening stock-全部新接纳采购单位数+合法首次release单位数，逐product比较。
SELECT p.id,p.stock,count(s.id) FILTER(WHERE NOT s.inventory_released) retained_units,
 count(s.id) FILTER(WHERE s.inventory_released) released_units
FROM products p LEFT JOIN services s ON s.product_id=p.id
WHERE p.id=ANY($1::bigint[]) GROUP BY p.id,p.stock ORDER BY p.id;
-- 每订单及unit，不是只有总服务数相等。
SELECT oi.order_id,oi.id,oi.product_id,oi.quantity,count(s.id) units,
 count(DISTINCT s.unit_number) distinct_units,min(s.unit_number),max(s.unit_number)
FROM order_items oi LEFT JOIN services s ON s.order_item_id=oi.id
WHERE oi.order_id=ANY($1::bigint[]) GROUP BY oi.id ORDER BY oi.order_id,oi.id;
-- local effect：每已付renew恰好1effect，before/after链接、used不倒置、当前末effect与服务吻合。
SELECT ii.invoice_id,ii.service_id,i.status,e.id,e.before_due,e.after_due,e.before_used,e.after_used,
 s.next_due_at,s.renewal_discounts_used,s.renewal_generation,s.auto_renew
FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id JOIN services s ON s.id=ii.service_id
LEFT JOIN service_renewal_effects e ON e.invoice_id=i.id
WHERE ii.item_type='renew' AND i.id=ANY($1::bigint[]) ORDER BY ii.service_id,e.id;
-- 明确任务集合状态。email/sms用相同字段独立查询，预期终态sent；provision预期succeeded。
SELECT id,service_id,action,status,attempts,available_at,locked_at,completed_at
FROM provision_jobs WHERE id=ANY($1::bigint[]) ORDER BY id;
SELECT id,status,attempts,max_attempts,available_at,locked_at,expires_at
FROM email_outbox WHERE id=ANY($1::bigint[]) ORDER BY id;
SELECT id,status,attempts,max_attempts,available_at,locked_at,expires_at
FROM sms_outbox WHERE id=ANY($1::bigint[]) ORDER BY id;
-- ticket真实来源，禁止漏掉skipped理由/把queued当sent。
SELECT e.id,e.ticket_id,e.actor_id,d.id delivery_id,d.channel,d.outcome,d.reason,
 d.email_outbox_id,d.sms_outbox_id
FROM ticket_notification_events e JOIN ticket_notification_deliveries d ON d.event_id=e.id
WHERE e.ticket_id=ANY($1::bigint[]) ORDER BY e.id,d.id;
-- 背景/全局采样示例；email/SMS分别pending/sending，不能并错状态名。
SELECT status,count(*),count(*) FILTER(WHERE status='pending' AND available_at<=clock_timestamp()) ready,
 count(*) FILTER(WHERE status='pending' AND available_at>clock_timestamp()) future,
 max(extract(epoch FROM (clock_timestamp()-created_at))) FILTER(WHERE status='pending' AND available_at<=clock_timestamp()) oldest_ready_seconds
FROM provision_jobs GROUP BY status;
SELECT id,running_job_id FROM supplier_accounts WHERE id=ANY($1::bigint[]) ORDER BY id;
SELECT user_id,limit_cents,used_cents,version FROM customer_credit_accounts WHERE user_id=ANY($1::bigint[]) ORDER BY user_id;
```

SQL输出逐invoice验证`paid_at`保留及重复callback不重生效，余额ledger的user_id必须匹配invoice.user_id；没有本地effect的旧历史单不要求补造。无promo首轮renew used保持0，有效最新effect.after_due=service.next_due_at；同service相邻effect.before_due=前effect.after_due。退款哨兵另外查询service_renewal_reversals/external_refund_renewal_holds，要求原due/used恢复且auto关闭/gen+1，外部退款不增余额。credit所有used仍0。

SMS实际送达关联查询`sms_delivery_evidence(outbox_id,attempts,locked_at,provider,http_status,outcome,created_at)`，不选response_encrypted，不能杜撰email对称delivery-evidence表。SMTP接收器持久记录接受DATA的`ticket_no/recipientHash/messageHash/sequence`；SMS接收器记录解析后的`ticket_no/recipientHash/requestHash/sequence`，实际body/phone只私有存储。正常无故障cohort各1次协议接受，SMS持久证据与job匹配；通知只保证至少一次尝试，注入丢响应允许重复且必须解释，不能称exactly-once。supplier create/pay计数按账户+service/job私有映射逐个比对；无持久外部job-id header时用上游返回ID及fixture请求序号，不臆造关联字段。

## release candidate、普通用户与证据边界

最终实现后先合入所有fixture/controller/library，再固定**一个clean提交**，用release builder得到摘要一致api/worker/web，真实candidate产物只构建一次；源test helper可负责随机schema和协议服务，但必须exec该candidate.worker与candidate.api，不能仍用httptest.New(db,...)后称candidate容量。若API仍是test Handler，则明确“source API容量”，不可满足最终release整合门槛。

普通非root UID运行API/两个worker/Web/Nginx/controller/sinks，独占namespace，网络仅loopback+授权测试PG loopback（私有netns需明确PG通道映射）；不为方便把宿主全网放回。采用既有release runtime namespace/路径验证，不假造现成G09入口。私有目录均归该UID；只读candidate root、单独可写run root、显式PG search_path；候选hash/PID exe/cwd/env白名单、listener、UID与namespace inode写公开脱敏证据。bootstrap迁移只在授权隔离阶段一次完成，负载阶段不迁移、不build。

公开输出profile/候选摘要/requests.jsonl/队列与资源采样/逐实体invariants/协议计数/summary/cleanup/index；SQL query正文、DSN、cookies、密钥、手机号/邮箱不进公开日志。每2秒采样API/worker/Web/代理逐PID RSS/CPU/FD/IO和PG连接/锁/deadlock/WAL（不收query正文）；不能声称已有pprof/Prometheus。硬停止条件沿原计划：资金错账、死锁、越界网络/进程/schema、资源预算越线即停新写保留FAIL；不能清unknown挽救指标。

实测前尚缺：新opt-in600任务fixture、SMTP真实协议sink、冻结cohort manifest/快照协议、candidate普通用户namespace控制器、请求流分配器与协议实体关联、后台periodic cycle审计、固定全局背景阈值采样。现有API无批量建1000客户/改服务due/停止单类scheduler，seed和有限自动续费准备必须明确fixture-only；不是产品已有能力。本文件和负载库单测均不证明容量、真实通知收件、真实商户出款或正式HTTPS/生产SLO。
