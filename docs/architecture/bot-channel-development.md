# 机器人通知渠道开发指南

本文是新增机器人平台的开发入口，适用于私聊机器人、组织应用、群机器人及需要平台模板的通知渠道。项目规则以[统一通知契约](../notification-architecture.md)和[编译模块契约](../compiled-module-architecture.md)为准；平台认证、收件资格、消息格式及限流须核对对应平台的官方协议。

当前只有 Telegram 私聊完成了机器人通知接线；通用发送接口已经存在，目标绑定、数据库渠道约束、worker 分发和部分页面仍有 Telegram 专用逻辑。本文说明如何完成一个新渠道，不表示所有平台已经支持，也不提供运行时上传插件的机制。

## 先确认接入范围

接入前在变更说明中填齐下表；`example`、`example_bot` 只是示例标识，不能直接用作正式平台名称。

| 项目 | 必须明确的内容 |
| --- | --- |
| 标识 | 通知渠道键，如 `example`；编译实现 ID，如 `example_bot`；实例 ID 由系统创建。三者分别用于路由、工厂选择和具体配置 |
| 目标类型 | 本人私聊、组织成员、群或组织 webhook；明确租户、应用、实例及外部身份的唯一性范围 |
| 认证与授权 | 服务端凭据、用户绑定方式、订阅资格、回调验签及解绑/撤销方式 |
| 消息能力 | 纯文本、卡片或平台模板；长度限制、模板审批、发送时间窗口及可发送的事件 |
| 投递知识 | 平台确认接受的判据、消息 ID、明确拒绝、明确限流和结果未知的判据 |
| 生命周期 | 凭据轮换、停用、绑定失效、模板变更、任务过期和回退时的处理 |
| 外部验收 | 官方文档地址、测试账号/租户、实际发送及回调的验收条件；缺失条件标为未验证 |

本人通知和群通知需要不同授权。群机器人不能直接复用个人绑定，把客户订单、服务或账单通知自动转发到群；组织接收范围、管理员权限和可见字段须另行定义。公众号还要定义 OpenID、平台模板及订阅资格，不能假设所有业务事件都允许发送。

## 复用边界与源码地图

统一链路为：`业务事务 → 事件 → 路由/订阅/目标 → 模板快照 → 投递任务 → 编译适配器 → 平台`。

新增平台复用业务事件，不在下单、开通、暂停、续费等业务函数中增加平台发送分支。新增事件则先扩展事件目录、资源约束、业务事务和幂等测试，再考虑渠道映射。事件目录当前覆盖哪些通知，以代码和统一通知契约为准。

| 接点 | 代码入口 | 接入要求 |
| --- | --- | --- |
| 事件与变量 | [notification_catalog.go](../../internal/store/notification_catalog.go)、[notification_center_events.go](../../internal/store/notification_center_events.go) | 复用资源查询、事件去重、变量白名单和一次性渲染 |
| 适配器与工厂 | [telegram.go](../../internal/notification/telegram.go)、[module/registry.go](../../internal/module/registry.go) | 实现 `BotProvider`，通过类型化工厂注册 |
| 公共数据结构 | [notify/model.go](../../internal/notify/model.go)、[channel/registry.go](../../internal/channel/registry.go) | 遵守 `Job/Result`、描述符与配置版本契约 |
| 绑定与偏好 | [store/notification_center.go](../../internal/store/notification_center.go)、[httpapi/notification_center.go](../../internal/httpapi/notification_center.go) | 扩展平台发现、目标验证、主动订阅、回调和权限 |
| 入队与发送资格 | [notification_center_admin.go](../../internal/store/notification_center_admin.go)、[notification_center_worker.go](../../internal/store/notification_center_worker.go) | 扩展路由实例校验、领取白名单、目标资格、锁序和恢复范围 |
| worker 分发 | [center_worker.go](../../internal/notification/center_worker.go) | 现有分发判断仍限 `telegram`；新增平台须接入通用工厂及发送前复查 |
| 数据迁移 | [162 通知中心](../../db/migrations/162_unified_notifications.sql)、[163 生命周期](../../db/migrations/163_notification_lifecycle.sql) | 阅读现有约束，新增前向迁移，保留旧数据及历史迁移 checksum |
| 用户及管理页面 | [notification-center.ts](../../web/src/lib/notification-center.ts)、[notification-preferences.tsx](../../web/src/components/notification-preferences.tsx)、[notification-center.tsx](../../web/src/components/notification-center.tsx) | 扩展平台交互、绑定链接校验和能力展示 |
| 编译与验收 | [all.go](../../internal/modules/all/all.go)、[构建 profile](../../audit/module-build-profile.json)、[通知门禁](../../scripts/test-compiled-notifications.mjs) | API/worker 装配一致，新增模块和测试进入精确清单 |

不要仅将 SQL 的 `telegram` 条件删除或放宽为任意字符串。新渠道必须有明确目录、合法目标、启用实例和权限校验；未知渠道或能力缺失时拒绝发送。

## 实现步骤

### 1. 定义描述符并注册工厂

模块类别使用 `channel.Bot`，实例来源使用 `channel`。实现 ID 应稳定，已有实例不能原地换成另一个实现。配置结构变化使用 `SchemaVersion`；宿主协议使用 `ContractVersion`；行级 CAS 使用 `expected_version`，三种版本不能混用。

普通字段支持的类型、默认值和约束见编译模块契约。密钥字段标记 `Secret: true`，用 `secure_config` 写入；工厂通过 `module.Config.Secrets` 读取，普通字段通过 `Values` 读取。沿用现有逐字段加密和 AAD，不自行保存明文凭据。配置 API 只返回密钥存在标记。

`ConfigValidator` 只做本地普通值和凭据存在性检查；`SecretValidator` 对新提交的密钥做本地格式检查。注册、构造和配置保存不能调用平台 API、发消息或注册 webhook。外部身份核验、连接检查和回调配置使用显式管理操作，检查权限并记录脱敏审计。

下面是可编译的注册骨架。建议新适配器放在 `internal/notification/<platform>/` 子包，导入公共通知包；由装配入口导入子包，避免公共包反向导入子包形成循环。此示例始终返回 `failed`，在实现协议与测试前不能用于实际发送。

```go
package examplebot

import (
	"context"
	"errors"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/notify"
)

type provider struct {
	appID string
	token string
}

var _ notification.BotProvider = (*provider)(nil)

func init() {
	err := notification.RegisterBotProvider(channel.Descriptor{
		ID: "example_bot", Name: "Example Bot", Category: channel.Bot,
		Version: "builtin", SchemaVersion: 1, ContractVersion: 1,
		InstanceMode: "channel", Capabilities: []string{"send"},
		Fields: []channel.Field{
			{Key: "app_id", Label: "应用 ID", Type: channel.Text, Required: true},
			{Key: "api_token", Label: "API Token", Type: channel.Text, Required: true, Secret: true},
		},
	}, func(cfg module.Config) (notification.BotProvider, error) {
		appID, ok := cfg.Values["app_id"].(string)
		if !ok || strings.TrimSpace(appID) == "" || cfg.Secrets["api_token"] == "" {
			return nil, errors.New("example bot configuration invalid")
		}
		return &provider{appID: appID, token: cfg.Secrets["api_token"]}, nil
	})
	if err != nil {
		panic(err)
	}
}

func (p *provider) Send(ctx context.Context, job notify.Job) notify.Result {
	return notify.Result{Outcome: "failed", Code: "example_not_implemented"}
}
```

`Capabilities` 目前是声明元数据，不会自动生成方法、回调、绑定页面或授权校验。`BotProvider` 当前仅有 `Send(context.Context, notify.Job) notify.Result`；需要 OAuth、扫码、回调注册等能力时，先定义并实现对应扩展点，不能虚构已有通用接口。

### 2. 实现平台发送协议

`notify.Job` 已包含投递 ID、lease、渠道、实例、目标、标题、正文和有效期。适配器消费已渲染的快照，不重新查询业务资源、不重新选收件人、不按最新模板重写正文，也不直接修改业务状态或投递表。凭据由宿主在发送前实时解析；实例停用或凭据轮换影响后续尝试。

发送顺序应为：验证目标和消息格式 → 检查有效期 → 建立有界请求 → 校验 HTTP 与平台业务响应 → 返回固定结果。请求必须支持 context 取消，超时不超过任务剩余有效期，限制响应体大小并禁止自动重定向。Telegram 当前超时十五秒、响应上限 64 KiB；新平台须明确自己的边界，不能让外部请求超过 lease 或无限等待。

生产 API 目标固定或来自受控平台配置。平台确需用户提供 webhook URL 时，须单独定义允许的 HTTPS 主机、凭据字段和出站地址校验，防止访问内网/本机及重定向绕过；不能沿用测试注入地址作为生产配置。代理属于 API/worker 部署环境，不能写入源码、描述符或共享实例。

平台需要短效 access token 或刷新凭据时，另行定义实例/租户隔离、过期、并发刷新、缓存与失效策略；当前没有通用机器人 token 刷新服务。刷新属于平台认证流程，不能写回业务事件或绕过配置版本与加密边界，也不能以刷新凭据为由重发结果未知的消息。

| `Result.Outcome` | 返回条件 | 宿主处理 |
| --- | --- | --- |
| `accepted` | 平台响应满足官方成功判据；保留可验证的消息 ID | 结束，不自动再发；仅代表平台接受，不等于送达或已读 |
| `failed` | 本地参数非法、凭据明确失效或平台明确拒绝 | 结束，不自动重试 |
| `rate_limited` | 平台明确确认本次未接受且可延后重试；提供正数 `RetryAfter` | 次数和有效期允许时重新排队 |
| `unknown` | 超时、连接中断、5xx、回包畸形或无法证明是否接受 | 结束自动重试，等待核验 |

HTTP 200 本身不是成功；HTTP 429 也须按平台协议确认限流语义。一次 `Send` 只执行一次消息投递尝试，不能在内部重试消息请求或自行拆成多条发送。需要多条、卡片或平台模板时，应先定义投递粒度、部分成功、幂等和结构化快照契约；当前 `Job` 仅有纯文本标题/正文，没有通用平台模板 ID 或卡片参数。

当前最多五次尝试，包含首次；只有有效 `rate_limited` 才能重排，最短等待一秒，下一次必须早于过期点。投递默认有效期二十四小时、lease 两分钟；不能把 `unknown` 或落库失败改回 `pending`。进程中断后过期的 `sending` 恢复为 `unknown`。该机制控制重复风险，不承诺跨平台恰好送达一次。

`pending/sending/skipped` 是宿主状态，不能作为适配器结果返回。`Code` 使用固定的小写字母、数字和下划线，最多 64 字符；`ProviderMessageID` 超过 128 字节会被清空。二者不能携带错误原文、正文、token、目标身份或带密钥的 URL；不要原样记录 SDK/HTTP 错误。

### 3. 定义目标验证、绑定和订阅

为新平台定义外部目标的格式、所属应用/租户、唯一性和归属验证。现有目标限制为 Telegram 正整数私聊 ID，不能直接用它保存 OpenID、群 ID、邮箱或 webhook URL。身份来自经服务端验证的平台交互，不能让浏览器直接提交一个外部 ID 就获得接收客户数据的资格。

私聊绑定至少保留：短效一次性挑战、只存挑战哈希、发起会话仍有效、账户仍启用、实例匹配、原子消费、跨用户占用保护和回放幂等。Telegram 的十分钟 TTL、私聊 `from.id == chat.id` 与 `t.me` 链接规则属于该平台实现；其他平台须设计等价证明，不能照搬字段判断。

接收平台回调时检查实例、签名/共享密钥、请求大小、重放和必要的时间窗口；免浏览器 CSRF 不代表免平台认证。授权凭据在事务提交前再次核对，覆盖轮换和撤权竞态。不能用通知平台回调直接宣称服务开通、续费或付款成功。

绑定成功后仍需用户主动订阅；当前偏好按“用户＋渠道”保存，范围为 `invoice.paid` 或 `business`，没有每事件独立用户订阅表。扩展范围必须显式选择。解绑、退订、目标变化和撤权应使旧待发任务失效；重新绑定或再次订阅不补发历史消息。

群/组织目标若需要管理员订阅策略，必须新增相应授权模型与审计，不能绕过个人偏好假装已取得用户同意。现有 Telegram 个人流程可以保持独立，不要求所有平台都提供 webhook 或用户绑定链接。

### 4. 迁移渠道、配置路由和模板

新增前向迁移，文件编号以实际迁移目录为准。不得改写 162/163 或其他已提交历史迁移；上线必须使用同包迁移和对应 API/worker。

迁移与宿主接线至少检查以下项目：

1. 扩展模板、路由、用户偏好、目标、投递等表的渠道约束；按新平台定义目标格式、唯一性、租户隔离与索引，保留现有数据约束。
2. 为支持的事件创建确定 ID 的渠道模板和默认关闭路由；不自动启用实例、扩大旧订阅或扫描历史业务补事件。
3. 扩展 API/Store 的渠道目录、可用实例发现及路由实例校验；渠道与实现映射由服务端控制，不能只校验 `category=bot` 就允许任意实例混用。
4. 扩展事件入队的目标选择、消息格式校验、worker 领取/恢复范围和分发。领取与发送前均保留用户、资源归属、路由、偏好版本、目标版本、实例和事件有效性复查及锁序。
5. 旧邮件短信继续链接原 outbox，不新建一套外发；新机器人不得重复运行旧渠道队列。

模板变量从事件目录取得，业务资源由事务内查询生成；仅一次替换，不执行脚本或递归插值。正文不含服务密码、密钥及付款凭据。管理员模板原文正文上限当前为 4000 字符；渲染后的 Telegram 上限为 4096 字符，站内信为 100000 字符。新平台须定义自己的长度/编码规则，并同时覆盖保存、渲染、领取和发送校验，不能依赖当前非 Telegram 的宽松回退值。

投递保存模板版本和已渲染正文。当前发送检查模板仍 active，但不要求当前模板版本等于快照版本，因此修改模板不会重写或自动取消旧消息；停用模板会阻止后续发送。新增平台应保持该语义，改变时需另立迁移及回归契约。

### 5. 补齐 API 与前端

通用实例配置可复用描述符表单；平台特有的 OAuth、扫码、绑定链接、模板选择、回调注册等操作需要已编译的前端组件和明确 API。不能让描述符加载远程 JavaScript，也不能仅根据 capability 文本放开服务端操作。

本人接口只操作本人目标及偏好；后台读取遵守 `notifications.view` 和客户范围，规则/模板写入要求 `notifications.manage`，现有渠道实例及 webhook 管理遵守 `settings.manage`。新操作须明确自己的权限。写入、CAS 和脱敏审计同事务，冲突返回 409 并保留草稿。

页面需覆盖：无实例、未绑定、绑定失效、未订阅、平台资格不足、只读、撤权、空记录、失败、加载、版本冲突和结果未知。按事件和渠道过滤再分页，避免隐藏请求失败阻断整个界面。外部链接须按平台白名单验证；现有 `safeNotificationBindingURL` 仅接受 Telegram 链接，新增平台必须扩展相应规则。

后台投递记录继续只展示授权范围内的元数据，不返回客户外部身份、挑战、消息正文或凭据。平台不支持个人绑定、回调配置或某种模板时，界面应明确说明，并隐藏不适用操作。

### 6. 编译装配与升级兼容

新子包由 [internal/modules/all/all.go](../../internal/modules/all/all.go) 显式导入，确保 API/worker 同时包含它；不要为单一进程私下注册。同步[预期构建 profile](../../audit/module-build-profile.json)，并更新固定的模块数、排序及 ID 断言，包括 [Go 清单测试](../../internal/modules/manifest/manifest_test.go)、[Node 清单测试](../../scripts/module-manifest.test.mjs)和[实际二进制检查器](../../scripts/check-module-binaries.mjs)。检查器本身目前也要求九模块，新增平台不能只改 profile 或测试文件。

实际 API/worker 的 `--module-manifest` 应有相同的模块列表、描述符摘要和公共契约，角色字段 `binary` 分别为 `api` 和 `worker`，不能要求完整 JSON 相等；无应用配置时不得尝试连接数据库或网络。描述符不含实例值或凭据。模块被移除或 schema 不兼容时保留已有配置和历史记录，拒绝新业务解析，提供受权限保护的停用路径；不能删记录来让清单通过。

回退前先停止新增投递，停用新增路由/实例并处理在途任务。当前没有自动反向迁移；旧二进制是否能理解新渠道、目标、命名凭据及任务必须实际验证。配置开关关闭不等于数据库已经回滚。

## 验收与 CI 登记

所有新平台先连接本地协议模拟器，数据库使用独立 PG18 测试库。真实平台、浏览器和生产发布分别留证，等级定义见[工程治理契约](engineering-governance.md#证据等级)。

| 范围 | 必须覆盖 |
| --- | --- |
| 配置及模块 | 缺字段、错误类型/枚举、未知字段、密钥混入普通值、密钥替换/清除、CAS、审计回滚、schema 不兼容、缺失实现、API/worker 清单一致 |
| 平台协议 | 成功判据、错误业务码、401/403、限流等待、超时/5xx、畸形及超大响应、重定向、目标不匹配、消息长度与脱敏 |
| 身份及权限 | 正常绑定、TTL、重放、错实例/租户、失效会话、跨用户抢绑、解绑、退订、凭据轮换和权限撤销竞态 |
| 事件与投递 | 复用业务事件、事务回滚无事件、事件/投递去重、显式订阅范围、默认关闭、无历史补发、其他渠道独立、旧邮件短信不重复 |
| worker 与升级 | 双进程领取、lease 失效、平台接受后落库失败、崩溃恢复为 unknown、最多五次尝试、到期停止、慢渠道与旧链隔离、旧数据及待发消息升级 |
| 浏览器 | 桌面/手机、平台特有绑定、规则/模板、筛选分页、409 保留草稿、只读/撤权及明确错误状态；列表种子数据不能证明实际发送 |
| 真实平台 | 核验平台身份与权限，以合成业务实际发送至正确目标；需要回调的平台完成公网 HTTPS 验签及解绑/退订后停止发送；记录平台消息 ID 与时间窗口 |

新增 Go 测试必须按包及精确入口名加入 [test-compiled-notifications.mjs](../../scripts/test-compiled-notifications.mjs) 的 `unitSuites` 或 `integrationSuites`；固定 `-run` 不会自动执行所有新文件。关键入口在 [compiled-notification-gates.test.mjs](../../scripts/compiled-notification-gates.test.mjs) 增加防移除断言。必要时更新 CI 和文档中的门禁数量；不得把长驻浏览器夹具或子进程 helper 当作普通测试入口。

从仓库根目录执行基础门禁：

```bash
node scripts/check-module-binaries.mjs
node scripts/test-compiled-notifications.mjs --unit
node --test scripts/channel-components.test.mjs scripts/notification-center-components.test.mjs scripts/module-manifest.test.mjs scripts/compiled-notification-gates.test.mjs
npm --prefix web run lint
npm --prefix web run typecheck
npm --prefix web run test:proxy
node scripts/check-doc-governance.mjs
node scripts/check-doc-links.mjs
node scripts/check-contract-gates.mjs
git diff --check
```

集成门禁必须先创建任务自有的回环 PG18、明确端口和以 `_test` 结尾的库，再设置 `ZJMFGO_TEST_DATABASE_URL`；不使用业务数据库。随后执行：

```bash
node scripts/test-compiled-notifications.mjs --integration
```

runner 强制串行 race，拒绝缺失、跳过和失败。实际升级启动拒绝还需提供同源 API/worker 二进制及 `ZJMFGO_UPGRADE_BINARY_ACCEPTANCE=1`，完整变量与命令见[测试入口](../testing.md#编译渠道与通知运行验收)。新迁移必须补前一实际 schema 起点的升级与旧二进制兼容边界测试，不能只复用现有 160/161/162 的拒绝断言。

前端生产构建在隔离目录使用现有运行目录保护，不覆盖活动 `.next`；候选构建、离线校验和发布运行按[发布准备契约](../release-readiness-next-contract.md)验收。本地通过不能宣称远端 CI 或真实平台通过。

## 交付检查单

- [ ] 接入范围、目标身份/租户、平台官方协议和可发送事件已确认。
- [ ] 适配器、描述符、命名凭据、本地校验和显式管理操作齐全。
- [ ] 目标验证、主动订阅、回调认证、解绑/撤销及发送前复查齐全。
- [ ] 新迁移、默认关闭路由、事件模板、消息格式和未知渠道拒绝策略齐全。
- [ ] worker 领取、分发、完成、恢复和旧邮件短信兼容已覆盖。
- [ ] API、前端状态、权限、CAS、脱敏审计及平台特有交互齐全。
- [ ] 编译装配、预期 profile、固定断言及严格测试入口已同步。
- [ ] 独立数据库、协议、浏览器、升级与真实平台分别记录 PASS/FAIL/未验证。
- [ ] 平台启用顺序、在途任务处理、回退限制和密钥轮换步骤已写入变更说明。
- [ ] 更新本指南或现有领域契约；截图、日志、JSON、密钥和运行证据只进忽略的 `output/` 或受控存储，不进活动文档。

本指南不保存某次接入的执行日志，也不替代平台协议。接入后的当前能力、限制和验收状态更新统一通知契约；共享模块规则更新编译模块契约，避免每个平台复制一份互相冲突的业务规则。
