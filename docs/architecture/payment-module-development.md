# 支付模块开发契约

状态：当前生效。业务收款、退款和人工处理口径见[在线支付](../payments.md)与[支付操作手册](../payment-operations-runbook.md)；本文规定编译适配器的接入边界。通用模型见[编译期模块架构](../compiled-module-architecture.md)。

## 宿主与适配器职责

宿主负责登录、客户归属、管理员权限、账单状态及版本、整数分金额、幂等、数据库事务、账本、审计和交付。适配器负责本地付款链接、协议验签和解码、只读查单，以及具有充分远端协议保证的关单/关单查询。适配器不能直接操作账单、余额或订单。

当前生产装配只有 `mock` 和 `epay`，默认模块清单仍为 9 项。第二支付实现只存在于 Go `_test.go`，证明通用链路可以扩展，不会出现在生产目录。

**`Gateway.CreateIntent` 必须纯本地构建付款号和付款链接。** 当前 HTTP 在数据库事务与幂等检查之前调用它，所以不得在工厂或该方法中联网创建、扣款、授权或冻结远端订单。需要远程预下单的渠道须先设计持久命令、固定操作号、发送前围栏及未知结果查询，不能用当前接口直接接入。

## 注册与方式归属

支付包提供 `RegisterFactory` / `MustRegisterFactory`，同时登记描述符、固定支付方式、回调协议及类型化工厂：

```go
payment.RegisterFactory(descriptor, []payment.Method{
    {ID: "example_card", Label: "银行卡"},
}, protocol, func(method string, cfg payment.FactoryConfig) (payment.Gateway, error) {
    // 只读 cfg.Values、cfg.Secret(s)、cfg.PublicOrigin；不联网。
    return newGateway(method, cfg)
})
```

- 描述符的 `ID` 是实现身份；`Method.ID` 是客户选择及交易 `provider`，全局唯一且固定属于一个实现。注册必须拒绝重复/空方式、错类别和空工厂/协议。
- `Method.Label` 是公开展示名。当前只允许内置 `mock` 的 `Simulation=true`；其余模拟协议只能在隔离测试中装配为测试实现。增加任意模拟方式前必须统一报表、充值和数据库资金约束。
- `NewFor(implementation, method, cfg)` 检查方式归属和工厂返回的 `Gateway.Name()`。不能借实例 A 的凭据构建实例 B 的方式。
- `Methods(implementation)` 返回固定声明，宿主逐个通过当前配置构建，过滤未启用或配置不完整的方式；接口继续返回 `name/label/instance_id/simulation`。
- `New` 与 `For` 是旧调用兼容入口；新增代码使用明确实现/方式和通用配置。Epay 的旧 `Payload` 转换仅留在其适配器内。
- 描述符能力是本地协议声明，不能表示网关健康。查询要求声明 `query` 且实现 `QueryGateway`；关单、关单查询还需对应强类型接口。MPAY 标准查单 404 的已知边界仍成立。

实例配置使用普通 `Values` 与命名 `Secrets`，字段、必填项和 schema 由共享描述符验证。凭据只从受控实例配置解密，不进入公开 DTO、错误文字或测试证据。多个密钥不再压入 Epay 的 `key` 单字段。

## 历史交易快照

普通实例的新交易在核心事务中冻结方式、实现、schema、实例身份及并发版本、普通配置、命名凭据和站点来源，使用 v2 envelope；默认 Mock/Epay 设置兼容入口继续写既有快照格式。快照用现有交易引用 AAD 加密。普通实例必须由 `MerchantScopeGateway` 提供稳定、非秘密的商户命名空间，供既有证据式退款登记使用。真实商户 scope 格式为 2–64 字符的小写命名空间、冒号和 64 位十六进制摘要，Mock 固定 `mock:test-only`。商户身份必须在创建时可验证，不能从当前可修改实例恢复。

- 同一幂等键必须绑定相同账单、方式和实例；相同方式的两个实例不能相互重放。
- 新单创建前在配置锁内重新检查实例启用、方式归属、schema 和版本；读取后发生停用或轮换不得保存旧配置的新单。
- 有快照的回调、查询和关单通过原快照构建；当前实例被停用、字段被修改或 singleton 密钥损坏不能替换原交易身份。
- 未知快照版本、未知实现、schema 不兼容或密文损坏均拒绝处理；不能悄悄改用当前凭据。
- 旧 Epay JSON、旧 Mock JSON、加密 `unavailable` 标记和历史无快照读取保持兼容。新迁移不重写旧密文，保留既有退款商户 scope、外部流水身份和关单凭据哈希。
- 历史无快照 Epay 单的商户轮换仍先保存原身份；无法恢复的未终态交易继续阻止轮换。历史已终态单的不可用标记不能解析为新商户。

## 回调协议

`WebhookProtocol` 是编译期声明的纯本地协议：

1. HTTP 宿主限制 body 和 query 各不超过 64KiB。
2. `Parse(CallbackRequest)` 无凭据地提取唯一 `Reference`、待验签 `Body/Signature` 和用于重放检查的 `CanonicalBody`。这些内容全不可信，不能决定金额或状态；持久化的 canonical body 必须是合法 JSON object（不能是数组、字符串或 null）。
3. 宿主按方式和引用读取已有交易快照，用其网关 `VerifySignature`、`DecodeWebhook`；解码后的引用必须等于 locator。金额、账单和状态由核心事务再次匹配。
4. `Webhook.EventID` 标识投递事件，须在同方式的所有商户实例间唯一，建议使用商户 scope 加原事件号；`ExternalTransactionID` 标识实际资金流水，真实渠道的成功或失败回调均必须提供。适配器须按稳定商户命名空间隔离流水，同商户不同方式不能用方式名绕开唯一约束。事件号不能替代独立资金流水号。
5. 核心提交成功后才调用协议 `Reply` 输出成功。冲突和内部失败必须给失败响应，使上游安全重试；日志仅记录受控身份和内部错误。

解析应拒绝重复键、尾随内容、query/body 混合、无效类型和过长字段。签名覆盖原始协议需要的确切字节；规范化只用于业务重放证据，不能替代原字节验签。协议输出不得夹带凭据。

内置兼容要求：Epay 支持 GET query 或 POST form，返回 HTTP200 纯文本 `success`，失败仍按对应 HTTP 状态返回 `fail`；Mock 保留 POST JSON、HMAC-SHA256、`X-Payment-Signature` 和 201/200 JSON 响应，验签使用该交易的原始密钥。

## 查询、过期和关单

方式的查询目录从编译声明获取，手动/worker 查询还需可构建的历史 `QueryGateway`。创建事务仅为实际支持查询的真实方式设置计划。保留 30 秒冷却、1 分钟租约、32 次上限及已有退避；查询完成必须通过原有权限、资源范围和租约围栏。

本地过期、查不到、404、待支付、超时或解析失败都不能证明远端关闭，不能解除付款保护。过期真实交易仍可接受原商户的迟到成功；非查询方式也继续冻结，等待可信证据。

`CloseGateway` 必须返回远端确认的终态，并支持固定操作号。未知结果记录为冻结，禁止自动重发有副作用的关单；后续只读核验使用 `CloseStatusGateway`。真实关闭后收到付款成功，复用既有异常资金事实与独立退款复核流程，不能悄悄结算或删除。Epay 没有因此获得自动关单或原路退款能力。

## 新增实现与验收

1. 在适配器包注册字段、方式、协议和工厂，必要时更新受控装配与预期模块 profile。
2. 使用 API 创建实例，验证双实例及命名密钥隔离；前端继续使用共享字段表单。复杂专用交互需要独立前端开发和重新构建。
3. 验证指定实例创建、跨实例幂等冲突、版本/schema/权限拒绝、轮换和停用后原凭据处理、错误金额/引用/账单、重复事件和流水重用。
4. 验证手动/worker 查询、回调竞态、租约失效、冷却/上限、本地过期继续冻结、关单未知及晚到异常。未支持的能力必须明确拒绝。
5. 验证审计失败全部回滚、旧 Epay/Mock/unavailable 快照、商户 scope、关单凭据哈希和从旧 schema 升级保留。
6. 在隔离 loopback PG18 `_test` 库运行严格支付 runner，要求所有指定测试真实 run/pass、零 skip；运行模块二进制清单、共享渠道兼容及文档门禁。

```bash
node scripts/test-compiled-payments.mjs --unit
ZJMFGO_TEST_DATABASE_URL=... node scripts/test-compiled-payments.mjs --integration
node scripts/check-module-binaries.mjs
node scripts/check-doc-governance.mjs
node scripts/check-doc-links.mjs
```

本地测试适配器及隔离数据库属于 E1；真实页面属于 E2；指定真实商户、买家付款、通知和到账核对属于 E3；生产配置、切换和恢复属于 E4。缺少外部商户验收不能以通用工厂通过替代。迁移 165 将非 Mock 流水统一纳入唯一索引；若遗留数据存在重复外部流水，升级会回滚并拒绝继续，须先核验原始财务事实，不能改写身份绕过约束。升级前备份并部署匹配 schema 的 API/worker；写入新实现交易后回退必须保留能读取其快照的二进制，不能用旧程序或回滚数据抹去在途付款。
