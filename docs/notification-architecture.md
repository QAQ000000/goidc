# 统一通知契约

本契约规定通知编排与编译渠道的边界。第一批覆盖 `invoice.paid`，新增 Telegram 私聊与站内信；邮件、短信继续使用原队列。工单专项规则见[工单通知契约](ticket-notification-contract.md)，验证码见[认证验证码](auth-captcha.md)。

## 领域与模块

业务事务写入事件，再按路由、收件身份和偏好生成投递；worker 使用编译适配器发送。数据库保存配置与消息，不保存可执行代码。渠道是投递方式，平台实现是渠道模块：邮件可用 SMTP，短信可用供应商模块，机器人首批使用 Telegram Bot。旧 `mail`、`sms` 技术类别保留，新机器人类别为 `bot`；前端按描述符呈现配置。

`业务事件 → 收件身份及权限 → 路由与偏好 → 模板快照 → 投递及尝试 → 编译适配器`

- 首批事件 `invoice.paid` 仅包含账单号、金额及关联账户、账单标识。相同账单的付款成功事件去重。
- 邮件、短信保持原入队条件、模板、实例选择和唯一键；统一记录引用原 outbox，不能再发一份。旧队列的重试语义沿用原实现。
- Telegram 与站内信路由初始关闭，用户偏好也初始关闭。启用需要管理员开启路由、有效模板及用户主动订阅；Telegram 还需要该实例下的有效绑定。
- 新投递冻结事件、模板渲染结果、路由版本和目标身份；尝试时复查账户、原资源归属、目标、偏好、路由、实例启用状态。后续模板修改不重写已入队正文。
- 实例凭据按发送尝试读取当前加密配置。轮换影响后续尝试；禁用阻止后续发送。已经进入外部请求的消息无法撤回。
- 站内信复用 `admin_inbox_messages`，投递和状态变更在同一事务提交；个人接口只读本人消息。

## Telegram 绑定与 webhook

用户浏览器会话创建指定实例的一次性绑定挑战；服务端仅保存随机 token 的 SHA-256，十分钟过期，用户的新挑战撤销该用户所有未使用的旧挑战。响应包含 `https://t.me/<bot_username>?start=<token>`，不得将该链接或原 token 写入审计、访问日志或公共证据。

Telegram 回调入口为 `POST /api/v1/notifications/telegram/{instance_id}/webhook`。每个实例独立使用加密保存的 `webhook_secret`，对 `X-Telegram-Bot-Api-Secret-Token` 做常量时间校验；此入口免浏览器 CSRF，但不免渠道认证。仅处理 private chat，要求 `from.id == chat.id` 且非机器人发送的 `/start <token>`。不接收群聊、任意填写的 chat ID 或客户资料中的社交账号作为验证绑定。

挑战必须属于该实例，未过期、未消费、未撤销，发起会话仍有效且账户仍启用。外部身份以“实例 ID＋chat ID”命名，不能转移其他账户已有绑定。重复 webhook 不得重复绑定或开启订阅。绑定成功后仍需用户主动订阅；解绑关闭后续投递并撤销未使用挑战。取消订阅或重新绑定不会补发曾跳过的历史事件。

管理员显式配置 webhook，回调 URL 来自服务端 `PUBLIC_ORIGIN`，不能接收任意外部 URL。需使用公网 HTTPS，设置 `secret_token` 与 `allowed_updates=["message"]`。机器人 username 与 token 应属于同一个 Bot；生产启用前需要真实平台核验。

启用顺序：先以 `getMe` 核验身份并以 `getWebhookInfo` 检查既有回调，再通过后台渠道实例保存 `bot_username` 与加密凭据 `bot_token`、`webhook_secret`；配置服务器的公网 `PUBLIC_ORIGIN` 后，由具备设置权限的管理员配置 webhook。随后用户完成绑定和主动订阅，管理员开启路由，再以真实业务事件验收。没有公网 HTTPS 地址时只能完成本地模拟验收，首批不提供轮询绑定。API 和 worker 若无法直连 Telegram，可在各自进程环境设置 `HTTPS_PROXY`；代理地址属于部署配置，不写入渠道或源码。

## 状态与恢复

| 状态 | 含义 | 后续处理 |
| --- | --- | --- |
| pending | 等待执行或平台明确限速后的等待 | 到期后尝试，重新验证授权和偏好 |
| sending | 持有短期 lease 的一次外部尝试 | 完成时校验 lease token，防止旧 worker 覆盖 |
| accepted | 平台确认接受，站内信则为本地事务写入 | 不等于客户已读或最终送达 |
| failed | 明确拒绝、配置失效或次数耗尽 | 保留固定错误码，修复后等待未来事件 |
| unknown | 超时、5xx、响应畸形或发送 lease 过期，结果不确定 | 停止自动重发，人工核验；首批没有重发 API |
| skipped | 未订阅、解绑、路由关闭、资源不可见或过期 | 不自动补发 |

Telegram 仅明确 429 且含有效 `retry_after` 时自动重试，最多五次、事件有效期二十四小时。请求不超过十五秒，限制响应体大小，禁止重定向，默认纯文本且不启用 Markdown/HTML。成功须包含有效消息 ID 及匹配的私聊目标。错误记录不能包含 token 所在 URL、外部原文或消息正文。

每次尝试单独保留 lease、次数、开始/结束时间、结果码和平台消息 ID。事务完成失败或进程退出后，过期 `sending` 转为 `unknown`，不假定外部没有发送。旧邮件、短信记录展示原 outbox 状态，并明确兼容队列边界。

## API、权限与页面

以下路径以 `/api/v1` 为前缀。本人设置和管理写入均需要浏览器会话，事务内复查会话、账户及权限；变更与审计同事务提交。所有版本写入使用 `expected_version`，冲突返回 409，页面保留草稿并提供刷新。

| 接口 | 权限与用途 |
| --- | --- |
| `GET/PATCH /me/notification-preferences` | 本人设置；按渠道返回偏好、目标和可绑定实例；首次版本为 0 |
| `POST /me/notification-bindings/challenges` | 本人创建短效绑定链接，频率受限 |
| `DELETE /me/notification-bindings/{id}` | 仅本人目标，版本检查，不允许提交外部身份 |
| `GET /me/notification-inbox` | 仅本人站内信，分页 |
| `POST /me/notification-inbox/{id}/read` | 仅本人消息，幂等标记已读 |
| `GET /admin/notification-center` | `notifications.view`；规则、模板、可用实例和分页投递元数据；客户范围实时过滤 |
| `PATCH /admin/notification-center/rules/{id}` | `notifications.manage`；显式启用与实例选择、版本检查 |
| `PATCH /admin/notification-center/templates/{id}` | `notifications.manage`；白名单事件变量、版本检查 |
| `POST /admin/notification-center/channels/{id}/webhook` | `settings.manage`；仅服务端配置的 HTTPS 回调地址，外部失败脱敏 |

个人页面提供绑定、刷新、订阅、解绑和站内信；后台保留旧邮件/短信入口，增加规则、模板与投递记录。统一中心只展示元数据，不向管理员返回客户 Telegram chat ID、绑定挑战、消息正文或任何渠道凭据。权限不足、无实例、未绑定、未订阅、空记录、加载失败及版本冲突须有独立说明。

## 后续扩展与验证边界

群通知、部门机器人、公众号 AppID/OpenID、平台模板与订阅资格、工单及营销迁移属于后续批次。个人通知不能自动降级到组织群；验证码保留原用途、限流、TTL、加密和失效流程，不进入通用广播。现有联系人资料不等于平台绑定。

迁移 162 新增数据结构并扩展渠道类别，不重写已有迁移。回退应用前需停用新增路由和机器人实例、排空或封存待发送记录；旧版本不能处理新队列，不能以删除通知记录代替回滚。

验证覆盖独立 PG18 的事件去重、旧队列兼容、绑定 TTL/重放/错实例/失效会话、跨用户与撤权、审计回滚、CAS、退订、解绑、429、未知结果、lease fencing；协议测试只连本地模拟器。浏览器与真实 Telegram 验证分别记录，不把模拟接受当成平台实收。

## 当前验收状态

| 层级 | 已验证范围 | 边界 |
| --- | --- | --- |
| E1 | 独立 PG18 串行专项 40 项通过；包含真实适配器与数据库交互、本地协议模拟器、权限、并发和恢复 | 普通专项跳过长驻浏览器夹具，夹具另行启动验收；未使用业务库 |
| E1 | Go 全仓测试编译、专项 race、通知及渠道组件、前端 lint/typecheck/代理、隔离生产构建和发布脚本快照测试通过 | lint 有 7 个既有 warning；发布脚本完整门禁在独立 Git 源码快照通过；这些验证不代表已经部署 |
| E2 | 隔离 API/worker 与生产构建前端完成绑定、主动订阅、路由保存、模拟付款投递、站内信展示和只读管理员检查；桌面及 390×844 手机检查通过 | 最终后端已重开隔离夹具复验；Telegram HTTP 由本地模拟器替代，解绑确认与并发边界以 E1 为证 |
| E3 | 真实 Telegram `getMe` 与 `getWebhookInfo` 经部署代理核验通过，既有 webhook 为空 | 当前没有公网 HTTPS 地址；未配置真实 webhook，未验收真实消息收发 |

运行日志、截图、源码快照元数据及清理结果保存在被忽略的 `output/notification-center/` 与 `output/playwright/notification-center/`。上述结果不代表已经部署或具备生产渠道可用性。

平台协议参考：[Telegram Bot API](https://core.telegram.org/bots/api#sendmessage)、[Webhook](https://core.telegram.org/bots/api#setwebhook)、[深链绑定](https://core.telegram.org/bots/features#deep-linking)。平台文档的在线可达性与真实凭据验证必须在启用前单独检查。
