# 供应商模块开发契约

状态：当前生效。供应商交易、生命周期与魔方协议见[供应商协议](../supplier-protocol.md)；本文规定新增编译适配器的账户、字段和运行接入。通用规则见[编译期模块架构](../compiled-module-architecture.md)。

## 宿主与适配器

供应商使用专用 `supplier_accounts`，通过 `supplier.Register(Provider, channel.Descriptor, ProviderFactory)` 注册；不能在 `channel_instances` 新建平行供应商账户。NOKVM 继续使用部署 profile，其资源、NAT、存储和光驱契约不因新增供应商改变。

生产仍只有 `zjmf_finance_v1` 供应商。第二供应商仅在 `_test.go` 装配，证明完整配置与现有业务链路可复用；整体构建清单以[编译模块架构](../compiled-module-architecture.md)为准。

宿主负责身份、权限、资源范围、整数分金额、商品绑定、账单、审计、持久任务与人工核验。适配器负责归一化商品、上游资源状态、报价及受控协议调用；不能直接写本地订单、余额或交付状态。工厂和配置校验只做本地工作，不联网。

## 定义、身份与配置

```go
supplier.Register(supplier.Provider{
    ID: "example_v1", Name: "示例供应商",
    IdentityFields: []string{"realm", "account"},
    Capabilities: supplier.Capabilities{Catalog: true, Product: true, Create: true, Sync: true},
}, channel.Descriptor{
    ID: "example", Name: "示例供应商", Category: channel.Provision,
    InstanceMode: "supplier", SchemaVersion: 1,
    Fields: []channel.Field{
        {Key: "realm", Label: "区域", Type: channel.Text, Required: true},
        {Key: "account", Label: "账户", Type: channel.Text, Required: true},
        {Key: "read_key", Label: "读取密钥", Type: channel.Text, Required: true, Secret: true},
        {Key: "write_key", Label: "操作密钥", Type: channel.Text, Required: true, Secret: true},
    },
}, func(account supplier.Account) (supplier.Adapter, error) {
    // account.Values 与 account.Secrets 包含完整的普通字段与命名凭据。
    return newAdapter(account)
})
```

`Provider.ID` 是持久身份，`Descriptor.ID` 是编译目录身份。能力从 `Provider.Capabilities` 生成，显式描述符能力必须完全一致；运行工厂的能力也必须一致。声明续费须实现 `RenewalAdapter`，声明控制台或重装须实现 `ConsoleAdapter`。暂停、恢复和终止使用共享宿主生命周期工作流；声明能力须实现 `Execute` 并返回可证明的目标状态，不得仅返回接收成功。

`IdentityFields` 必须显式选择真实上游账户的必填普通字段，不能含凭据或可选传输配置。身份哈希不包含凭据、schema 版本和非身份字段；修改超时不能创建一条平行账户来绕过账户租约。提供方可在 `Provider.NormalizeConfig` 注册纯本地普通字段归一化函数，输出会再次通过 schema 检查，用于统一该协议等价的上游身份；它不接收凭据。内置魔方统一主机大小写、默认端口、路径转义与尾部斜杠。URL 别名、共享真实账户或多部署仍需运营保证使用专用上游客户。

普通配置创建后全部冻结，修改来源、区域、账户或其他普通字段须新建不同上游身份的账户；同一身份的停用行仍占用身份。历史行可能尚无身份哈希；新建账户会比较全部历史行，运行和连接测试还会比较其他启用或持有任务租约的历史账户。身份冲突或相关历史身份无法验证时停止派发，须先核验重复账户和在途任务，不能通过修改 URL 写法绕过租约。名称、启用状态与凭据可按并发版本更新。配置 schema 与并发版本分别比较，不做隐式 schema 转换；不兼容实例保留原值，允许安全停用。

后台从 `GET /api/v1/admin/supplier-providers` 的 `descriptor` 渲染字段；`id/name/capabilities` 保持兼容。字段复用公共类型、默认值与服务端约束，未知字段类型阻止保存。复杂新交互仍需前端开发和重新构建。

## 写入、凭据与安全停用

账户 API 继续使用 `active` 与 `expected_version`；新客户端发送 `config`、`secure_config`、`clear_secure_fields`、`schema_version`、`expected_schema_version`。密钥留空或省略表示保留，清除必须明确指定，不能在同一请求中替换并清除同字段。未知字段、普通配置混入密钥、错误类型或不完整的启用配置均拒绝。

迁移 166 添加配置 schema、普通配置、显式凭据格式与逐字段密文，不重加密旧字节。旧魔方 API 的 `base_url/username/password` 仍可用，保持原 `credential_format=0`；新配置协议使用格式 1。格式 1 使用账户 ID 与字段名作为 AES-GCM AAD，不能跨账户或字段替换。旧行采用新协议轮换后可保留原 bytea 供历史诊断，但运行时严格按格式读取，损坏命名密文不能回退旧密钥。

列表和资源 API 设置页只回显合法普通字段、`secure_fields` 存在标记、`schema_version`、`configuration_status` 和 `configuration_errors`，不解密凭据。`valid` 表示本地定义检查通过，不能证明密文可解密或上游健康。

账户保存必须在事务内重查当前会话和权限、并发版本、schema 以及 `running_job_id`，与审计一并提交。缺失实现或 schema 不兼容时使用 `disable_only`，保留全部身份与密文，不构造适配器、不解密，也不能绕过在途或未知任务。

## 任务、服务与协议边界

供应商与支付的凭据语义不同：待派发任务在运行时使用账户当前配置；发送前锁定并重查账户版本，随后持有持久 `running_job_id`。进行中或等待核验期间禁止改凭据、停用或重新定向。凭据轮换不会自动重派发已知或未知写任务。服务和商品仍使用不可变供应商绑定。

上游写操作发送后不自动重试。未知结果保留账户租约，进入 `waiting_verification`；重启恢复只允许纯读取 `sync` 自动恢复，其他动作等待核验。新提供方不得把会写入远端的动作声明成只读。续费继续要求原账单、报价、资源及完整期间的到期日证据；付款确认后须重新读取资源真实状态和详情，不能只返回本地预测期限。宿主仅以本次可信续费来源接受到期暂停的即时或延迟恢复，人工/策略暂停保持保护，详见[续费协议](../supplier-protocol.md#legacy-renewal-integration-2026-10-13)。

魔方 `/api/host/sync` 是其专用协议，只处理不可变快照绑定到魔方提供方的服务。新增供应商默认使用 `Sync` 轮询，不能借用该回调；若需要回调，须单独定义签名覆盖、引用定位、重放、旧服务凭据、事务和状态合并，再接入宿主。

服务详情的 `supplier_capabilities` 仍是无需解密的编译能力信息。实际动作另查当前账户、权限、资源、状态和凭据；不能用就绪检查把公共能力改成零值来掩盖配置错误。

商品固定映射继续使用 `ValidateSelection`；数量型规格只支持一个选项及非阶梯单价，输入受 `min_qty/max_qty` 约束，成本为单价和初装费分别乘数量。件数不是选项 ID，前后端都必须核算；金额上限以服务端整数分校验为准。

## 生命周期动作

`Capabilities.SupportsAction` 统一入队和派发映射；服务使用不可变绑定的编译能力，worker 另查实际账户适配器。`Suspend/Resume/Terminate` 各自独立，默认魔方提供方仍全部为 false。支持者执行 `Execute`，未知写保留账户租约和 `waiting_verification`，禁止自动重发。适配器须返回原资源/原商品以及 Suspended、Active 或 Deleted/Terminated/Cancelled 目标状态；上游受理和资源不存在不代表执行成功。

不支持的管理员动作和逾期调度生成 `waiting_manual`、`manual_lifecycle:true` 的未派发待办，保留原服务状态，不提前发送暂停通知。人工接口只回读原资源并核验，前后重查真实会话、权限、客户范围、账户/服务版本及任务身份；不通过通用人工交付接口，不保留只读账户租约。相同核验回执可重放，客户详情不返回管理员依据。客户生命周期入口及供应商取消申请保持未开放。

客户退款、停用需求通过工单交给管理员核实，再使用账单退款或服务详情操作，不新增专用客户申请入口、API 或申请状态机。工单本身不触发财务、资源或上游状态变更，具体退款约束见[人工操作手册](../payment-operations-runbook.md#客户请求与管理员处理)。

系统到期暂停与客户请求分开：既有逾期任务还扫描无未付账单的供应商周期服务，要求 Active、非导入复核、`next_due_at` 已过 24 小时，排除批准取消及未决任务。来源固定为原期限的微秒值与系统任务，不建单、不扣款、不提前修改状态。自动执行须同时支持 Suspend 和 Sync，写前 fresh Sync 核对原资源、商品与期限，再复查账户和任务；远端期限更晚、尚有权益或非 Active 时仅同步观察并取消未派发任务，更晚但仍过期的期限留下一轮按新来源扫描。Active 资源的远端期限早于本地承诺时保留本地权益，未派发转人工待办，不发写、不建立系统暂停来源。协议同秒视作同一期限，本地微秒来源仍不可变；读失败不发送写操作。默认魔方仍创建人工待办，不能直接将 `/provision/default` 的 `self` 暂停协议当成 due 暂停能力。

付清自动恢复只使用成功系统暂停的固定账单/任务来源和服务标记；管理员动作或观察到新的上游暂停原因会失效该标记。仅凭 suspended 状态、幂等键或旧快照不得推断来源。自动任务派发前取消已失效的账务动作；人工待办须核验真实结果或明确确认未生效，不能因平台未派发或账单变化推断人员尚未在上游处理。已确认的晚到暂停或恢复分别交回后续恢复/逾期扫描，已派发未知结果继续保留核验。完整规则见[供应商协议](../supplier-protocol.md#billing-suspension-and-paid-recovery)。

无账单到期暂停使用独立的不可变 payload、精确期限及成功任务服务标记；续费恢复还须同次可信来源、账单已付、完整延期和 fresh Active。普通回调不能建立该来源或消费延迟恢复；人工、self、abuse/策略暂停及终态继续保护。人工处理后已实际暂停时，即使本地期限变化也须核验真实事实，不能直接丢弃待办。

## 验收与发布

```bash
node scripts/test-compiled-suppliers.mjs --unit
ZJMFGO_TEST_DATABASE_URL=... node scripts/test-compiled-suppliers.mjs --integration
node --test scripts/supplier-components.test.mjs
node scripts/check-module-binaries.mjs
node scripts/check-doc-governance.mjs
node scripts/check-doc-links.mjs
```

严格集成仅允许显式回环 `_test` PG18，逐入口要求真实 run/pass、零 skip。至少覆盖第二协议双账户、双密钥 AAD、普通配置不可重定向、重复身份、schema/CAS、权限撤销、审计回滚、创建/同步/续费、未知结果、真实 worker 崩溃恢复、暂停/恢复/终止目标确认、人工待办/并发/撤权及注销、付清恢复强来源、账单变化期间人工外部事实保留，以及 schema165 和 schema169→170 正向升级保留。

到期分支另覆盖无账单、24 小时宽限、并发/重放、派发前本地及远端延期、上游读取失败/归属不符、已有暂停不得归因、回调与确认竞态、可信续费恢复及人工原因保护；`TestSupplierExpiry*` 与 schema171→172 升级及数据库 request guards 纳入同一严格供应商入口。迁移 172 保留旧来源和资金，不回填历史任务；先停旧 writer，再配套迁移/API/worker，不混跑。

本地适配器与隔离数据库属于 E1，组件夹具不能替代 E2 浏览器；真实供应商购买、扣款、续费、回调、网络和配额属于 E3，生产发布属于 E4。升级前备份并同包部署匹配 schema 的 API/worker；写入命名账户后，旧二进制不能读取该格式，回退必须保留兼容运行程序，不得通过抹去在途服务与任务恢复旧程序。
