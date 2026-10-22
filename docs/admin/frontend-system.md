# 工作台、内容与系统页面设计

本文是后台设计规格，配合[设计总契约](../admin-frontend-design.md)和[完整页面目录](frontend-pages.md)使用；不表示下述排版、组件或交互均已实现。各页继承局部加载、权限、版本冲突、未知结果和响应式规则。所有API路径省略 `/api/v1`，共享本人API明确使用管理员门户。

## 登录、工作台与本人账户

### `/admin-login` 管理员登录

- 入口与排版：独立于后台外壳，居中单列表单最大420px；品牌、管理员登录标题、账号、密码、验证码、登录按钮、错误提示依次排列。MFA挑战成为同一表单的下一步；不混入客户注册和购物入口。
- 组件：带label的Input、密码显隐、CaptchaField、提交Button、局部Alert；验证码按当前装配能力呈现图片/点击/widget，图片不能只靠颜色辨认，换题不清账号。
- API：`POST /auth/login`、`POST /auth/login/mfa` 明确 `portal=admin`，验证码通过 `GET /auth/captcha`；成功后读取管理员身份并进入授权首页。错误不暴露账号是否存在或后台内部异常。
- 交互：校验→锁定提交→登录/MFA→校验返回的管理员身份→安全跳转。挑战过期重新开始；登录失败按协议刷新验证码。返回地址只能是同站允许的管理路径；密码和挑战不写URL或持久草稿。
- 手机：保持单列，输入和按钮至少44px，软键盘弹起后错误和提交按钮可滚动到达；不使用桌面侧栏。

### `/admin` 工作台

- 排版：标题与刷新→授权指标行→待处理事项→商品/人员业务收款排名双栏；桌面指标四列、平板两列、手机单列，排名表在手机上下排列。
- 组件：Metric、状态Badge、待办列表、月份选择器、Top N选择器、排名表、各区Skeleton/Retry；卡片有可读标题、口径和更新时间，数字链接保留对应筛选条件。
- API与权限：`GET /admin/dashboard` 要求 `dashboard.view`；`GET /admin/reports/product-ranking`、`staff-ranking` 要求 `reports.view`。缺排名权限只隐藏或明确标记该区，不能让整个工作台失败。
- 交互：每区独立读取，年月只影响排名区；快切月份丢弃旧响应。排名显示上海自然月、件数和业务收款，不能改名为外部现金、利润或提成。无工单权限显示不可用而非0。
- 点击待办进入实际列表/详情，无真实数据或入口的卡片不展示演示数字。汇总与明细范围不一致时说明口径，不用当前页数据计算总额。

### `/admin/account` 个人账户

- 排版：身份摘要→密码安全→本人MFA→本人通知偏好；桌面安全表单与说明两栏，手机垂直排列。与系统MFA政策页分开。
- API：`GET /me`、`PUT /account/password`、`GET /account/mfa` 及其 enroll/confirm/disable 动作、`GET/PATCH /me/ticket-notification-preferences`；使用管理员门户与本人会话，不读取客户资料和钱包。
- 组件与交互：密码输入、MFA启用步骤和一次性材料、偏好开关各自提交；MFA未confirm不得显示已启用；秘密仅短暂展示、不缓存。换密/MFA撤销行为以认证API结果为准；会话失效转管理登录。
- 偏好保存失败保留安全输入；离开一次性材料界面前提示。退出只调用管理员门户 `POST /auth/logout`，不清除客户Cookie。

## 内容与官网呈现

下列列表沿“标题/新增→筛选→表格→分页”排布；编辑器在同页抽屉或编辑状态中呈现，不能据此虚构尚不存在的 `/new` 或详情路由。返回列表恢复内容类型、筛选和页码。权限与字段以[内容管理](../content-management.md)、[分类标签](../content-taxonomy.md)、[下载中心](../download-center.md)为准。

### `/admin/content` 新闻与知识库

- 排版：新闻/知识库页签→状态/分类/标签/关键词筛选→标题、发布状态、分类、时间、操作表；类别/标签管理置次要入口。编辑状态为标题/标识→正文→分类标签→发布与SEO，桌面正文占主列、发布信息侧栏，手机单列。
- 组件：类型Tabs、内容表格、受控富文本或Markdown编辑器、标签选择器、图片上传、发布状态、删除确认。内容预览默认转义或隔离，不能把上游HTML直接执行。
- API：`GET/POST /admin/content`、`GET/PUT/DELETE /admin/content/{id}`，分类 `/admin/content-categories`、标签 `/admin/content-tags`，图片 `POST /admin/content/{id}/images`；读取 `content.view`、写入 `content.manage`。正文保存规范图片地址 `/content-images/{id}`，后台预览映射 `/admin/content-images/{id}`，不能把仅管理端可读的图片URL存成公开正文地址。
- 交互：读取详情与版本→编辑→保存→重读详情/列表；保存前校验必填、公开标识和链接安全。图片上传与文章保存分别显示结果，失败不伪称发布成功；删除确认列文章身份并回列表当前有效页。

### `/admin/downloads` 下载中心

- 排版：分类侧树约240px＋文件主表；主表列名称、分类、可见范围、状态、文件信息和操作。编辑抽屉按文件来源、分类、访问条件、关联商品分组；设置/分类管理各自独立提交。
- 组件：分类树、文件表、商品选择、上传/替换面板、授权下载按钮、上传结果。手机分类折叠为选择器，文件改主字段卡片。
- API：`/admin/downloads`、`/admin/downloads/{id}`、`/admin/download-categories`、`/admin/download-settings`、`/admin/download-products`；具体method见注册目录。读取 `downloads.view`、写入 `downloads.manage`。
- 交互：上传完成不等于已保存下载条目；未知上传遵循存储协议停止盲目重传。下载只走授权业务文件地址，替换后重新读取文件版本；分类被引用时展示API拒绝原因，不删除关联事实。

### 官网配置各页

| 页面 | 排版与组件 | API与权限 | 交互及刷新 |
| --- | --- | --- | --- |
| `/admin/theme-settings` 主题 | 固定预设选项→主色、Logo样式、纯文本页脚；右侧非交互预览、下方保存条 | `GET/PUT /admin/theme-settings`；`settings.view/manage` | 校验颜色；保存后重读主题与公开站点；预览不保存，后台样式独立；不上传代码或任意CSS |
| `/admin/site-fields` 官网字段 | 字段表（名称、标识、类型、状态、顺序）＋编辑抽屉 | `GET/POST /admin/site-fields`、`PATCH/DELETE /admin/site-fields/{id}`；`content.view/manage` | 仅呈现API支持的字段类型，删除二次确认；成功重读字段，不与客户自定义字段混用 |
| `/admin/navigation` 官网导航 | 位置/层级列表＋名称、目标、顺序、状态表单；排序使用明确顺序输入，拖动只可作同一写入的增强 | `/admin/navigation` 及 `/{id}`；`content.view/manage` | 校验URL与父子关系；保存后刷新导航；未保存拖动不得显示已生效，手机可用按钮调整顺序 |
| `/admin/friendly-links` 友情链接 | 名称、URL、显示顺序、状态列表＋轻量编辑Dialog | `/admin/friendly-links` 及 `/{id}`；`content.view/manage` | 校验链接协议，外链安全打开；启停/删除后刷新列表，危险操作说明对官网的影响 |

## 设置表单

通用排版为页头/重新加载→分区表单（桌面两列、最大960px）→保存/取消→局部反馈；手机单列底部操作不遮挡最后字段。只读角色看到定义列表和说明，管理权限决定可写。字段取对应响应与DTO，不把部署环境的数据库连接、主密钥、监听端口做成表单。

| 页面 | 必需分区及组件 | API与权限 | 具体交互 |
| --- | --- | --- | --- |
| `/admin/settings` 站点设置 | 名称/品牌、公司与联系信息、维护及公开展示；文本输入、开关、维护说明 | `GET/PUT /admin/site-settings`；`settings.view/manage` | 用本页version；保存更新baseline，取消恢复最近保存值；维护开关确认影响，禁止带回信用/合同开关 |
| `/admin/site-settings` 站点设置兼容入口 | 复用上一页同一表单和草稿命名空间，维持来源返回路径 | 同上 | 不维护第二套站点设置；若后续统一为跳转，保留合法分区参数并测试旧书签 |
| `/admin/order-settings` 订购设置 | 下单需验证手机、下单需实名认证两项开关＋实名模块就绪/遗留策略只读说明 | `GET/PUT /admin/order-settings`；`settings.view/manage` | 直接保存本接口的 `require_verified_phone/require_certification` 与版本；其他实名策略链接独立页面，不跨页偷偷保存 |
| `/admin/auth-settings` 登录注册与验证码 | 注册登录、验证渠道、验证码实例/能力和管理员防暴力策略；Tabs、开关、能力提示 | `GET/PUT /admin/auth-settings`；`settings.view/manage` | 采用本端点，不能因旧表单曾共用site-settings而混写；挑战能力来自已装配模块，新提供方未装配不显示为可用 |
| `/admin/security/mfa` MFA政策 | 客户/管理员是否允许MFA两区，说明已有绑定与登录影响 | `GET/PUT /admin/security/mfa`；`settings.view/manage` | 这是全局政策，不在这里扫描本人二维码或替其他管理员绑定；保存后重读政策 |
| `/admin/certification-settings` 实名策略 | 实名开关、默认实例、API已有个人/企业及资格设置，渠道就绪状态；不添加有效期字段 | `GET/PUT /admin/certification-settings`；`certifications.view/manage` | 实例选项使用渠道目录实际能力；配置保存与外部认证成功分开，不假设人工提供方具有自动核验能力 |
| `/admin/finance-settings` 充值与财务 | 充值开关、上下限及API允许的财务策略；金额Input与规则提示 | `GET/PUT /admin/finance-settings`；`settings.view/manage` | 金额转整数分、上下限互校验；不展示信用授信或把余额充值当业务收入 |
| `/admin/invoice-settings` 发票设置 | 抬头/税务与发票政策、邮寄方式表和方式编辑器 | `/admin/invoice-settings`、`/admin/invoice-shipping-methods` 及 `/{id}`；`settings.view/manage` | 设置与邮寄方式分别保存，分区成功不掩盖另一区失败；不影响已有发票事实 |
| `/admin/currencies` 币种展示配置 | 币种代码、符号、显示格式、默认标记表＋编辑与设默认确认 | `/admin/currencies` 及 `/{id}`、`POST .../{id}/default`；`settings.view/manage` | 明示当前账本CNY；展示配置不是多币种结算或换汇，默认切换只按实际API效果说明 |
| `/admin/sales-settings` 销售规则 | 全局规则、员工、提成组、阶梯各页签；规则表/员工编辑抽屉 | `/admin/sales-settings`、`sales-staff`、`sales-commission-groups`、`sales-commission-tiers`；`sales.view/manage` | 不同规则按自身版本独立保存；预计与历史确认分开，改规则不能让历史已确认金额变动，正式结算转销售结算页 |

### 邮件、短信与支付设置入口

`/admin/smtp-settings`、`/admin/sms-settings`、`/admin/payment-settings` 当前均进入渠道实例工作台的对应类别。设计复用[运营页面的渠道规格](frontend-operations.md)，不能再做三套独立的网关表单；页面标题、默认类别与返回路径不同。

排版为类别说明→实例表（类型、名称、启用/配置状态、能力）→按配置schema生成的编辑抽屉；这些入口为超级管理员专用。读取使用 `GET /admin/channel-catalogue` 与 `GET /admin/channel-instances`，创建 `POST /admin/channel-instances`，更新/删除 `PUT/DELETE /admin/channel-instances/{id}`；`/admin/channels` 是页面路由，不能当作API。遵循[渠道设置](../admin-channel-settings.md)和模块契约；旧单提供方设置仅按其既有兼容协议保留，不推断渠道目录替它接管全部历史订单。

密钥显示“已配置”而不是假掩码当原值回填；保留、替换、清除是三个明确意图，空值语义按各提供方协议。保存失败不缓存秘密；试发/查单/退款/核验仅在模块真实声明并授权时出现。HTTP成功不显示“收件成功”或“已原路退款”。手机实例表改卡片、schema字段单列。

## 管理员、角色与安全

### `/admin/access` 管理员与权限

- 排版：管理员/角色Tabs；管理员页为搜索→姓名、账号、角色、启用状态、范围、操作表；角色页为名称列表＋按领域分组的权限表与客户/部门范围说明。新增管理员和角色编辑分别开表单，不混成一键保存。
- API：`GET /admin/permissions`、`GET/POST /admin/roles`、`PATCH/DELETE /admin/roles/{id}`、`GET/POST /admin/staff`；超级管理员浏览器会话。权限目录不得前端硬编码成允许更多操作。
- 组件与交互：权限分组Checkbox、范围选择器、状态Badge、保存/删除确认；全选仅作用于当前明确展示的权限组。受保护超级管理员、自身降权及最后管理员限制显示服务端原因；保存后重读角色/员工和当前身份。
- 手机：管理员表改卡片，权限编辑全屏Sheet；可访问地逐组选择，不靠仅鼠标拖拽。

### `/admin/access/staff/[id]` 管理员编辑

- 入口：管理员表编辑；返回恢复原搜索/页码。页头员工身份，分区为角色与状态、客户范围、工单范围及API支持的安全设置；桌面表单主列＋影响说明，手机单列。
- API：`GET/PATCH /admin/staff/{id}`，角色/部门选项分别读取对应接口；超级管理员会话。不从普通客户表取资料或显示客户钱包/购买功能。
- 提交带本员工资源版本；权限变化和撤权后重读身份与范围。密码不回显；409保留安全草稿，范围缺失不能推断为空集或全局。服务端404按不存在或无权访问处理。

### `/admin/blacklist` 登录黑名单

- 排版：搜索/生效状态→阻断对象/原因、操作、来源IP、上海解封时间、状态表→服务端分页；新增使用模板Dialog，解除沿用确认。API为 `/admin/blacklist` 及 `/{id}`，读 `security.view`、写 `security.manage`；本页不提供编辑入口。
- 沿用服务端IP/标识和期限校验、创建key及解除expected_version/key。留空期限为永久，相同对象新增会更新原规则；自动阻断阈值/期限由安全设置决定，不硬编码为三次/三小时。分页支持25/50/100条（默认50），搜索/状态提交及页大小变更回第一页；最后一页解除唯一记录时回退一页。读取错误/403隐藏旧行、总数和分页，保持查询草稿及新增表单挂载。手机改卡片展示原因和到期，等待服务器成功后刷新，不把前端移除行当作规则已解除。

## 日志、队列与调度

### 日志各页

各页从相应业务详情或系统导航进入；固定客户上下文时不可清除成全局。排版为标题/刷新→API支持的时间、对象、状态筛选→时间/对象/动作/状态/摘要表→分页→脱敏详情抽屉。JSON或长日志仅在容器内滚动，手机按时间和对象改卡片；不增加通用导出。

| 页面 | 读取API与权限 | 特有字段/组件与交互 |
| --- | --- | --- |
| `/admin/audit` 操作审计 | `GET /admin/audit`；`audit.view` | 时间、操作、操作者、对象及记录ID；本页仅展示元数据，不渲染原始payload，不允许编辑/删除 |
| `/admin/logs/[kind]` 系统日志及兼容入口 | `kind=system` 读 `GET /admin/logs`；`audit.view` | 审计元数据的时间、actor、action、entity及编号，不虚构严重性level；login/cron/message分别跳管理员登录/任务日志/站内信日志，其他kind返回404，不将任意kind拼到接口 |
| `/admin/logs/admin-login` 管理员登录 | `GET /admin/logs/admin-login`；`audit.view` | 时间、账号、IP、成功/失败及安全原因；客户登录事件不得混成管理员登录 |
| `/admin/logs/api` API日志 | `GET /admin/logs/api`；`audit.view` | API调用对象、时间、结果和可用关联编号；敏感参数/Token不展示，范围和筛选服从该端点 |
| `/admin/logs/email` 邮件日志 | `GET /admin/logs/email`；`notifications.view` | 脱敏收件人、模板/事件、投递结果，不因HTTP接受就标记最终送达 |
| `/admin/logs/sms` 短信日志 | `GET /admin/logs/sms`；`notifications.view` | 脱敏号码、提供方、发送结果；验证码正文不展示 |
| `/admin/logs/inbox` 站内信日志 | `GET /admin/logs/inbox`；`notifications.view` | 收件客户、主题、状态；保留原渠道含义，不把读状态当外部投递成功 |
| `/admin/logs/jobs` 调度日志 | `GET /admin/logs/jobs`、`/admin/logs/jobs/trend`；超级管理员 | 任务键/目录名称、开始结束、耗时、结果/计数及安全错误码；趋势与列表独立加载，失败不伪造成零次执行 |

当前系统审计、管理员登录、API、邮件、短信和站内信六页已适配公共列表、分页及手机卡片；固定客户/工单范围下，清空仅重置可见筛选，不显示会丢失范围的跨渠道快捷导航。邮件和短信按原始状态展示待处理、发送中、已发送、过期与失败；已发送不等于最终送达，短信正文仍为加密占位。调度日志另已接入同一公共列表和手机卡片，执行明细与近31天趋势独立加载/错误/重试，保留任务目录、未知任务键纯文本、上海日期及原URL筛选/分页；列表失败时所选任务仍可辨识。独立操作审计页也已使用公共列表/手机卡片，保留`/admin/audit`数组接口和每页25条、零基URL页码；API不提供总数/搜索，不推算总数或提供无效筛选。错误/403隐藏旧行和分页，原始payload不放入可见内容。详情抽屉和清理操作尚未适配。

通知统一目录历史、Telegram/Web Push记录以及工单/营销日志按[运营规格](frontend-operations.md)展示；旧邮件/短信日志不能被当作所有渠道完整历史。业务对象已转移或当前范围失效后，日志按实际归属限制遮蔽，不通过跳全局页绕过。

### `/admin/logs/cleanup` 日志清理

- 排版：类型选择→保留截止时间→不可恢复影响说明→明确确认；支持的 `log_type` 为audit/email/sms/scheduler，无预估API不显示猜测条数。财务业务历史不属于日志清理；受保护事实触发数据库拒绝时展示原因，不提供绕过入口。
- API：`POST /admin/logs/cleanup`，超级管理员；提交 `log_type`、RFC3339 `before`、`acknowledged=true` 和8–128字符稳定 `Idempotency-Key`。截止至少早于24小时前。Dialog显示类型/截止时间，提交中锁定参数，成功显示实际 `deleted` 并刷新日志。未知保留原key/body按原请求恢复；原回执并不表示新的截止范围也已清理。
- 手机：纵向策略表单和显式确认，不通过图标误触。

### `/admin/jobs` 任务概览与 `/admin/jobs/queue` 队列

- 概览排版：页头/队列入口/手动刷新→按来源的总数、待处理、运行中、等待人工/核验及失败/死信汇总；`GET /admin/jobs`，`jobs.view`。本端点不含最近任务或worker心跳，不伪造健康仪表盘。超级管理员另以 `GET /admin/runtime` 独立加载后台执行状态，任务汇总失败仍可刷新它；普通jobs.view用户不请求runtime，也不显示调度日志入口。
- 队列排版：关键词/来源/状态/开始及结束日期→客户、商品、动作、描述、来源、状态、更新时间、服务链接表→服务端分页；`GET /admin/jobs/queue`，`jobs.view`。沿用即时筛选、URL及popstate恢复，筛选/页大小改变重置offset；分页取响应limit/offset，默认25项，可选25/50/100。日期条件沿用后端既有解析，本批不改变时区口径。
- 两页复用 `AdminListPage`、`AdminListRegion` 和 `ResourceTable`；失败/403不展示旧行、总数和分页；401沿用全局逻辑跳转管理员登录页。队列用来源与ID组合行键，避免不同队列ID重叠；来源、动作和状态有未知兜底。手机汇总和队列均改卡片，长客户/商品可换行。
- 错误只给 `has_error` 核验提示，不展示错误正文、原始结果、凭据、尝试数或可执行时间。服务链接仅在服务操作任务有service_id且具备 `services.view` 时显示，不新增客户链接或元信息抽屉。“已发送”不等于最终送达。
- 只提供手动刷新及已有核验入口，无自动轮询、“重试全部”或立即执行操作。GET不触发执行；未知外部结果继续进入原业务页核验，不绕过状态机。

### `/admin/job-settings` 调度配置

- 排版：自动提醒总开关→到期提醒提前天数→账单逾期提醒宽限天数→保存/取消与结果；内部任务目录只读呈现固定执行周期/触发来源，不为每个任务画启停和频率表单。
- API：`GET/PUT /admin/scheduler-settings`，超级管理员；仅编辑 `enabled`、`settings.expiry_days`、`settings.billing_grace_days`，提交 `expected_version` 和稳定key；组件为Checkbox、天数组输入、数值输入和版本确认，详见[调度契约](../architecture/scheduler.md)。
- 交互：到期天数按1–365且不重复校验，空数组表示关闭到期提醒，宽限期0–365；确认→保存→重读。总开关仅按现有提醒语义说明，不称关闭全部worker。并发冲突重读，未知保留原body/key；不把配置保存或最近tick当作业务已执行，不提供cron/脚本或逐任务频率修改。

## 运行信息与迁移复核

### 系统只读各页

| 页面 | 排版与组件 | API/权限 | 行为边界 |
| --- | --- | --- | --- |
| `/admin/system/runtime` 运行环境 | Go版本/OS/架构、goroutine、数据库版本/大小/迁移和构建定义列表，刷新及采样时间 | `GET /admin/system/runtime`；`settings.view` | 无PHP扩展清单或worker心跳；运行信息不暴露连接串/环境变量，不提供修改部署配置 |
| `/admin/system/database` 数据库状态 | 容量/连接池摘要→名称搜索→表统计表→分页；长名称换行 | `GET /admin/system/database`；`settings.view` | 只读统计，按服务端计数标注估算含义；不添加SQL编辑器、建表或下载数据库按钮 |
| `/admin/system/about` 关于系统 | 产品、backend/frontend技术栈说明、Go与数据库版本、构建版本/运行契约定义列表 | `GET /admin/system/about`；`settings.view` | backend/frontend不是独立API/Web发布版本；build_version缺失显示未设置，不将仓库HEAD推断成正在运行的发布版本 |
| `/admin/system/upgrade` 升级检查 | 当前状态→已应用/待应用数量→待迁移列表→刷新 | `GET /admin/system/upgrade`；`settings.view` | 仅比较数据库与编译迁移清单，迁移由部署流程执行；没有一键升级、混跑或回退旧二进制按钮 |

以上页面桌面两列定义列表，手机单列；采样失败保留旧值时必须标明“上次结果”和时间，不能继续显示绿色“实时正常”。

### `/admin/migrations` 旧数据复核

- 排版：导入运行摘要→实体/复核状态等受支持筛选→来源/目标、差异、复核状态表→明细抽屉（来源摘要/当前值/解除隔离阻断原因）→“确认归档”或“恢复业务”；不是浏览器上传任意旧库的迁移器。
- API：`GET /admin/migrations/runs`、`GET /admin/migrations/reviews`、`GET/POST /admin/migrations/reviews/{entity}/{id}`；读取 `migrations.view`，写入限定超级管理员会话，不虚构 `migrations.manage` 权限，详见[迁移契约](../legacy-migration.md)。
- 交互：读取当前差异与 `current.version`→选择 `decision=archive` 保持隔离归档，或满足 `release_blockers` 后 `release` 恢复业务→填写 `evidence`→二次确认→按原 `version` 与稳定key提交→重读该条、列表及运行摘要。服务release需按协议填写核验状态、真实资源标识/金额；不把它当作任意改状态入口。冻结/范围/冲突保留依据，未知按原意图恢复，不能前端跳过门禁。
- 手机：差异上下对照，依据和确认按钮可滚动到达，归档与恢复使用明确文字和影响说明。

## 退役页面与验收

`/admin/credit`、`/admin/credit-settings`、`/admin/contracts`、`/admin/contract-settings` 保持404，不放导航、占位卡或恢复按钮；相应旧API鉴权后410。页面目录列出它们只为避免遗漏旧链接清理，不代表需要重新设计业务。

本域验收至少覆盖管理员独立登录/MFA/退出、授权首页选择、分区失败、只读设置、409安全草稿、密钥不回填、日志范围与兼容跳转、任务未知结果不重发、升级只读及四个退役入口。视口和键盘/焦点要求按设计总契约执行。当前新增内容仅为设计文档，浏览器、真实投递、迁移与部署均未在本轮执行。
