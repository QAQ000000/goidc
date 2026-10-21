# 后台客户、订单与服务页面设计

状态：待实现的页面与交互设计；不表示这些页面已接线、完成浏览器验收或部署。视觉令牌、外壳、公共组件、可访问性及通用状态沿用[前端设计总契约](../admin-frontend-design.md)，请求协议以[后台 API](api.md)、[客户 API](customer-api.md)、[服务管理 API](service-management-api.md)为准。

本文覆盖17个已有页面路由及其明确页签/弹窗。文中的 API 省略 `/api/v1` 前缀；浏览器使用现有 `api()`，不能重复前缀。页面参数 `[id]` 是路由占位，不是可由搜索框替代的客户范围。库存和商品另有领域设计，客户资源池不属于服务器库存。

## 装配和交互规则

- 桌面采用“页面头＋筛选工具栏＋主数据区＋分页”，详情主区与320px关联摘要并排；手机按身份、状态、正文、操作排列，表格仅在自身容器滚动。
- 通用组件使用 `Workspace`、面包屑、`Button/Input/Select/Checkbox/Textarea`、`Table`、分页、`Tabs`、`Sheet/Dialog/AlertDialog`、状态徽标、金额/时间单元格、局部骨架和错误反馈；这些是设计组件职责，不要求新增同名业务组件。
- 服务已装系统与真实地域复用[总契约第12节](../admin-frontend-design.md#12-现有图片素材与图标)的现有系统图标和国家/地区旗帜，保留文字及缺图回退；不能根据素材推断可安装系统或可用地域。
- 列表条件与页签用可恢复URL状态保存；更改筛选回第一页。筛选、分页或身份变化清空批量选择，旧响应不得覆盖新客户/新管理员状态。未支持的筛选不渲染成假控件。
- 各分区独立请求、加载和重试；401为登录失效，403为权限不足，404为不存在或不在范围，不能全部转换成“暂无数据”。所有金额以整数分接收，展示两位小数；空值不显示为0。
- 有回执协议的写操作按一次意图保存原请求体和稳定8–128字符 `Idempotency-Key`；超时保留原键原参数。409保留草稿，读取最新基线后重新核对，禁止自动覆盖版本或换键重发。
- 没有幂等回执的动作不伪装成可安全自动重试；标记“结果待核对”，先查当前状态和审计。回执只证明当时提交的事实，成功后重新读取实时对象。
- 仅保存恢复所需的非秘密意图；密码、文件字节、资源凭据和身份证资料不写URL、浏览器持久存储或日志。失效管理员身份立即停用旧动作并清理敏感草稿。

## 页面一览与入口关系

| 页面 | 目的与主要入口 | 返回/关联 |
| --- | --- | --- |
| `/admin/customers` | 客户导航入口；查找和管理授权客户 | 工作台；客户详情、新建 |
| `/admin/customers/new` | 创建普通客户 | 保留筛选的客户列表；成功进详情 |
| `/admin/customers/[id]` | 固定客户的运营工作区 | 客户列表；关联服务、订单、账单、工单 |
| `/admin/customers/[id]/files` | 固定客户的文件管理 | 同客户详情的文件页签 |
| `/admin/customer-groups` | 客户组及新购折扣规则 | 客户列表；按组筛选 |
| `/admin/customer-levels` | 客户条件规则及匹配数量 | 客户列表；按规则筛选 |
| `/admin/customer-fields` | 客户字段定义管理 | 客户列表；字段预览 |
| `/admin/customer-files` | 授权范围内的客户专属文件 | 客户详情；业务文件下载 |
| `/admin/file-groups` | 公共下载中心分类 | 公共下载管理；不归类专属文件 |
| `/admin/customer-resources` | 尚未分配销售的客户池 | 客户列表；销售归属 |
| `/admin/certifications` | 实名申请查阅和人工审核 | 客户摘要；详情抽屉内历史 |
| `/admin/services` | 服务筛选与同客户批量代续费 | 服务详情、客户详情 |
| `/admin/services/[id]` | 服务运行、期限、续费及管理动作 | 原服务列表；客户、订单、账单 |
| `/admin/orders` | 新购订单、审核与批量操作 | 订单详情、代下单 |
| `/admin/orders/new` | 用管理员身份为指定客户代下单 | 原列表/客户详情；成功进订单 |
| `/admin/orders/[id]` | 核对交易快照、审核和备注 | 原列表；账单、服务 |
| `/admin/cancellations` | 处理既有取消记录 | 关联服务；不新增客户自助入口 |

## 客户列表 `/admin/customers`

目标是快速定位客户并进入固定身份工作区；不在一行内塞入充值、推荐人、重置密码等全部写操作。

- 排版：标题右侧“新建客户”；一行关键词/状态/分组/销售，折叠高级筛选；下方客户表格及分页。主列为编号、姓名/账号/邮箱、启停状态、客户组、销售、注册时间、进入详情；需要显示余额时明确为钱包余额，不把旧 `income_cents` 标成实收。
- 组件：搜索表单、客户组/销售远程选择器、状态选择、筛选标签、字段类型输入、可复制编号、状态徽标、行菜单、分页。
- API：`GET /admin/customers/page`，`customers.view`；高级条件支持公司、资料手机、QQ、认证状态、等级条件、自定义字段，精确参数及空值规则见[筛选协议](customer-api.md#客户列表和高级筛选)。自定义字段读取 `/admin/customer-field-definitions`；无对应目录权限不强行预载其他管理页。
- 交互：回车/查询才应用草稿；点击客户保留列表查询；“无结果”允许清空条件。`level_id` 是满足某项规则，不表示客户唯一等级；资料手机不显示登录手机验证徽标。
- 行写操作若保留启停/分组/销售快捷入口，复用详情的独立版本与恢复流程；成功刷新本行及列表，因范围变化消失时提示“已更新，当前列表不再包含此客户”。
- 手机：客户身份＋状态作为卡片首行，分组/销售为次行，筛选进入Sheet，主动作保留“查看客户”。

## 新建客户 `/admin/customers/new`

- 排版：头部返回和“创建客户”；主区账号、资料、动态字段三段，右侧说明普通客户身份及可选分组/销售；底部提交栏。手机合为单列，密码字段不吸附复制。
- 组件：邮箱、可选用户名/显示名、密码输入/显隐、启用状态、类型化资料字段、分组选择；销售仅超级管理员显示，缺权限不提交对应字段。
- API：`GET /admin/customer-field-definitions` 取得完整定义和 `definitions_version`；`POST /admin/customers` 要求 `customers.manage`、稳定键、必填email/password；分组另需分组管理权限，销售另需超级管理员会话，见[客户写协议](customer-api.md#新建编辑启停和字段定义)。
- 交互：先完成定义分页，再校验必填字段并带 `custom_fields_version`；创建前核对邮箱、普通客户身份和分组。密码8–72字节不等同字符数；服务端结果为准。
- 成功进入返回的客户详情，刷新列表目录；定义变化保留安全资料草稿、重新读取并核对新规则。结果未知保留当前页面的原意图，避免自动再创建；离开时不得持久保存明文密码。

## 客户详情 `/admin/customers/[id]`

桌面头部固定客户编号、姓名、账号、启停状态与返回列表；主区固定页签，右侧仅展示当前客户身份/销售/分组与有权限的动作。公共头与摘要、资料、当前页签分别读取，隐藏页签失败不遮挡工作区。手机身份优先，摘要栏下移折叠，页签横向滚动或Select切换。

| 页签 | 排版和主要组件 | 确定的读取 API / 权限 | 写入与刷新 |
| --- | --- | --- | --- |
| 摘要 | 服务/未付账单/工单指标；钱包与现金分两组；实名与推广状态 | `GET /admin/customers/{id}/summary`，`customers.view`；各section另检业务权限 | `available=false` 显示无权限；`data=null`不渲染0。显示 `as_of`，刷新摘要不触发业务 |
| 资料 | 账号、联系方式、公司/地址、内部备注、自定义字段分段；联系人表独立分区 | `GET /admin/customers/{id}` 与字段定义，`customers.view`；`GET .../contacts`同权限 | 保存资料用PATCH与profile_version；联系人独立版本/键，不能整页合并保存 |
| 服务 | 状态/服务导航组/关键词筛选、服务表、同客户选择与批量工具栏；不提供未支持的商品/期限全库筛选 | `GET /admin/services/page?customer_id={id}`，`services.view`；参数仅customer_id/group_id/status/q/limit/offset | 进入服务详情；单/批量代续费见下文。身份锁定，不能清空customer_id |
| 账单 | 账单号、类型、状态、应付、到期/付款时间；订单作为关联区 | `GET /admin/invoices/page?customer_id={id}`，`invoices.view`；订单 `/admin/orders/page` 要求 `orders.view` | 人工账单、代下单、代建充值链接固定客户；退款进入原账单流程 |
| 交易 | 现金汇总独立于钱包；筛选栏＋交易表，按来源/方向标注 | `GET /admin/transactions/page` 与 `/summary`带customer_id，`invoices.view` | 两接口使用完全相同筛选；分页金额不当合计；event_key作稳定行键 |
| 工单 | 状态/部门条件＋工单列表＋代建入口 | `GET /admin/tickets/page?customer_id={id}`，`tickets.view`及部门范围 | 代建需tickets.manage，成功刷新工单与摘要；无权限不写“暂无工单” |
| 日志 | 日期/动作搜索；时间、动作、对象、操作者；元数据时间线 | `GET /admin/logs?kind=system&customer_id={id}`，`audit.view`，返回分页结构 | 只读；不按actor等于客户推断归属，不显示原始payload |
| 通知 | 统一投递/历史邮件/短信/站内信分区，渠道、状态、期间筛选 | `GET /admin/notification-deliveries/page?customer_id={id}`，`notifications.view`及后台会话；旧日志见[后台契约](api.md#客户操作日志与通知历史) | accepted只称渠道接受；unknown显示待核验，无重发按钮；分区独立分页 |
| 文件 | 专属文件表及上传入口 | `GET /admin/customer-files?customer_id={id}`，`customer_files.view` | 上传需customer_files.manage；客户固定，完整流程见文件页面 |
| 推广 | 当前推荐人、调整历史、该客户推广活动分区 | `GET .../{id}/affiliate/page`要求customers.view；推荐人读写另需customers.manage＋affiliates.manage | 选择候选/清除、原因、referral_version与稳定键；只影响以后新建账单 |

当前客户详情已接代建未付充值单及推荐人面板，创建成功不改变钱包；推荐人候选与调整历史分别分页。三种新建页仅按各自创建权限精确读取传入客户，非法、重复或范围外编号阻断创建，不能回退为全客户选择；返回地址只接受受控后台路径。写入和接受回执前均核对真实管理员身份。未知结果冻结原客户、载荷和键；刷新或权限重挂载后先恢复核验。购买字段和工单正文可能含凭据，仅在原页面内存中保存原请求，持久标记不存这些内容，工单草稿只留标题和优先级；原页丢失后需先到业务列表核对，不能自动换键重建。

资料与高风险客户动作使用各自Dialog，不使用能任意编辑所有字段的通用JSON面板：

| 动作 | 步骤 / API | 版本、成功与未知恢复 |
| --- | --- | --- |
| 编辑/启停 | 核对稀疏变化 → `PATCH /admin/customers/{id}`；customers.manage | expected_version=profile_version；成功刷资料/摘要/列表；停用撤销客户会话；409不覆盖 |
| 联系人 | 列表 → 新建/编辑抽屉 → `POST .../contacts`、`PATCH/DELETE .../contacts/{contact_id}` | customers.manage；新UI总传键及已有联系人version，成功刷新联系人；联系人不称可登录子账号 |
| 调分组 | `GET .../{id}/group` → 选择或主动移出 → `PUT`同路径 | customer_groups.manage；group_membership_version专用；成功刷组/资料/摘要，不重算旧服务价格 |
| 销售分配 | 读 `/admin/sales-owners` → 明确人员或清空 → `PUT .../{id}/sales-owner` | 仅超级管理员会话；sales_version＋键；与资源池共用待确认意图，完成后重读当前范围 |
| 直接钱包充值 | 金额、备注 → 明确“立即入钱包” → `POST .../{id}/balance`，balance.manage | 未知先 `GET .../balance-operation`带原键；unconfirmed不等于未执行；确认后刷钱包/交易 |
| 代建充值单 | `GET .../{id}/topup-options` → 金额/原因 → `POST .../{id}/topups` | invoices.view/manage；设置version＋键；只建未付单不增加钱包，刷账单/摘要，实时状态另读账单 |
| 推荐人调整 | 读referrer/options/history → 候选或清除 → 原因和未来生效说明 → PUT referrer | customers.manage＋affiliates.manage；referral_version＋键；原账单/佣金不改，成功刷关系/历史 |
| 重置密码 | 新密码＋二次确认 → `POST .../{id}/password`，customers.manage | 不显示旧密码；会撤销旧客户凭据；无专用结果查询，不自动重复重置；清除密码草稿 |
| 受控代登录 | 解释客户身份和2小时边界 → `POST .../{id}/impersonate` → 新客户门户页面 | customers.manage且管理员浏览器会话；保留独立管理员Cookie；代登录横幅/退出按[身份边界](../admin-rbac.md#前后台身份边界)，不代签长期API密钥 |

推广页签不得用普通客户PATCH变更推荐人；资料修改不得写认证结论。信用额度、合同、独立CRM跟进、自助退款/停用申请不出现在页签或快捷动作中；保留内部备注和工单入口。

## 客户专属文件 `/admin/customer-files` 与 `/admin/customers/[id]/files`

两页共享文件工作区，但后者头部必须保留客户身份和“返回客户”；从详情文件页签进入再返回时恢复原客户和筛选。

- 排版：头部上传；全局页有授权客户选择器，固定客户页使用不可清除的身份条；搜索名称/备注/文件名；文件表列名称/原文件名、客户（仅全局）、类型/大小、上传时间/管理员、下载计数、操作；底部分页和选择摘要。
- 组件：文件选择、上传进度、元数据表单、可选替换文件、下载按钮、版本删除确认、明确选择项的批量删除Dialog。手机按文件卡片显示名称/大小/客户，操作保持文字。
- API/权限：读取 `GET /admin/customer-files`、`/{id}`、`/owners`、`/{id}/file` 要求customer_files.view；上传 `POST`集合、编辑 `PUT /{id}`、删除 `DELETE /{id}`、批删 `POST /bulk-delete` 要求customer_files.manage，详见[客户文件](../customer-files.md)。
- 交互：上传/编辑用multipart，metadata明确customer_id/title/remarks/expected_version；编辑无file表示保留原字节，不把空文件作为“保留”。固定客户页不提供归属切换；全局转移须同时拥有两侧范围。
- 成功刷新文件页、计数及客户详情文件分区；下载走授权接口，不拼存储URL，不当作数据导出。删除与批删均确认具体文件及版本，1–100项原子提交，任一冲突不展示部分成功。
- 本协议没有通用上传回执恢复保证；断网保留待核对状态，查列表/详情与审计，不套用工单附件上传的专属恢复接口或自动再上传。替换/转移不撤回已经授权返回的字节。

## 客户分组 `/admin/customer-groups`

- 排版：组列表与选中组规则区；头部新建，表格列名称/颜色、默认减免、拆分账单设置、版本/操作；选中后右区显示商品组覆盖规则。手机列表先行，规则区Sheet内独立保存。
- 组件：颜色输入、百分比/固定金额编辑、客户组和商品组选择器、规则表、版本删除确认；百分比明确“减免20%＝支付八折”，不能标“售价20%”。
- API：`GET /admin/customer-groups`、`/customer-product-groups`、`/customer-product-discounts`要求customer_groups.view；组 `POST/PATCH/DELETE`、商品组CRUD及 `PUT /admin/customer-product-discounts`要求customer_groups.manage。
- 交互：选择规则类型后仅提交对应字段，稳定键和资源本身version；成功重读该组及规则。商品组规则覆盖默认、命中多组取实际最大减免，按[分组定价](../domain/customer-group-pricing.md)说明；不做前端第二套结算。
- 删除被客户、优惠码或规则引用的组时呈现服务端冲突，不自动解绑。规则仅影响新购，原服务合同价不追溯；优惠码可否叠加以券的客户组范围为准。

## 客户等级 `/admin/customer-levels`

- 排版：名称/启用筛选＋规则表，列规则名称、六条件摘要、匹配客户数、排序、启用、编辑；新建/编辑用双列表单Dialog，手机单列。
- 组件：整数金额/次数输入、六项阈值组、启用Switch、排序输入、匹配数量链接、删除确认。匹配数量进入客户列表 `level_id`筛选，不创建虚假的“手动升级等级”。
- API：`GET /admin/customer-levels`要求customer_levels.view；`POST /admin/customer-levels`、`PATCH/DELETE /{id}`要求customer_levels.manage，写入使用当前规则expected_version及稳定键。
- 交互：六条件为AND，近30天条件单独标注；同一客户可以同时满足多项规则。保存成功刷新列表和匹配数量；409保留草稿重新核对，未知用原键原body恢复。
- 等级规则不是客户组折扣，不显示“本等级自动享受折扣”或最高等级身份，相关筛选语义见[客户 API](customer-api.md#客户列表和高级筛选)。

## 客户字段 `/admin/customer-fields`

- 排版：关键词/类型/启用条件；字段表列label/key、类型、必填、管理员专用、注册展示、排序/启用；右侧或Dialog编辑，底部预览只读样例。
- 组件：六类型选择(text/textarea/number/date/select/boolean)、选项编辑器、正则与说明输入、required/admin_only/show_on_register/active开关、排序；手机字段卡片和单列表单。
- API：`GET /admin/customer-fields`、`/{id}`、集合 `POST`、单项 `PATCH/DELETE`均要求customers.manage；普通资料表单只读 `/admin/customer-field-definitions`不能借此获CRUD权限。
- 交互：读取当前定义version后编辑，写键和expected_version按[字段契约](../customer-registration-fields-contract.md)；显示对下次保存/注册的影响，不自动修改现有客户值。
- 成功刷新字段管理、递交表单的定义缓存；定义变化时客户编辑保留安全草稿并重新核验。停用/删除字段的历史值不悄悄清空，预览不证明公开注册流程已完成新接线。

## 公共文件分类 `/admin/file-groups`

- 这是公共下载中心分类，与客户专属文件分开；入口来自下载管理，返回仍是公共下载，不出现“客户文件所属分类”误导。
- 排版：标题新建＋关键词/启用状态；分类树表展示名称/父分类、描述、有效启用、文件/子分类数量、排序、版本/操作；新建/编辑Dialog，删除AlertDialog；手机保留路径的分类卡片。
- API：`GET /admin/file-groups`要求downloads.view；`POST /admin/file-groups`、`PUT/DELETE /{id}`要求downloads.manage。字段沿用下载分类模型，使用该分类expected_version，不使用客户文件version。
- 交互：保存成功刷新列表和公共文件分类选择器；引用冲突展示服务端原因。该写协议没有统一幂等回执，未知结果先读当前分类，不自动重建；不顺带删除所属文件。

## 客户资源池 `/admin/customer-resources`

- 目标：超级管理员分配未归属销售的客户；不显示IP/服务器库存，也不允许普通销售靠此领取范围外客户。
- 排版：说明＋关键词查询＋未分配客户表；列客户编号/身份、启停状态、注册时间、销售分配；如保留财务列必须沿用明确的钱包口径，不能把旧余额收入当实收。手机客户卡片＋“分配销售”。
- API：`GET /admin/customers/page?sales_owner_id=none`要求customers.view，页面本身仅超级管理员；候选 `GET /admin/sales-owners`，分配 `PUT /admin/customers/{id}/sales-owner`仅真实超级管理员浏览器会话。
- 交互：打开客户归属Dialog → 读当前sales_version → 选择启用销售 → 核对客户/目标人员 → 原键提交。共用客户列表/详情的归属组件与待核对意图，不能同时创建两个未知操作。
- 成功后重读资源池，该客户正常移出列表；409不自动覆盖新销售，未知保留原body/key，权限或会话改变停止恢复并清理旧身份视图。

## 实名审核 `/admin/certifications`

- 排版：关键词/业务状态筛选＋认证申请表；列客户、个人/企业、业务状态、处理状态、提交/审核时间；行操作打开审核Sheet，不为不存在的独立详情路由造菜单。
- 组件：状态Badge、身份资料受控展示区、申请历史时间线、审核备注、批准/驳回与二次确认；手机审核Sheet全宽，敏感资料禁止复制进全局搜索。
- API：`GET /admin/certifications/page`、`/{id}`、`/{id}/history`要求certifications.view及客户范围；`POST /admin/certifications/{id}/review`要求certifications.manage、expected_version和稳定键。
- 交互：先读最新申请和历史，再填decision/note，核对目标客户后提交；成功刷详情/列表和客户摘要，409保留备注并重读，原键恢复未知结果。无权限不显示证件占位值。
- 业务pending与queued/processing/needs_review/finished分开显示；技术失败不标成认证驳回，人工审核不能清除提供方已发送标记。详情不展示密文/提供方原始结果，能力边界见[认证领域](../domain/certification.md)。

## 服务列表 `/admin/services`

- 排版：关键词/客户/状态等已支持条件＋服务表；列名称/编号、客户、产品/交付模式、状态、周期价、客户到期时间、自动续费、进入详情；顶部批量代续费在明确单客户后启用。
- API：`GET /admin/services/page`要求services.view并受客户范围限制；精确customer_id固定于客户详情嵌入列表。筛选条件只使用当前接口支持项，不以本地过滤冒充全局查询。
- 组件：客户选择器、状态/日期展示、Checkbox和选中清单、独立“仅建续费单”“使用余额付款”按钮；批量不是一键开启自动续费。
- 交互：一次最多20个同客户服务，跨客户选择阻止进入；每项版本来自自己的服务详情/响应。单项状态变化须重新核对，不用列表所有version的最大值。
- 手机：服务卡片显示客户/状态/到期日；批量工具栏显示“同客户N项”及展开清单；金额/确认不藏在图标内。

## 服务详情 `/admin/services/[id]`

桌面头部服务身份、归属客户、状态和版本；主区“概览、运行、任务/活动、续费账单、管理”页签，右侧资源、期限与计价摘要。手机将摘要移到概览，管理动作逐项Dialog，不使用一个“任意改状态”的下拉框。

- 读取：`GET /admin/services/{id}`、`GET /admin/services/{id}/management`要求services.view；活动 `GET /admin/services/{id}/activity`；运行页仅按实际适配器能力请求monitoring/storage/NAT/CDROM等接口，缺能力显示不适用，失败局部重试。
- 概览：客户期限 `next_due_at`、上游观测 `supplier_next_due_at/supplier_observed_at`、term_mode分别展示；定价mode、固定最终应付、原合同估算、剩余优惠分开，不用“已修改上游”描述本地改期。
- 常规动作：`POST /admin/services/{id}/actions`、`POST /admin/services/{id}/delivery`要求services.manage；显式读取凭据 `POST .../credentials`同样要求services.manage，不自动预载，离开清除展示；暂停/恢复、开关机、重装等按能力和状态决定，破坏性动作二次确认，任务受理不等于执行成功。
- 自动续费：`PATCH /admin/services/{id}/auto-renew`，services.manage，明确enabled布尔；仅改意图不立即扣款。成功重读详情；该接口无回执/版本协议，未知先读取目标状态，不按金融操作声称恰好一次。
- 人工交付引用原待人工任务，选择独立库存需resources.view/manage、服务/库存各自版本；库存绑定按[人工资源](../domain/manual-resources.md)，不能通过普通资源信息编辑绕过占用或释放。
- 任务/活动只展示当前授权返回的安全投影。服务归属/owner_generation改变时，立即清理旧客户的任务、凭据、配置及财务缓存；未获旧客户范围不展示历史订单/购买字段，A→B→A也不恢复旧代次。

### 单服务代续费：报价、建单、余额付款

1. 管理员点击“代续费”读取最新management和服务；展示客户、周期、原到期日、计价模式。`POST .../renewal-quotes`带服务expected_version，要求services.view、invoices.view、services.renew。
2. 显示服务端base/discount/amount、expires_at及原未付续费单。can_create=false仅提供“查看已有账单”，不自动采用其金额或再次建单。
3. 管理员填原因，核对“仅创建未付单”，`POST .../renewals`带原quote_hash/expected_version和稳定key。首次201/重放200后读取最新账单与服务，显示“未付款”；不得自动接余额付款。
4. 独立“使用客户余额付款”需services.renew.pay_balance及两项读取权限；展示客户/服务/账单号、账单version、精确amount和扣款提示，提交 `POST .../renewals/{invoice_id}/pay`，使用独立付款key。
5. 供应商本地paid与renewal_status=pending分开，轮询最新服务/任务直到确认或进入核验；不把钱包扣款成功直接写为远端已续费。0元也显式确认。
6. 409刷新原账单/服务并重新报价确认；报价到下一个整五分钟边界，不保证完整五分钟。超时先用原body/key恢复，随后读取实时状态，禁止新key重复建单或扣款。

### 同客户批量代续费：清单与逐项执行

1. 从固定客户服务列表选择1–20项；展示冻结顺序。`POST /admin/service-renewal-batch-quotes`逐项显示quoted/failed及业务原因，不将失败金额写成0。
2. 选择action=create或pay_balance，两者独立；create逐项保留服务version/hash，pay_balance逐项读取并确认账单id/version/amount，禁止自动采用已有未付单或混合动作。
3. 填统一原因，确认客户、顺序、清单与各项金额 → `POST /admin/service-renewal-batches`稳定登记key；登记不建单、不扣款，页面明确显示“已登记，尚未执行”。
4. 清单结果区显示pending/unconfirmed/failed/succeeded、单项code和账单链接；`GET /admin/service-renewal-batches/{batch_id}`只查询，只有原登记管理员可访问。
5. 显式勾选本批子集 → 核对动作 → `POST .../{batch_id}/execute`带service_ids和独立execute key。执行按原顺序逐项提交，后项失败不回滚已成功项，不显示“全批撤销”。
6. unknown/unconfirmed保留原execute key恢复并停止后续动作；确定失败修复条件后才能用新execute key显式重试失败子集。报价/金额/版本变化须新清单，已成功项不能再次纳入扣款。
7. busy/capacity显示稍后恢复查询；无权限/范围变化停后项，不切换管理员接管。成功后刷新相关账单、服务和客户钱包/摘要，批次历史不当作当前余额或上游成功证明。

### 改价、改期与状态纠错

| 操作 | 明确步骤与 API | 权限/确认内容 | 成功、冲突和未知 |
| --- | --- | --- | --- |
| 长期续费价 | 读management → fixed最终应付或contract原合同 → `POST .../renewal-pricing/preview` → 原因 → `PUT .../renewal-pricing` | services.view＋services.pricing；展示原价/新价/模式、0元显式；恢复原合同时保留剩余优惠 | 原key/body恢复；成功刷management；不重算客户分组，固定价会阻止升级直至恢复contract |
| 本次固定应付 | 金额 → `POST .../fixed-renewal-quotes` → 核对/原因 → `POST .../fixed-renewals` | services.view/invoices.view＋services.renew＋services.renew.override；仅本次应付，长期价不变 | 单独稳定key；只建未付单，再走显式付款；0元同样确认 |
| 客户期限纠错/补偿 | 选择correction/compensation → RFC3339日期时间与原因 → `POST .../term-adjustment-quotes` → 前后期限确认 → `POST .../term-adjustments` | services.view＋services.term.correct或services.term.compensate；补偿必须延长，服务转managed | 服务expected_version/hash/key；成功刷期限/观测/活动；不建单、不自动恢复暂停、不修改上游 |
| 受限本地状态纠错 | 核对最新一条生命周期尝试source_job_id（create/suspend/resume/terminate，含失败及取消尝试），该条本身须成功且suspend→suspended或resume→active；不得从旧成功集合选择 → 原因/证据 → `POST .../status-correction-quotes` → `POST .../status-corrections` | services.view＋services.status.correct；仅人工/模拟已交付服务，真实远端模式不显示入口 | key与版本/摘要恢复；本地纠错不派发远端动作；来源失效、权益到期或围栏冲突需先处理原业务 |

以上路径均续接 `/admin/services/{id}`；每个预览都有独立hash，不能互换。Dialog显示前后差异与明确的副作用；409不自动把最新version/hash塞进旧请求，报价过期需管理员重新核对。期限纠错不是免费续费收款，固定应付不是改长期合同价。

### 单服务续费人工收款

1. 从本服务的单服务自动续费账单打开；只支持正金额全额到账，非充值/购买/部分/批量收款。权限services.view、invoices.view、invoices.collect_manual独立判断。
2. 读取账单expected_version和服务expected_service_version，收集amount_cents、实际received_at、method、receiving_account、transaction_no、evidence_ref、evidence_sha256和reason。账户/凭证引用只能非秘密标识；不把选择文件等同服务器已存证。
3. `POST .../renewals/{invoice_id}/manual-collection-quotes`，展示客户、账单、金额、实际到账时间、流水及对钱包无入账的说明；核对后以原body补quote_hash，稳定key提交 `POST .../manual-collections`。
4. 展示本地paid及renewal_status，供应商pending继续读任务；刷新账单、服务、现金交易及摘要，金额不得再算一笔网关收入。
5. 流水占用、非全额、版本变化、日期/报价失效显示明确冲突，先查原来源；断网保留原key/body，禁止自动转余额支付或重新补收。同一资金来源不能为了提交成功改流水号。

### 服务转移客户

1. 在服务管理打开独立向导；要求services.view、customers.view、services.transfer。读取management的service_version、owner_generation和customer_id，将customer_id明确映射为提交的source_customer_id；源客户固定，目标从授权启用普通客户中搜索，不能转给管理员/本人同源。
2. 展示源/目标身份及范围核对；管理员先在真实资源侧完成密码/外部账号轮换、撤销控制台会话、移除不应交接的原客户数据，并核对备份/快照/网络等交接。填写reason与非秘密handover_evidence，明确勾选credential_handover_confirmed=true。
3. `POST .../transfer-quotes`携带expected_version、必传expected_owner_generation及原输入；页面展示转后客户、期限、保留价格、自动续费关闭。预览未执行交接或远端转移，不使用“系统已轮换密码”的提示。
4. 最终确认重述：原订单/账单/资金/佣金不迁移，历史退款仍归原账单客户；保留库存绑定、合同价与期限；新续费/升级归新客户，自动续费关闭，机器人绑定/通知地址不跟随。
5. `POST .../transfers`在原body补quote_hash并用稳定key提交，首次201/重放200；成功后清理该服务旧代次缓存，重读详情/management、源/目标服务列表及摘要，显示实时客户与新owner_generation。
6. 未付单、待交付流量、退款/取消/配置/任务未知、sending/unknown通知等阻断时保留原因，引导处理原流程；不得靠删除记录、改状态或自动换目标绕过。
7. 409 stale_service/stale_service_owner/service_owner_changed等重读当前归属后停止旧向导；超时保留原key/body恢复，仍受源、目标及当前归属三方范围约束。历史回执优先恢复事实，但不能当作当前owner；A→B→A的代次仍增长，旧购买字段/通知/任务不复活。

## 订单列表 `/admin/orders`

- 排版：关键词/客户/状态等真实筛选＋选择工具栏＋订单表；列订单号、客户、商品摘要、净应付/付款状态、审核状态、创建时间、备注摘要、查看；长商品和内部备注放可展开区，避免全表横向膨胀。
- API：`GET /admin/orders/page`要求orders.view及客户范围；管理动作要求orders.manage。销售佣金数据另需sales.view，不能因无权读取而整表报错或展示0提成。
- 组件：状态徽标、金额快照、Checkbox、批量审核/取消/删除Dialog、分页。`POST /admin/orders/bulk-review|bulk-cancel|bulk-delete`用明确order_ids和各自expected_versions，按响应逐项展示结果。
- 交互：批量动作先确认具体编号/数量/原因，稳定key与原body恢复；切页/筛选清选择。删除不视为退款、不对已付款/交付记录开放绕过；成功刷新列表及受影响详情，409回读对应项。
- 手机订单卡片保留订单号/客户/状态/金额，备注折叠；不把批量取消隐含成多次无确认退款。

## 管理员代下单 `/admin/orders/new`

- 排版：步骤“客户→商品与规格→报价确认”；桌面左主区配置、右320px价格清单与客户，手机价格摘要放当前步骤末尾。客户详情跳入时锁定customer_id，返回恢复原页签。
- API：`GET /admin/orders/create-options`及kind=customers|products读取真实候选，`POST /admin/orders/quote`报价、`POST /admin/orders`创建，均orders.manage；不用管理员Cookie调用客户购物车结账。
- 组件：远程客户搜索、商品选择、账期Radio/Select、配置项/数量输入、购买字段动态表单、首期/续费显式覆盖价、优惠码、备注、付款偏好/发信选项和价格差异清单。
- 交互：改变客户/商品/规格/数量/券/覆盖价使旧报价失效；服务端返回完整折扣与净额后才允许确认，展示组折与券折、初装费和续费价，不能用浏览器百分比代算结算。
- 明确“创建订单与对应账单/待交付服务，不立即调用网关付款或上游开通”；带原quote_hash和稳定key提交，首次201/重放200进入原订单。未知保留原输入/key，重新报价不能覆盖未知意图；成功再读最新订单/账单。
- 字段和配置的能力以商品选择返回为准；不支持的供应商联动给明确原因，不把缺规格显示成固定配置成功。

## 订单详情 `/admin/orders/[id]`

- 排版：订单号/客户/订单及审核状态头；主区商品行及配置、原价→组折→券折→净额、关联服务、内部备注、活动；右侧账单/付款/创建时间与授权动作。手机账单摘要置商品前，表格内部滚动。
- API：`GET /admin/orders/{id}`，orders.view及范围；`POST .../review`、`POST .../notes`、`POST .../invoice`、`POST .../cancel`、`DELETE /admin/orders/{id}`要求orders.manage。每个动作独立表单/确认，不提供任意字段PATCH。
- 审核按expected_version、decision/note与稳定key；备注按expected_version/notes/key；延后开票动作按订单版本/key。成功重读订单、关联账单及列表，回执不代替当前付款状态。
- 取消/删除先显示业务影响及当前状态，确定冲突先处理付款/交付，不能删除订单当作退钱。退款进入有invoices.refund权限的原账单流程，与资源暂停/终止分开。
- 无关联读取权限时不显示可点击财务/服务链接；历史转出服务不展示其当前IP、域名或新客户身份。转移不重算原订单分摊，也不把新归属当旧订单财务客户。

## 既有取消记录 `/admin/cancellations`

- 目标是核对已存在的取消记录；客户退款/停用需求继续提交工单，管理员分别在账单和服务处理，不恢复新的客户自助申请入口。
- 排版：标题与只读范围说明；记录表列客户、服务、立即/账期结束类型、原因、状态、创建/处理时间、处理说明、查看服务；pending行提供决策Dialog。手机记录卡片＋展开原因。
- API：`GET /admin/cancellations`要求services.view；`POST /admin/cancellations/{id}/decision`要求services.manage，请求decision/note。当前读取为limit/offset数组，无总数及状态查询协议；使用上一页/下一页，尾页仅由实际长度判断，不伪造总数/全局状态筛选。
- 交互：先打开原服务核对当前权益、续费单和支付未决，再批准/拒绝；确认页展示类型、时机及终止影响。批准表示进入原任务流程，等待人工/未知不等于已终止。
- 该决策无统一key/expected_version回执协议；超时先重读列表/服务/任务，不自动重复批准。invoice_payment_pending等冲突先处理原支付，不通过删除意图解围；成功刷新取消记录、服务和任务。

## 本领域页面交付检查

每页均需记录桌面与390px手机下的加载、空、筛选无结果、局部错误、无权限、401/404、成功及可用键盘路径；写页面另覆盖提交锁定、409安全草稿和未知结果恢复。客户页签的固定customer_id、切客户旧响应隔离与分区权限必须有浏览器证据。

服务专项还须覆盖同客户20项上限/部分成功、报价过期、原key恢复、paid与上游pending区分；转移须覆盖三方范围、真实资源交接确认、代次变化清缓存及A→B→A。文件实际下载、密码/会话失效、真实供应商结果分别验收，不能由API测试或本文设计直接标为通过。
