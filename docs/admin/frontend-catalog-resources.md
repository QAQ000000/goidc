# 后台商品、供应商与资源页面设计

状态：前端实施设计。本文覆盖21条页面路由，其中20条已有页面文件，`/admin/manual-resources`为拟新增页面；已有路由不代表本文交互已接线或完成浏览器验收。只定义当前批准业务；API和数据约束以[商品API](catalog-api.md)、[后台API](api.md)、[供应商协议](../supplier-protocol.md)、[人工库存](../domain/manual-resources.md)及代码为准。

公共外壳、颜色、字号、表单、焦点、加载/空/错误/权限/冲突/未知结果与断点遵循[后台前端设计](../admin-frontend-design.md)。下文API省略`/api/v1`；页面路由和API路径分别列示。金额输入按元呈现，校验后提交整数分；无限库存使用null，不能转换成0。

## 页面与导航边界

| 工作区 | 页面路由 | 入口及关系 |
| --- | --- | --- |
| 商品目录 | `/admin/products`、`/admin/products/new`、`/admin/products/[id]` | 商品与服务导航；列表进入创建/编辑；创建成功进入返回id的编辑页 |
| 商品组织 | `/admin/catalog-groups`、`/admin/catalog-groups/new`、`/admin/configurable-options`、`/admin/service-groups` | 购买分组、共享配置、已购服务导航分别管理，三种ID不能互换 |
| 供应商 | `/admin/suppliers`、`/admin/suppliers/catalog`、`/admin/suppliers/orders`、`/admin/suppliers/renewals`、`/admin/suppliers/jobs`、`/admin/suppliers/tasks`、`/admin/suppliers/api-settings` | 账户→目录导入→本地商品；订单、续费与任务回链服务/账单 |
| 资源 | `/admin/resources`；拟新增`/admin/manual-resources` | 前者为已购服务只读视图；后者为独立人工库存，不能合成一个库存列表 |
| 流量包 | `/admin/traffic-products`、`/admin/traffic-orders`、`/admin/traffic-orders/[id]` | 流量包商品→购买记录→资源端人工交付核验 |
| 编译模块 | `/admin/apps`、`/admin/apps/store` | 已配置实例目录与当前构建模块目录；旧store URL兼容，不提供运行时安装 |

列表返回保留受支持筛选、页码和页面来源；跨对象切换立即清空旧详情。路由查询参数只存非敏感筛选，不存密码、购买字段值或供应商原始响应。未列为现有API筛选的条件只能明确标为当前已载入集合筛选，不能伪装成全库搜索。

## 1. 商品列表 `/admin/products`

目标是找到商品、核对上架与分类状态，并进入完整编辑器。头部左侧标题与商品总数，右侧“新建商品”“导入上游商品”；分组与配置管理作为次级导航。返回从编辑页恢复原列表。

桌面上方为关键词、一级分组、二级分组、启用状态和搜索/重置；下方单表与分页。表格列固定为名称/ID/公开标识、购买分组、交付模式、默认账期/周期价/初装费、库存、启用状态、排序、操作。名称链接编辑，行尾“编辑”“复制”；不设计删除和没有API支持的批量上架。

组件使用搜索框、级联选择器、金额文本、状态徽标、库存标签、分页表格与复制弹窗。复制表单只要求新名称与公开标识，确认中锁定两字段；来源是supplier时显示“请从供应商目录导入”。显示停用与分类不可见为两个状态，不能把active当隐藏/退役开关。

读取`GET /admin/products/page`及`GET /admin/catalog-groups/{kind}`，要求`products.view`；写入还需`products.manage`。复制`POST /admin/products/{id}/duplicate`使用稳定key，成功进入新id，刷新列表；未知结果同key同body恢复，冲突保留名称/标识。手机用商品卡片保留名称、价格、库存、状态与文字操作，筛选进入抽屉，分页留底。

## 2. 新建商品 `/admin/products/new`

入口为商品列表，返回保留列表；头部“新建商品”与“创建”，表单下方取消。桌面主体8/4分栏：左侧基本资料、基础定价、交付与准入；右侧显示本地预览和保存范围。此预览只说明填写内容，不是结账报价或真实供应商成本。

基本资料区含名称、公开标识、说明、购买分类、排序、启用；定价区含默认账期、周期价、初装费、有限/无限库存；交付区含manual/simulation/nokvm，NOKVM依据已配置profile选择合法固定规格；准入区含手机、实名、人工审核。supplier选择改为明确跳转“从上游导入”，不提交伪造来源。

组件使用带帮助文本的字段、长文本、金额输入、单选、Switch、可搜索分类选择和profile规格选择器。四个原生账期仅`monthly/quarterly/annually/once`；试用、多币种、小时/日付不提供可保存控件。名称和slug不依赖客户端派生后未经确认直接提交。

`GET /admin/provision-profiles`及目录读需`products.view`；`POST /admin/products`需再具`products.manage`。创建意图预生成稳定key；201或恢复响应取得id后进入编辑页，并读取服务器基线。提交未知保留原body/key，不另建商品；401停止提交，重新登录不意味着旧请求失败。手机所有分区单列，摘要折叠，底部保存栏不遮挡字段错误。

## 3. 商品编辑 `/admin/products/[id]`

入口来自商品列表、导入结果或关联业务；返回恢复来源。头部显示商品名/ID、公开标识、启用及revision，右侧“复制”“打开客户商品页”。主体左侧分区页签，右侧320px状态摘要；各分区独立保存，不设置虚假的“全部一键保存”。

### 基本资料、定价、交付与归属

基本资料表单沿用新建字段，读取`GET /admin/products/{id}`，`PATCH`同时提交`expected_revision/expected_stock`；保存完整可编辑资料。定价表列账期、周期价、初装费、启用、默认、操作；行内编辑后调用`POST /pricing`，明确保存一行；先切换默认账期再停用旧默认。供应商只列实际导入账期，支持半年、一至十年及once等实际集合，不复用原生四项限制。

交付卡片展示来源、模式和NOKVM已配置规格，supplier用`GET /supplier-binding`展示只读上游商品/映射状态与目录入口。服务导航独立选择，`PUT /service-group`提交商品revision；购买分类和服务组同时显示名称及用途，避免混淆。

### 配置、字段与升级

“配置与联动”使用组导航、选项表及价格矩阵；组字段为类型、必选、数量上下限/步进、单位、排序、启用、内部备注；选项表列标签/值、周期增价、初装增价、数量界限、启用及父项。阶梯编辑器明确graduated/volume、区间和账期；元数据与明确提交的各账期阶梯一次原子保存。原生规则用条件/结果构建器，保留无效项并定位错误，不静默删除。

API为`/option-groups`、`/options`、`/price-tiers`与`/configuration-rules`的已定义POST/PUT/DELETE；全部使用最新商品revision。共享投影显示来源并提供跳转共享配置管理；supplier导入规格和字段只读，不开放原生写入控件。引用中的配置不能强删，提示停用及引用原因。

“购买字段”表列key、标签、类型、作用域、必填、加密、发票显示、排序；展开编辑说明、长度、正则、选项、客户资料映射。`PUT /purchase-fields`为整体替换，最多20项；加密不进发票，管理员字段不设客户必填。此页仅编辑定义，不显示客户历史填值。

“升级政策”两卡：可升级商品列表（候选/兼容/已选/允许降配）与配置升降配开关；`GET/PUT /upgrade-targets`、`PUT /configuration-upgrade-policy`均绑定revision和稳定key。适配器不支持的目标不因勾选而启用。

### 关联与保存恢复

“推介规则”“业务下载”展示受权关联入口，分别使用`affiliates.view/manage`和`downloads.view/manage`及其领域API；不能用商品权限代替。客户购买链接指向`/products/{id}`，复制为本地操作；无试用功能时不给空的可编辑试用页签。

商品读需`products.view`，本节目录写还需`products.manage`。保存后重新读取详情/revision/库存/相关分区；409保留当前安全草稿，展示重新加载及差异核对，禁止自动换版本提交。普通CAS未知先读取对照；只有明确支持回执的接口使用原key重放。手机分区选择器置顶，价格/配置矩阵内部横滚，摘要折叠，不让整页横滚。

## 4. 购买分组 `/admin/catalog-groups`

目标是管理商城一级section和二级category，入口来自商品导航；列表右上“新建分组”，返回商品时保留来源。桌面顶部类型Tabs，关键词/启用/一级归属筛选，中部表格，底部分页。

一级表列名称/标识、说明、启用、排序、关联数、version、操作；二级额外显示父分组与父启用状态、headline/tagline。组件为Tabs、分页表格、状态徽标、编辑抽屉、删除确认；排序使用显式数字并保存，不设计无批量排序API支撑的拖拽落库。

读`GET /admin/catalog-groups/{kind}/page`需`products.view`；编辑`PUT /{kind}/{id}`、删除`DELETE`还需`products.manage`及`expected_version`。删除前列当前对象及关联数，`catalog_group_in_use`保留对象并解释须先调整归属；不做强制级联删除。修改成功刷新当前类型列表及商品分类选择器；CAS未知重读核对。手机分组卡片，类型Tabs常驻，父分组与可见性合并摘要，编辑抽屉转全宽Sheet。

## 5. 新建分组 `/admin/catalog-groups/new`

从列表传入非敏感kind与父section作为预选，返回原类型列表。桌面单列表单最大760px，右侧显示“商城一级/二级”的实际位置示意；不能用示意假造在线商城数据。

字段分区为类型、名称/slug/说明、启用/排序；category另含父section、headline/tagline。组件为类型单选、可搜索父组选择、slug校验、文本区和Switch；切换类型提示丢弃专有草稿，父组停用清楚提示公开侧不可见，不自动启用。

读取分组及创建`POST /admin/catalog-groups/{kind}`，权限分别`products.view`和`products.manage`。使用稳定key，创建成功返回原类型列表并刷新；未知同key原body恢复，不以相同slug冲突推断成功。手机示意折叠在表单之后，保存/取消全宽，错误直接定位字段。

## 6. 共享配置 `/admin/configurable-options`

入口为商品设置或商品共享来源链接。桌面左侧共享组列表（名称、关联数、version），右侧选中组编辑器；头部“新建”“复制”，编辑器Tabs为定义/价格、关联商品、影响核对。保持当前选中组与返回商品上下文，无需拟造`/[id]`页面。

定义区域列组slug、类型、数量规则、内部备注及items；价格矩阵列各项账期周期/初装价。关联区域为可搜索商品多选与当前投影revision表；移除关联或删除组前展示受影响商品、脱离后独立保留的范围，并提供显式确认。

组件使用分栏列表、树/选项编辑器、账期矩阵、商品选择器、影响预览弹窗。`GET /admin/global-configuration-groups`及`/{id}`需`products.view`；创建/复制/更新/assignments/删除需`products.manage`，所有写均必填稳定key。提交`expected_version`及受影响商品`expected_revisions`，需要时`acknowledge_detach`；不能把局部组ID当共享定义ID。

写成功重读共享详情及关联商品版本；冲突保留草稿、重新核对完整影响集合；未知原key/body恢复。手机先列表后全宽编辑Sheet，矩阵独立横滚；关联列表可折叠但影响确认不能省略。

## 7. 服务导航组 `/admin/service-groups`

目标是组织客户“已购服务”导航，独立于购买分组；入口商品设置，行内可查看所属商品，返回原来源。桌面表格列名称、图标、排序、version、操作；右上新建，编辑用窄抽屉。

字段仅名称、图标和排序，图标选择`server/cloud/database/globe/box`且同时显示文字。无slug/active字段，不提供对应开关；排序用数字。删除弹窗必须选现有目标组，明确商品归属迁移，不把删除写成丢弃服务或删除商品。

`GET/POST /admin/service-groups`、`PUT/DELETE /{id}`；读`products.view`，写还需`products.manage`。修改带`expected_version`，删除带`target_group_id`与原版本；成功刷新组列表和商品归属，未知先重读核对，不承诺不存在的统一回执。手机用图标+名称卡片，编辑与迁移确认Sheet保留完整目标名称。

## 8. 供应商账户 `/admin/suppliers`

从商品与服务导航进入；头部“新增供应商”“查看上游目录”，次级导航链接订单、续费、队列、交付任务和API设置。桌面账户表列名称/ID、provider、地址、启用、配置状态、schema/账户版本、占用/待核验提示、操作；状态不把“本地有效”写为连接健康。

新增/编辑抽屉分基本身份、普通配置、凭据、启用状态；由`GET /admin/supplier-providers`的描述符渲染已知字段类型、必填和范围。敏感字段只写、显示是否已配置；替换/清除分开确认。provider与身份不可重定向，已有账户不提供切换provider的假控件；实现缺失只提供受支持的停用路径。

读取`GET /admin/suppliers`需`products.view`；`POST /admin/suppliers`、`PUT /{id}`、连接测试还需`products.manage`和真实后台会话。保存提交`expected_version/schema_version/expected_schema_version`，普通`config`与`secure_config`分开，账户启用使用`active`，不套用通用channel的`enabled`输入字段。

连接测试`POST /{id}/test-connection`使用账户/schema版本和稳定key，显示测试中及结果时间；测试只读，不开通上游。外部读期间失权导致原key仍in_progress时，重新认证后允许明确发起新只读测试，不把此规则用于任何上游写任务。账户普通保存未知先重读核对，无通用key恢复保证；凭据不写浏览器存储。手机账户卡片、动态表单单列，关键启用/待核验状态在顶部。

## 9. 上游目录与导入 `/admin/suppliers/catalog`

目标是“选商品→设售价比例与本地分类→逐项预览→确认导入”，入口账户行/商品列表；返回原供应商或商品列表。桌面顶部账户选择与读取按钮，左侧上游分组，右侧关键词和商品表；底部选择栏显示跨页已选数量、清空与“预览导入”。不因目录条目已有摘要便宣称已读取完整价格。

目录`POST /admin/suppliers/{id}/catalog`，完整详情`GET /{id}/products/{product_id}`；均需`products.view/manage`，只读外部调用仍按实际路由权限展示。若返回数组，则搜索/分组/翻页明确为已加载目录本地操作；账户切换先确认清空选择，不能混合两账户导入。

目录列为勾选、上游名称/ID、上游组、摘要规格/字段数、导入状态；本地批量设置区有售价比例、二级商品分类、默认上架。比例100.00%提交10000bps，不能解释为减免100%；提供整数基点校验，单批1–50项。

预览用宽弹窗/步骤页内区域：列商品/自动slug、各可用账期基础价/初装费、规格组/购买字段数、核对结果；每行展开全部账期、配置算法与`excluded_cycles`原因。必须区分“该账期排除但仍有可导入付费账期”和“整商品不可导入”，不把排除账期当零价。复杂联动、外币、字段不支持及缺密钥逐项显示可读原因和原错误码。

组件为跨页选择表格、比例输入、级联分类、步骤条、逐行核对徽标、可展开价格矩阵与确认弹窗。`POST /import-preview`取得账户/商品预览版本；确认`POST /import-batch`必填稳定key，绑定原预览和选择集合。一项失败全批回滚，禁止UI展示部分成功；不能静默剔除不支持商品后提交剩余。

成功展示每个本地商品id/名称和“编辑商品”链接，刷新目录导入标记、商品列表及分类选择；同key重放以原回执为准。预览过期/上游变更保留选择并重新预览，不替换版本自动提交；未知原body/key恢复。导入复制全部受支持多账期、客户选配与字段，本地售价不随上游之后变价自动变更。旧单品固定规格`POST /import`只作为明确高级兼容入口，不替代主流程。

手机分组改选择器，商品卡片可勾选，底部选择栏不遮内容；预览改一商品一卡、账期折叠表，禁止把核对原因截成只有图标。当前不支持已售服务规格升级和账期切换，不从导入页产生上游购买或付款。

## 10. 供应商订单 `/admin/suppliers/orders`

目标是核对本地下单与上游交付，入口供应商导航；订单链接回`/admin/orders/[id]`，账单与服务按各自权限跳转并保存return上下文。桌面筛选为关键词、状态、供应商、客户、起止日期；表格列订单号、客户、商品/供应商、金额、付款状态、IP/域名、服务/供应商关联、创建时间、详情。

使用分页表格、金额文本、订单与交付分离徽标、可复制非秘密编号和关联链接。`GET /admin/supplier-orders`需`orders.view`，依实际支持参数提交分页/过滤，不在此页复制服务生命周期按钮；详情动作在订单/服务所属页复用。

刷新只重读此列表；未知枚举保留原值，成本未记录显示“未记录”，不得补0。订单paid不等于上游active。手机卡片优先订单号、客户、付款/交付状态及金额，关联资源在展开区，筛选Sheet保留日期范围。

## 11. 供应商续费 `/admin/suppliers/renewals`

入口供应商订单/服务，返回保持来源。顶部“供应商续费订单”与订单切换入口，下方关键词/状态、供应商ID、客户ID、起止日期与重置；桌面表列ID/账单号、客户、商品/供应商、IP/域名、建单时间、金额、支付方式/本地状态、上游续费状态/成本。

`GET /admin/supplier-renewals`需`orders.view`；账单跳转需`invoices.view`，服务链接需`services.view`。使用双状态徽标与成本空值占位，清楚区分本地paid、上游待核验、上游已确认及尚未排队；不能把本地付款时间当上游期限推进证据。

本页只读，不设计“再次扣款”或无报价的批量续费。进入服务详情使用现有管理员续费协议；刷新本地数据不会重发上游任务。手机卡片依次金额与本地状态、上游状态/成本、账单/服务链接，长IP在容器内换行。

## 12. 供应商任务队列 `/admin/suppliers/jobs`

从供应商导航或异常提示进入，行链接服务详情并保留队列筛选。桌面筛选关键词、供应商、客户、动作、状态、更新时间范围；表列任务ID、客户/商品、服务、供应商、动作/来源、状态、尝试数、更新时间、可执行操作。

读取`GET /admin/supplier-jobs`需`products.view`；重试`POST /admin/supplier-jobs/{id}/retry`另需`services.manage`。组件为状态筛选、任务表、详细摘要抽屉、重试确认弹窗和局部刷新。动作词典仅用于翻译返回值，不能据词典给所有provider生成按钮。

仅服务器`can_retry=true`才显示安全重试，并提交`expected_attempts/expected_updated_at`与稳定key；本入口主要恢复未发外部请求的失败sync，不做通用上游写重试。成功刷新该任务、页列表与服务活动；unknown按原key/body恢复，waiting_verification进入服务核验，不换key强行重发。手机任务卡片保留状态、尝试数和文字操作，筛选折叠。

## 13. 供应商交付任务 `/admin/suppliers/tasks`

此路由面向任务运维，区别是读取`GET /admin/supplier-tasks`需要`jobs.view`；不因为能看队列就自动授予交付或核验权限。入口供应商导航/任务工作台，返回原任务来源，复用上页列与过滤组件，但标题及权限门禁独立。

桌面在状态筛选中突出待人工/待核验，列表保持API排序；默认筛选只在API支持的状态值内配置，不伪造聚合计数。摘要抽屉展示任务ID、当前服务、账户、动作、状态和版本线索，不显示原始result、密码、JWT或未经白名单过滤的上游响应。

可重试仍依`can_retry`与`services.manage`；待核验链接服务详情的对应核验表单。`POST /admin/services/{id}/supplier-verification`、`/supplier-lifecycle/verify`沿各自job/attempt/证据协议，不合成一个“标记成功”。写成功重读任务及服务，网络未知保留原核验意图，先查当前任务；无协议不承诺key回执恢复。手机状态与去核验操作在卡片首屏，其余元数据折叠。

## 14. 客户资源API设置 `/admin/suppliers/api-settings`

目标是管理客户Bearer资源API的全局准入，并非填写上游账户密钥；入口供应商导航。桌面最大760px设置卡，三行开关为启用资源API、要求实名、要求已验证手机，下方保存/取消和影响说明，右侧只读当前version。

`GET/PUT /admin/supplier-api-settings`必须后台会话，读`products.view`、写`products.manage`；PUT提交`enabled/require_certification/require_verified_phone/expected_version`和稳定key。组件为Switch、权限说明Alert、未保存提示、关闭确认Dialog；关闭时明确现有Bearer调用也会立即受限，不声称删除了客户密钥。

保存成功刷新并替换服务器基线；未知锁定原body/key提供“恢复原提交”，不允许一边改表单一边恢复；409保留草稿与当前服务器版本分别展示。返回其他页保持未保存提醒。手机一列、说明在开关后，危险关闭确认显示完整业务影响。

## 15. 已购服务资源 `/admin/resources`

从资源导航或客户/商品详情进入，是服务只读投影。桌面顶部关键词、精确IP、供应商、状态、客户和商品筛选；表列服务ID/名称、客户、商品、IP/域名、状态、供应商/上游ID、客户到期日、进入服务详情。

`GET /admin/resources`需`resources.view`，只返回当前客户范围；关联客户或服务入口再查`customers.view/services.view`。供应商选项来自响应的`suppliers`，不从无权对象补全。组件用可搜索ID选择、只读状态表、空值文本和分页；`next_due_at=null`显示未设置，不补成已过期。

仅刷新、筛选与跳转，不给资源行添加无能力依据的电源、续费、改密或释放库存按钮。`resource_info`使用服务端白名单字段，隐藏未知结构；账号不等于授权密码读取。与拟新增库存页提供清楚的双向切换。手机卡片突出客户、IP、状态、到期日，更多元数据折叠。

## 16. 独立人工库存 `/admin/manual-resources`（拟新增页面）

现有API已实现，但当前没有页面文件；本设计新增此列表页，创建/编辑/详情均用页内抽屉，无需额外拟造详情URL。入口资源导航“人工库存”，与已购资源并列；从人工交付选择器返回时保持服务/任务上下文。

桌面上方关键词、库存状态available/disabled/retired、占用bound/unbound及刷新/新建；表列名称/ID、资源类型、IP或主机名、提供方/区域、库存状态、占用服务/客户、成本、上游到期、凭据已配置、version、操作。库存状态与占用是独立两列，available且bound不能显示为空闲。

详情抽屉分资源身份、成本与期限、凭据存在状态、占用关联及备注。新建/编辑表单分身份(name/type/provider/region/address/upstream_id)、成本/上游期限、只写账号密码、状态/备注；金额不是支出入账，上游期限不是客户到期日。账号密码不通过GET回显，不提供库存“查看密码”按钮；未修改敏感字段保持省略。

`GET /admin/manual-resources`、`GET /{id}`需`resources.view`；`POST/PATCH`及`POST /{id}/release`还需`resources.manage`。未分配库存仅全客户范围管理员可见/新建；已占用按实际客户范围。创建/修改/释放必填稳定key，后两者带`expected_version`；retired不可恢复，已占用时禁止改身份、凭据和库存状态，允许的成本/上游期限/备注仍可编辑。

人工交付在服务详情复用`POST /admin/services/{id}/delivery`，增加可用且未占用库存选择器，提交`job_id/manual_resource_id/resource_expected_version/expected_version/note`；必须是真实manual create waiting_manual任务。预览显示服务、资源、双方版本及交付影响，使用库存时不同时填手工username/upstream_id。成功刷新服务、任务和库存占用，不另建裸绑定接口。

终止后不会自动释放。行内“核实回收并释放”先读取绑定服务实际状态，只对terminated提供确认：资源/原服务、回收原因、不可替代资源侧实际回收。`POST /release`成功刷新库存与绑定；原回执重放不代表当前空闲，再交付必须重读当前版本。运行/暂停/待人工终止都不能释放。

409保留非敏感草稿并重新核对绑定/版本；超时保持原body/key恢复，不换key抢占；凭据仅本次内存持有，不写会话草稿或日志。手机库存卡片保留身份、双状态和期限，编辑为全宽Sheet，绑定与释放确认独立呈现，不合成一键终止释放。

## 17. 流量包商品 `/admin/traffic-products`

入口商品与服务导航，头部“新增流量包”；桌面关键词/启用筛选后为表格：ID、名称、容量GB、价格、启用、销量/库存、适用商品、创建时间、编辑/删除。点击适用商品可在受权时进入商品编辑，不代表该商品具备上游自动增流量能力。

创建/编辑抽屉分基本（名称/容量/价格）、库存（无限null或非负整数）、适用商品多选、启用；显示空适用集合的真实语义“未限制商品”，不写成无人可购。组件为整数容量输入、金额框、商品多选、库存模式单选和Switch。

读`GET /admin/traffic-products`需`products.view`，`POST/PATCH/DELETE`需`products.manage`；创建版本0，编辑/删除`expected_version`，所有写稳定key。已有订单的删除返回`traffic_product_in_use`，提示停用，不提供强删。成功刷新列表/版本/库存；未知原body/key恢复；手机改商品卡片，容量、售价和启用状态常显，适用商品折叠。

## 18. 流量包订单 `/admin/traffic-orders`

目标是分辨付款与交付，入口流量包导航；点击订单号进入详情，详情返回恢复关键词/状态/页码。桌面过滤关键词和订单状态，表列订单号、客户、商品/容量、创建时间、金额、付款状态、交付状态、付款方式、关联服务、操作。

读`GET /admin/traffic-orders`需`orders.view`；取消`POST /{id}/cancel`另需`orders.manage`，带原订单version和稳定key。仅pending展示取消候选，仍由服务端核对unpaid及未知网关结果；确认弹窗列账单、订单、金额与不产生退款的影响，不能把取消当退款。

使用双状态徽标、金额、关联链接、取消Dialog及局部重试。成功刷新订单和关联账单状态；未知保持原取消意图恢复，409重新读取不强制覆盖。缺`services.manage`不影响读取，却不能显示交付核验按钮。手机卡片首屏显示付款/交付双状态及金额，操作为完整文字。

## 19. 流量包订单详情 `/admin/traffic-orders/[id]`

头部返回订单与订单号；桌面左侧8列为订单/付款摘要及人工交付核验卡，右侧4列为客户、服务、账单关联。字段为商品快照、容量、金额、付款方式/状态、交付状态、核验说明与完成时间；不展示“资源已增加”除非有已确认核验事实。

`GET /admin/traffic-orders/{id}`需`orders.view`；`POST /{id}/delivery-verification`另需`services.manage`。仅paid且fulfillment_status=waiting_manual显示表单，包含5–2000字符的资源端实际核验说明、明确“已在资源端完成交付”确认，提交`expected_version/note/acknowledged:true`与稳定key。

此操作只记录事实，不调用上游或改变真实额度；不得提供“自动开通”“重发流量”。成功重读详情与订单列表，保留核验说明；未知用原意图恢复，不先展示成功。取消沿上页协议，退款转账单领域处理。手机摘要单列，关联卡移至核验表单下，确认显示订单/服务/容量。

## 20. 已配置模块 `/admin/apps`

入口系统应用导航，仅super_admin；返回原系统页。桌面上方类别、关键词、状态筛选，右侧“当前构建目录”跳转store兼容页；表格/卡片列模块名称/稳定ID、类别、配置来源、实例数/启用数、配置状态、schema不兼容数、配置入口。

`GET /admin/apps?view=configured`返回聚合目录与build元数据；组件为类别Tabs/选择器、状态徽标、实例计数及受控配置链接。只按API支持的category值生成可选项，不从注册类别猜某次构建必有实现；build缺版本显示未提供，不补虚构版本。

本页只读，点击`configuration_url`进入已知本地配置入口；缺失实现/不兼容保留管理行与说明，不自动删除实例。状态valid不代表外部连接成功，启用数不代表渠道可用。刷新仅更新本页聚合；手机卡片保留实现ID、来源、状态和配置入口，长版本信息折叠。

## 21. 当前构建模块目录 `/admin/apps/store`

保留旧兼容URL，页面名使用“当前构建模块目录”，入口为apps页；返回已配置模块。目标是解释已编译实现和配置去向，不设计上传zip、在线安装、卸载、拉取远端JS或重启自动装模块。

`GET /admin/apps?view=catalogue`仅super_admin；桌面类别/名称/状态筛选后使用模块卡片，字段为名称/实现ID、类别、实现/宿主/schema版本、工厂可用性、实例来源、声明能力、配置数量与入口；构建revision/modified/Go版本独立只读摘要。没有配置URL显示“由部署配置管理”，不生成不存在的设置页。

能力徽标取真实descriptor.capabilities，未知能力保留原值并说明；能力声明不等于当前资源、权限及状态允许业务动作。供应商操作读取`GET /admin/services/{id}`返回的`supplier_capabilities`并结合服务当前状态和权限；不能从此页power标签推断关机、暂停或终止权限。OAuth只有类别不虚造可安装实现。

手机模块卡片单列，能力列表可展开，版本摘要折叠。模块增加通过源码编译与成套发布处理；UI仅导航到现有配置，不以“安装完成”替代构建、worker一致性或真实提供方验收。编译/配置边界见[模块架构](../compiled-module-architecture.md)。

## 跨页写入与动态操作组件

商品/服务的系统、规格选项与真实地域可复用[现有素材规范](../admin-frontend-design.md#12-现有图片素材与图标)：`/assets/system/` 系统SVG、`/assets/country/` 国家/地区旗帜。名称/版本和地域文字来自真实返回值，图标缺失时用中性占位；图标存在不表示该系统可安装、该地域可购买或某个提供方具备操作能力。

| 组件/场景 | 必须显示的信息与交互 | 成功与未知恢复 |
| --- | --- | --- |
| 商品分区保存条 | 当前分区、未保存标记、保存/取消；其它分区保存推进revision时提醒基线变化 | 保存后重读完整商品；无回执的CAS先核对，不假设传key即幂等 |
| 共享配置影响确认 | 共享组version、受影响商品及revision、脱离范围 | 成功重读共享和商品；未知同key/body恢复，冲突不自动扩大影响集合 |
| 导入确认 | 账户版本、每项revision、比例、分类、上架状态及全部核对结果 | 仅提交与预览一致集合；回执展示原本地商品，不按新目录重组恢复body |
| 服务动作入口 | 详情`supplier_capabilities`、服务状态、在途任务、真实管理权限 | 缺能力保留不可用说明；待核验进入对应服务流程，不生成通用重发按钮 |
| 人工库存交付选择器 | 服务/任务身份、可用未占用库存、双版本及真实交付说明 | 原任务结果优先核对；绑定成功刷新三方，不能另选资源补做一次 |
| 人工库存释放确认 | 原服务已终止、资源实际回收原因、库存版本 | 原请求回执不等于当前库存空闲；重读后才能再次交付 |
| 流量交付核验 | paid/waiting_manual、容量、关联服务及实际操作证据 | 核验只登记事实；成功重读，不触发额外真实流量操作 |

实现者核对来源为[HTTP路由](../../internal/httpapi/server.go)、[活动权限映射](../../internal/httpapi/admin_permissions.go)、[商品后台投影](../../internal/store/catalog_projection.go)、[供应商批量导入字段](../../internal/store/supplier_catalog_import.go)及[资源库存契约](../domain/manual-resources.md)。这些链接用于确认真实字段和接口，不把handler源码作为给管理员显示的帮助文字。

## 实施与验收约束

每条页面按公共状态规范逐项验收，不能以截图替代交互。至少覆盖列表返回/刷新、范围撤权、跨对象异步返回、未保存离开、冲突保留、断网恢复、手机焦点/滚动及未知枚举；商品分别覆盖无限库存、共享投影、供应商只读配置和多账期。

供应商目录验收包含跨页选中、1–50上限、部分账期排除、整项不支持、预览变更、全批回滚与原key恢复；任务页面包含can_retry=false、waiting_verification及未知外部结果停止重试。库存验收包含抢占冲突、人工交付绑定、终止前拒绝释放及终止后显式释放；流量包验收保持真实资源侧交付独立。

当前文件是E0设计依据，不声明上述页面已经重做、浏览器通过、真实凭据轮换或上游资源交付完成。数据导出、信用/合同、运行时插件安装与自动流量交付均不进入本设计实施范围。
