# 编译期模块与渠道扩展契约

本项目采用“Go 编译适配器、数据库保存实例、前端按元数据渲染”的扩展模型。代码在编译时进入 API/worker，启动时注册描述符与类型化工厂；配置不能引入可执行代码。当前供应商、支付、邮件、短信、实名需要分别遵守各自的业务契约。

新增机器人平台按[机器人渠道开发指南](architecture/bot-channel-development.md)实施；其中明确现有通用接口、Telegram 专用接点、配置骨架和完整验收要求。

## 当前装配与构建核验

`internal/modules/all` 是 API 和 worker 的默认装配入口，显式引入 payment、notification、certification、supplier、filestorage 和 captcha。`audit/module-build-profile.json` 是受控的预期构建声明，不是审计数据转储。默认组合有 12 个模块，含 Telegram Bot 和本地/S3 文件存储；OAuth 目前只有类别定义，没有正式实现。通知事件与渠道边界见[统一通知契约](notification-architecture.md)，文件存储见[应用文件存储契约](architecture/file-storage.md)，验证码见[验证码接入契约](auth-captcha.md)。

程序可在读取环境变量、连接数据库之前输出公共模块清单：

```bash
./bin/api --module-manifest
./bin/worker --module-manifest
```

清单包含类别、稳定 ID、实现版本、配置 schema 版本、宿主契约版本、实例来源、工厂存在性和描述符摘要，不含配置值与凭据。内置实现的 `version=builtin` 随核心提交发布，不代表有独立版本号。

两个候选构建器都会执行刚构建的二进制，核对预期清单、实际注册清单和 API/worker 描述符一致性。产物保存预期 profile、两个实际清单及其与源码、二进制的哈希绑定。离线验证重验这些证据，不执行候选目录中的应用代码。当前只实现明确的 default 组合，尚未提供任意 build tags 裁剪工具。

`node scripts/check-module-binaries.mjs` 是 CI 的实际二进制门禁：真实构建 API/worker，在没有应用配置的最小环境执行上述命令，使用 strace 拒绝网络 syscall 并要求零尝试；同时核对 12 模块 profile、描述符和两二进制一致性。构建、执行、输出及进程组清理均有上限。它证明清单入口不启动数据库或网络，不证明正常启动健康和外部渠道可用。

当前 `c25d3bf` 已完成一次独立 v2 候选真实构建与运行：同包两次迁移、API/Web/worker、3 个公开页及 21 份资源、原生依赖与 worker TERM/重启通过。实际运行发现验收控制器漏复制 `module-manifest-lib.mjs`，已补齐并以“仅复制清单文件后独立导入”的回归接入 CI；使用修正的独立控制器重跑同一候选通过，未重建业务包。构建输入、控制器源码锚、首次失败和重跑证据保存于忽略的 `output/notification-runtime/`。这仍是隔离准备，真实渠道和生产切换另验。

新增模块必须同步装配和预期清单；发布前检查现有实例、在途任务、历史支付回调与服务绑定。不能因为新清单没有某实现就删除数据库记录。

## 配置定义与前端契约

`channel.Descriptor` 定义 ID、名称、类别、能力、字段以及以下版本边界：

| 字段 | 含义 |
| --- | --- |
| `version` | 实现版本；内置模块由核心提交追溯 |
| `contract_version` | 宿主模块协议版本；当前只接受 1 |
| `schema_version` | 配置值的结构版本，与并发更新版本分开 |
| `instance_mode` | `channel`、`supplier` 或 `profile`，决定真实配置来源 |

注册时拒绝重复字段、未知字段类型、无效默认值和不支持的宿主契约。字段支持 text、textarea、number、boolean、select、multiselect，以及默认值、数字上下界和整数约束。

同一份定义用于服务端配置归一化、错误分类和前端表单。服务端拒绝未知键、错误类型、无效枚举，以及密钥字段混入普通配置。布尔 false、数字 0 和空数组不被当成未提供值；可选 null 被省略，默认值真正进入保存的配置。

模块可以注册纯本地 `ConfigValidator`，仅接收普通值和凭据存在标记，不接收明文密钥，也不能联网或执行业务操作。启用、运行解析及配置状态使用相同校验入口；SMTP 和内置 SMS 已接入本地协议配置校验。

前端使用已编译的 React 组件渲染上述字段，未知字段类型阻止保存。复杂布局、自定义交互和新业务操作仍需扩展前端并重新构建；本模型不装载任意远程 JavaScript。

## 实例、凭据与并发

迁移 161 为渠道实例增加 `schema_version` 和按字段保存的密文对象。旧单密钥列与既有密文格式保持兼容；命名多密钥分别使用 AES-GCM，并以实例 ID 和字段名作为 AAD，防止跨实例或字段替换密文。

- 普通值通过 `config` 写入；命名密钥通过 `secure_config` 写入，空字符串或省略表示保留。
- `clear_secure_fields` 显式清除指定密钥；同一字段不能同时替换和清除。
- 旧 `secret/clear_secret` 只适用于恰有一个密钥字段的模块，不能与命名写入格式混用。
- API 只返回 `secure_fields` 的存在标记；它不证明凭据可解密或外部连接成功。
- 更新必须明确提供 `enabled` 和 `expected_version`；新前端同时提交 `schema_version/expected_schema_version`。
- 持久化 schema 与当前描述不兼容时拒绝配置写入和新业务解析，不隐式转换；后续升级须提供显式迁移并验证回滚。
- 已有实例的类别和实现 ID 不可更换，需新建实例；默认渠道的专用设置页面保留既有兼容同步逻辑。
- 更新、密钥替换、版本增加和审计在同一事务中提交。已有业务引用继续阻止删除。

读取时只返回已知、合法的非密钥配置字段，同时返回字段名与错误码。缺失实现或 schema 不兼容的实例不回显配置内容，保留数据库原值和管理行，允许仅停用或在无业务引用时删除。

## 实际运行边界

| 类别 | 行为契约与运行范围 |
| --- | --- |
| captcha | 类型化挑战/证据提供方；内置 PNG、图片/点击/widget 扩展协议；部署 profile 指纹绑定，核心控制单次消费，见[验证码契约](auth-captcha.md) |
| mail | 类型化 `MailProvider`；默认选择、实例解析、验证码、业务通知、试发和 worker 统一按实现 ID 分发 |
| sms | 类型化 `SMSProvider`；工厂接收完整普通值与命名凭据；内置传输校验留在各自实现 |
| bot | Telegram 私聊作为通用业务通知渠道，按事件路由、有效绑定和显式订阅范围分发；未知结果停止自动重发 |
| payment | 方式/类型化工厂与回调协议统一注册，实例配置和交易凭据快照驱动运行；生产仍为 mock/epay，接入规则见[支付模块开发契约](architecture/payment-module-development.md) |
| certification | 类型化实例工厂、申请配置快照与持久提交/查询；生产只装配人工审核，真实厂商另验，见[实名认证契约](domain/certification.md) |
| provision | 供应商使用 `supplier_accounts` 的完整普通配置、命名凭据与 schema，运行解析和后台字段共享定义；NOKVM 保留部署 profile，接入见[供应商模块开发契约](architecture/supplier-module-development.md) |

`RuntimeChannelInstance` 使用单行快照解析普通实例，校验启用、工厂、实例来源、schema 和本地配置，再解密凭据。默认渠道保留部署配置和旧 singleton 兼容，但不允许以默认入口绕过 schema 检查。历史支付继续使用交易凭据快照，不因新实例停用而改写既有交易身份。

邮件和短信 outbox 绑定实例 ID，在发送时解析当前配置和凭据，没有冻结入队时的配置版本。轮换凭据会影响待发送与重试任务；停用或不兼容会使对应发送失败，按原有通知重试规则处理。它与支付交易的凭据快照语义不同。

邮件和短信已有第二个测试适配器，分别使用独立字段和双密钥，通过 API 创建、试发、outbox 领取、通用分发及完成验证。这证明对应类别无需增加具体实现分支；它不是实际供应商或独立 worker 进程验收。

## 状态与生命周期

渠道实例区分 `valid`、`invalid`、`implementation_missing`、`schema_incompatible`、`external`。`valid` 表示当前保存状态通过本地配置检查；启用才要求必填凭据及模块本地校验，不能将它解释为凭据认证或远端健康成功。

应用目录继续用聚合数据展示配置/启用数量，另列 schema 不兼容和专用入口误配数量；不加载全部配置或解密凭据来计算目录状态。供应商账户和 NOKVM profile 的来源分别统计。能力用于界面展示，真实业务操作仍由服务端校验权限、资源范围、状态、版本和适配器支持。

缺失实现的供应商账户提供专用停用路径，只修改启用状态和并发版本，不解密或改写身份与凭据；仍检查当前会话、`products.manage`、版本、在途任务和审计。具体请求见[供应商契约](supplier-protocol.md)。

配置保存先本地校验，失败回滚并保留旧配置；外部发送/支付/交付通过各自队列和事务执行。当前没有通用的连接健康协议、实例连接池缓存、全局热切换或每任务 panic 隔离。新增这些机制时必须定义资源释放、超时、在途版本与未知外部结果处理，不能以自动重试掩盖可能已成功的写操作。

第三方独立发布或跨语言成为实际需求后，可以另立 RPC/子进程协议，明确签名清单、权限、超时、故障恢复与兼容；本阶段继续使用受控编译和本地 UI 组件。

## 验证入口与发布边界

```bash
node scripts/check-module-binaries.mjs
node scripts/test-compiled-notifications.mjs --unit
node scripts/test-compiled-notifications.mjs --integration
node --test scripts/channel-components.test.mjs scripts/module-manifest.test.mjs scripts/compiled-notification-gates.test.mjs scripts/release-runtime-acceptance.test.mjs
```

集成入口必须配置隔离 PG18 `ZJMFGO_TEST_DATABASE_URL`，内部强制数据库与进程恢复开关，串行 race 并拒绝跳过。覆盖类型/default、逐字段凭据、AAD、审计回滚、孤儿停用、旧 schema 拒绝、默认渠道兼容和新增适配器分发；还覆盖 160→当前版本的旧密文、待发任务和历史事件回放，以及 162→163 的既有订阅、路由和待发正文保留。CI 提供实际 API/worker 强制验证 160/161/162 启动拒绝；本地候选路径和证据边界见[测试入口](testing.md#编译渠道与通知运行验收)。

前端还需 lint/typecheck；构建证据需候选和离线验证测试。数据库、组件事件、浏览器、实际外部渠道和生产发布分别记录证据，不能互相替代。迁移与新 API/worker 应在同一发布包使用；回退前检查旧二进制能否理解已有模块、配置版本和命名凭据。

## 参考案例

- [Caddy 模块生命周期](https://caddyserver.com/docs/extending-caddy#module-lifecycle)、[xcaddy 构建选择](https://github.com/caddyserver/xcaddy#custom-builds)：装配、配置验证和资源生命周期。
- [Redpanda Connect 配置定义](https://github.com/redpanda-data/benthos/blob/main/public/service/config.go)：统一字段约束、解析与文档；敏感标记不等于加密。
- [Grafana 凭据配置](https://grafana.com/developers/plugin-tools/how-to-guides/data-source-plugins/add-authentication-for-data-source-plugins#encrypt-data-source-configuration)、[插件元数据](https://grafana.com/developers/plugin-tools/reference/plugin-json)：分字段密钥和兼容清单。
- [JSON Forms](https://jsonforms.io/docs/)：数据约束、布局与本地组件注册分层。本项目目前使用有限字段契约，没有新增该库依赖。
