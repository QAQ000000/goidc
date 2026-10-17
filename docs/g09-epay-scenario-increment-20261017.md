# G09 易支付通知业务场景增量

状态：独立源码实现及Node协议guard通过；真实PG18/Go API→Node闭环尚未执行。不是商户真实支付、查询、关单、退款、浏览器支付页、完整候选或容量证明。

`createG09EpaySettlementExecutor` 供后续controller执行固定分配的external_settle链：真实客户身份、付款前账单归属/金额/未付/版本、创建intent，再校验签名checkout的精确商户origin、PID、类型、金额、付款ID和回调/返回目标。它不访问checkout_url，不向商户发起请求；生成与标准易支付兼容的合成TRADE_SUCCESS通知并直接发送本地Go API。两次通知是预先指定的同form原始成功通知和重复通知，不是出错重试。第一次不明确成功时不发送第二次。

每流最多1000分，每客户最多50笔，最多1000条冻结flow，账户/票据独占，whole-flow timeout≤30秒，已尝试intent后任何错误冻结客户，禁止换键再付。callback只投明确IPv4 loopback API、禁重定向，body上限64bytes，必须HTTP200且精确`success`，回读paid/paid_at。原表单在任何发送前同步私有持久化，公开记录仅hash/状态/序号，不含cookie、key、checkout、签名或交易号。该helper要求调用方落实0600/fsync记录器、私有目录、namespace及SQL对账，本身不能证明这些外部约束。

`TestG09EpaySourceScenarioSmoke` 仅在`ZJMFGO_G09_EPAY_SOURCE_SMOKE=1`执行：授权loopback PG18_test随机schema中seed两张真实管理员人工未付账单、普通客户真实session，实际Node场景分别走支付宝/微信intent和重复签名通知。计划核对2个paid账单/唯一payment/event/audit、余额和无交付任务、12次实际HTTP与4份私有原请求（2intent+2callback表单）。本地merchant拒绝所有访问且必须保持零请求；不冒充访问过真实支付页。

默认no-DSN该测试明确SKIP，仅证明编译/opt-in边界。10项Node guard覆盖精确签名/重复参数/错目标/身份归属/版本、原通知保留、重复及未知冻结、无私有持久化不发送、响应/时限预算及停止/并发边界；其合成API依赖不计真实Go/PG集成通过。实际PG结果待单列追加。
