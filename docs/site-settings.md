# 站点设置

## 源码与范围

旧系统 `app/admin/controller/ConfigGeneralController.php` 的 `getGeneral/postGeneral`、`registerLoginPage`，`app/admin/common/server/ConfigGeneral.php` 及首页登录控制器提供品牌、公司、联系方式、Logo、备案、条款和注册方式的依据。`app/http/middleware/{Check,UserCheck}.php`、`app/home/controller/ViewBaseController.php` 提供维护公告、跳转与管理员会话豁免的依据。新系统迁移公开信息、邮箱注册开关、验证码开关和维护模式；SMTP 与短信通知在独立管理页面配置。外部条款链接不代表已记录客户法律同意。

`034_site_settings.sql` 新建单行设置，默认站点名称为智简魔方，使用旧位图生成的深色 `brand-logo.png`，注册开启。原图保留；新图保持原尺寸与透明度，在白底页面可见。不会读取或复制旧库设置、SMTP 凭据，也不会创建客户。

## API 契约

| 接口 | 权限与行为 |
| --- | --- |
| `GET /api/v1/site` | 无需登录，只返回 16 个公开字段 |
| `GET /api/v1/admin/site-settings` | 超级管理员浏览器会话；额外返回 `version/updated_at` |
| `PUT /api/v1/admin/site-settings` | 超级管理员浏览器会话；全量替换，需要 `expected_version` 与显式布尔 `registration_enabled/maintenance_enabled`；过期版本返回 409 |

公开字段为 `site_name`、`description`、`company_name`、`logo_url`、`support_email`、`support_phone`、`address`、`support_url`、`terms_url`、`privacy_url`、`record_number`、`copyright`、`registration_enabled`、`maintenance_enabled`、`maintenance_message`、`maintenance_url`。普通管理员、客户、超级管理员 API 密钥均不能访问管理接口。写请求继续要求同源及 `X-Requested-With: zjmfgo`。

站点名必填。字段限制在 store 校验；描述与地址的换行/连续空白归一，其他字段拒绝控制字符。邮箱必须是无显示名的邮箱地址。链接允许为空、单斜杠开头的站内绝对路径或 HTTPS 地址；拒绝相对路径、协议相对地址、凭据、反斜杠、空格和控制字符。Logo 由浏览器直接加载，Next Image 使用 `unoptimized`，后端不会下载外部图片。

保存锁定设置行并检查版本；更新与 `site.settings_updated` 审计在同一事务中，审计包含公开字段的前后值及版本。审计写入失败则整体回滚。

## 注册与页面

关闭注册时 `/auth/register` 返回 403 `registration_disabled`；已存在账号仍可登录。注册先检查开关，密码计算后在创建客户的事务内持有设置行共享锁并再次检查。关闭设置的事务提交后，等待该锁的注册请求会读到关闭状态；先获得共享锁的注册先完成，再关闭。

`/admin/settings` 具有服务端超级管理员门禁和版本化表单，支持失败重试与冲突后重新加载。品牌、联系方式和条款用于登录、找回密码、公共内容与工作台；页脚长内容可以换行，工作台页脚高度受限且可滚动。根布局每次请求读取公开设置，生成标题、描述和图标，不缓存旧品牌。浏览器在窗口聚焦、可见性恢复、设置保存后以及可见窗口每 30 秒重新请求；没有即时推送。站点设置不可用时注册页停止显示注册表单，API 数据库错误也不会绕过开关。

## 维护模式

迁移 035 默认为关闭，不改变已有品牌、注册状态或版本。维护开关、公告和跳转沿用设置版本与审计事务。公告最多 1000 字符，允许换行和制表符，按纯文本显示。跳转只接受外部 HTTPS 地址，拒绝凭据、无效字符、相对路径和部署 `PUBLIC_ORIGIN` 的主机名（忽略大小写和尾点）；其他别名需由部署方避免指回本站。设置保存不探测外部 URL，也不向它发起服务器请求。

| 请求/运行范围 | 开启维护后的行为 |
| --- | --- |
| 客户业务 API、公开目录与内容、注册 | 在处理请求前读取开关，返回 HTTP 503 `site_maintenance`、公开公告/`maintenance_url`，`Retry-After: 60`、不缓存 |
| 有效管理员浏览器会话 | 可预览客户接口/页面，后台仍执行原 RBAC 与范围限制；过期、停用会话不可豁免 |
| API 密钥 | 客户接口不豁免，即使同时带管理员 Cookie；管理接口继续按原权限授权 |
| 健康/就绪、公开设置、当前身份、注销 | 可用；身份接口不扩大授权 |
| 登录/MFA 与找回/重置密码 | API 可用，管理员能恢复登录；此豁免也允许客户认证/重置，但不解除业务拦截 |
| 管理 API、`/admin/*`、`/admin-login` | 可用，设置修改仍只允许超级管理员浏览器会话；管理员登录页拒绝客户落入后台并注销该会话 |
| 支付回调 | 保持验签、金额/事件校验及幂等结算，维护期间的已发起付款不会被直接丢弃 |
| worker、邮件与已排队资源任务 | 按原规则运行；维护模式是 HTTP 访问窗口，不是后台任务停止或数据库维护锁 |

Next.js `src/proxy.ts` 在页面请求前读取公开开关并复核管理员会话。客户页面显示维护公告并返回真实 HTTP 503；配置外部地址时临时 307 跳转。后台、恢复页面、静态资源与 API 同源代理不受页面门禁影响。站点读取失败时客户端页面返回暂不可用，管理员恢复入口仍可访问。维护页 HTML 由本机 Next.js `/maintenance` 渲染，Proxy 返回该文档并设置 503，避免 App Router rewrite 被重置为 200；`WEB_INTERNAL_URL` 默认为 `http://127.0.0.1:3088`，其他端口需配置该部署变量。直接 `/maintenance` 是渲染入口，维护关闭时返回首页。

已打开页面在聚焦、30 秒轮询或业务 503 时更新维护状态；管理员豁免重新查当前身份。关闭维护后公告页恢复原路径，直接维护页回首页。已开始处理的 API 请求可继续完成；本模式不承诺等待中的事务被撤销，也不能代替 schema 迁移的停服务流程。用户会话和现有数据不会因启停维护而删除。

## 验证

2026-10-05 本批集成测试覆盖公开字段白名单、会话/API 密钥边界、必填注册布尔值、链接/邮箱/字符校验、版本冲突、并发保存、审计失败回滚、注册关闭与恢复、等待锁的注册拒绝及既有客户登录。完整 `go test ./...` 在隔离 `_test` 库中通过，并强制真实旧库只读导入演练；`go vet ./...`、前端 lint/typecheck/build 通过。深色默认图修正后设置与权限回归再次通过。

隔离 Playwright 验收实际保存公开信息、连续保存版本递增、标题/描述重载、链接目标、注册关闭/恢复、冲突后加载最新版本、普通管理员页面跳转与 API 403、设置不可用时隐藏注册及恢复。桌面 1440×1000、手机 390×844 和长站名/版权/地址检查没有横向溢出或导航/页脚重叠；Logo 141×31，1710 个可见深色像素。fixture、随机 schema、3089 预览及浏览器均已清理。

开发实例已应用迁移 034，API、worker、Next.js 正常运行，健康/就绪及 root 设置页/接口通过。生产构建检查登录、注册、找回密码、新闻、知识库品牌与布局；最后浏览器无错误/警告。公司名称与 80 字符连续英文站名通过浏览器响应替换检查，没有写开发库。设置仍是版本 1、默认名称、注册开启；仅保留此前 root 和 acceptance-customer，未改变归属，也没有导入批次或设置保存审计。验收 root 会话已注销。

备份位于 `bin/previous/20261005T015626Z/`，包含 schema 033 PostgreSQL custom dump 和 API/worker/migrate 三个旧二进制；dump 目录可读取，尚未演练恢复，不代表完整切换回滚已验收。详细浏览器记录见忽略目录 `output/playwright/settings-acceptance-20261005.md`。

2026-10-05 维护模式批次：完整 Go 测试（强制隔离 PG 与真实旧库只读导入演练）、vet、前端 lint/typecheck/build 通过；最终设置/维护回归 3.120s。测试覆盖客户业务路由、无会话/客户/无效 Cookie、API key 与 Cookie 混用、管理员会话和过期/停用、后台 RBAC、公告/URL 校验、必填布尔、默认公告、支付回调验签/结算/重放、管理员 MFA 重新登录。

隔离生产构建浏览器验证实际启停与多行纯文本公告（HTML 不执行）、匿名页面 503/Retry-After、管理员登录及预览、客户被管理员入口拒绝并注销、普通管理员不能改设置、700 字公告手机布局、已打开页面恢复。外部跳转 307 与 Location 已检查，浏览器实际连接测试域名的跳转目标因网络关闭而未验证；不依赖外部状态站可达性。隔离预览因 node_modules 外部软链使用 webpack 生产构建，本目录 Turbopack 构建也通过；fixture 730.308s 后已清理，schema 与 3089 预览不存在。

开发实例已部署 schema 035，默认维护关闭、注册开启、站点版本仍 1，两个原有账号、零导入批次与零设置保存审计保持。root 从 `/admin-login` 登录并只读检查设置页，桌面/手机布局正常；登出后客户首页/新闻/知识库/登录/注册均 200、无维护页和横向溢出，后台未登录跳转 `/admin-login`。最终浏览器 0 errors / 0 warnings，验收会话注销并关闭浏览器。备份在 `bin/previous/20261005T022519Z/`（schema 034 dump、三个旧二进制、旧 web unit）；dump 目录可读取，未做恢复演练。详细证据在忽略目录 `output/playwright/maintenance-acceptance-20261005.md`。
