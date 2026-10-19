# 验证码模块与接入契约

## 当前范围

验证码是编译装配的 `captcha` 模块。默认 `builtin_png` 保留六字符、190×64 PNG 图片，管理端原有“登录、注册和找回密码需要图形验证码”开关默认关闭。登录与管理员登录共用 `login`，注册使用 `register`，找回密码使用 `forgot-password`。MFA、邮箱/短信验证码、注册资格和锁定策略仍由各自认证流程控制；自动风险触发模式暂不支持。

第三方图片、原生点击和 SDK/widget 令牌共用挑战生命周期。本批提供通用接口、前端组件和测试提供方，未接入任何真实第三方服务。测试提供方只存在于测试文件，不能在部署中选择。

前台和后台登录分别绑定 `portal: customer/admin`，MFA 挑战冻结入口；客户商业接口必须验证真实客户角色，管理员只使用后台和显式共享能力。完整规则、兼容策略与迁移 181 的发布边界见[前后台身份边界](admin-rbac.md#前后台身份边界)。

## 配置与装配

实现通过 `internal/captcha` 的类型化注册表注册，默认 API/worker composition root 和离线模块清单均包含 `captcha:builtin_png`。注册表复用 `internal/module`，代码编译进二进制；数据库和接口不能注入 Go、HTML 或脚本。

部署环境 `CAPTCHA_DEFAULT` 默认 `builtin`；`CAPTCHA_PROFILES` 为最多 32 项的 JSON 数组，每项包含 `id`、`implementation_id`、`config` 和 `secrets`。空配置使用内置 profile；保留 ID `builtin` 不允许覆盖。额外 profile 示例：

```json
[{"id":"vendor_primary","implementation_id":"vendor_compiled_id","config":{"site_key":"public-key"},"secrets":{"secret_key":"server-only"}}]
```

示例实现必须先开发、注册并重新构建，未知实现和错误字段会拒绝启动。密钥仅留在部署环境，不能进入文档、公共接口、日志或前端。当前管理端负责开关；提供方和密钥由部署 profile 配置。应用目录支持“验证码”筛选，按部署 profile 汇总；只有当前选中且站点开关已开启的 profile 计入启用数，保留 profile 只计入配置数。目录只返回汇总和描述符，不返回 profile ID、指纹或密钥。

每个挑战保存实现、profile ID、包含配置及密钥的 SHA256 指纹和私有验证状态，不保存密钥快照。消费时按照该绑定解析，不能改用当前默认提供方。切换默认 profile 后，保留的旧 profile 可完成未过期挑战；修改或删除其配置会让旧挑战返回不可用，用户需要刷新。切换时建议新增 profile ID、保留旧配置至少五分钟，再清理。所有 API 实例必须部署一致配置。

## API 与前端协议

`GET /api/v1/auth/captcha?action=login|register|forgot-password` 返回 `enabled`；启用时返回 `captcha_id`、`expires_at` 和 `challenge`。图片响应额外保留旧 `image` 字段。

| challenge.kind | 公开字段 | 提交的 captcha_proof |
| --- | --- | --- |
| `image` | `image`、`width`、`height`、`prompt` | `{kind:"image",answer:"用户输入"}` |
| `click` | 同上，加 `max_points` | `{kind:"click",points:[{x:0.2,y:0.5}]}`，坐标按图片宽高归一化至 0–1，顺序保留 |
| `widget` | 本地 `renderer` ID、`public_data` | `{kind:"widget",token:"提供方证据"}` |

认证请求同时提交 `captcha_id`。旧 `captcha_answer` 仅用于内置 PNG；显式 `captcha_proof` 优先，不允许验证失败后回退。服务端不会全局大写、截断或假定六字符，格式与最终校验由提供方负责。客户端的“已成功”或非空 token 不构成服务端验证结果。

图片只接受有界的嵌入 PNG/JPEG/WebP，声明尺寸须与图片一致。公共挑战最多 64 KiB，私有状态最多 16 KiB，证据最多 16 KiB；图片答案最多 1024 字节，点击最多 32 点。未知类型、渲染器、格式错误、超时和过期均拒绝放行。

登录、管理员登录和找回密码共享组件与挑战 hook。刷新、动作切换、过期、组件卸载或进入 MFA 时清空证据；旧请求不能覆盖新挑战。签发失败提供可重试状态，认证失败后的自动刷新保留原认证错误。widget 渲染器来自本地编译注册表；API 的 `renderer` 只选择已注册实现，不作为脚本地址，不插入任意 HTML。

## 核心与提供方责任

核心控制五分钟过期、随机挑战 ID、HttpOnly/SameSite=Lax 浏览器 nonce、动作绑定、对端地址限流和有界过期清理。格式正确的 nonce 在刷新和多标签页间复用。IP 被记录并参与签发限流，消费不绑定 IP，允许移动网络切换。`TRUSTED_PROXY_CIDRS` 控制可信代理；HTTPS 部署必须设置 `COOKIE_SECURE=true`。

核心在调用提供方前，以一条原子 UPDATE 持久化消费，且验证期间不持有数据库事务或行锁。同一挑战只能尝试一次；错误、畸形证据、超时、取消、进程中断和未知外部结果都需要刷新。并发请求最多一个进入提供方验证，不能自动重试远端验证。核心限制每次提供方调用八秒；提供方必须遵循 context 并配置 HTTP 超时。返回成功时再次核对 deadline 和挑战有效期。

提供方负责：生成公共挑战和私有状态、校验证据、检查厂商返回的 action/hostname/challenge ID 等绑定、验证签名及服务端成功状态、将故障与不匹配分开返回。内置 PNG 仅保存答案 SHA256。第三方私有状态不能包含答案明文、凭据或可执行内容。关闭验证码时不签发 Cookie、不写挑战；启用后存储或提供方故障返回 `503 captcha_unavailable`，验证不匹配返回 `invalid_captcha`，均不能创建会话或绕过认证。

升级迁移保留旧 PNG 挑战的答案摘要、nonce、动作和消费状态，在原有效期内仍可使用；升级后新挑战记录明确提供方绑定。管理设置继续使用 `settings.view/manage`、版本、活动会话复查和审计；省略旧站点设置字段仍保留当前开关。

## 新增任意第三方验证码

1. 在 `internal/captcha` 中实现 `Provider.Issue` 和 `Provider.Verify`，或者在独立适配包中调用 `Register`。工厂接受 `module.Config`，描述符声明 schema、profile 模式、配置和逐项 secret 字段；工厂只校验本地配置，不在启动时联网。
2. 根据厂商协议选择 `image`、`click` 或 `widget`。厂商提供 SDK 时使用 widget 及不透明 token；不要把安全校验简化为坐标格式或客户端成功标志。状态必须能绑定当前 action/挑战并在过期后失效。
3. 原生图片/点击复用共享组件；SDK 类实现增加本地 widget renderer，固定 SDK 来源并遵循 Next 的脚本生命周期，处理加载失败、销毁、过期和重复回调。公共数据逐项白名单，禁止把 `secrets` 复制到 `public_data`。
4. 在共享 composition root 导入适配包（API、worker 以及读取配置的迁移/初始化命令均导入该入口），更新 `audit/module-build-profile.json` 和离线清单校验。先通过模拟 HTTP 服务测试签发、正确/错误证据、action/站点不符、超时、异常响应、重复 token、配置轮换和并发消费，再执行真实厂商验收。
5. 配置 profile，先在隔离环境验证登录、注册、找回密码和管理员 MFA。真实厂商可用性、CSP/SDK 兼容和生产配置独立验收，不用测试提供方代替。

## 验证入口与发布边界

- `node scripts/test-captcha.mjs --unit`：提供方、配置及边界单元回归。
- `node scripts/test-captcha.mjs --integration`：要求独立回环 PostgreSQL 18 `_test` 数据库，逐入口核对 run/pass，任何 skip 阻断。
- `node --test scripts/captcha-components.test.mjs`：组件和表单协议；组件夹具不代表真实浏览器或真实厂商。
- `node scripts/check-module-binaries.mjs`：真实 API/worker 离线清单；不证明厂商服务健康。

数据库测试覆盖旧 PNG、第二图片/点击/widget 提供方、浏览器和 action 绑定、并发/重放、失败消费、配置切换与撤销。生产配置不自动修改；生产迁移和真实第三方接入需要各自部署验收。
