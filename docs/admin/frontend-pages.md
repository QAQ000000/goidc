# 后台前端页面设计目录

本文回答“需要哪些页面、每页如何排版、使用哪些组件、用户如何完成操作”。公共视觉与组件状态见[设计总契约](../admin-frontend-design.md)；精确字段、权限和写入规则以[后台API](api.md)、[客户API](customer-api.md)、[商品API](catalog-api.md)、[服务API](service-management-api.md)为准。不能只依据接口数量生成菜单，也不能只画有数据的成功页面。

## 范围与使用方式

当前源码 `web/src/app/admin/**/page.tsx` 共106个路由模式，其中4个信用/合同路由退役，102个其余路由含兼容入口和共享页面；另有独立 `/admin-login`。人工库存已有独立UI路由 `/admin/manual-resources`，新建/编辑/详情由同页抽屉承载。这些数字是路由覆盖范围，不能作为已实现页面数、菜单项数或验收通过数。

当前39组批准必要API对应本轮设计范围；10组明确排除和7组暂缓项目沿用[需求取舍](api-requirements.md)，不因旧版截图出现而加入设计。客户自助退款诉求走工单，由管理员在账单处置；不增加自助申请页。保留业务文件下载，不增加报表导出。状态纠错是受限动作，不能画成任意状态下拉保存。

| 阅读层次 | 需要解决的问题 |
| --- | --- |
| 本目录 | 是否遗漏列表/新建/详情/编辑、共享子视图、兼容入口和拟新增页面 |
| [设计总契约](../admin-frontend-design.md) | 外壳、颜色/尺寸、模板、基础与业务组件、局部状态、响应式、交付单 |
| 下列四份领域规格 | 每页区域顺序、字段/列、UI组件、入口返回、动作步骤、代表API/权限、成功刷新与异常恢复 |
| 对应领域API契约 | 确切DTO、资源自身version、quote、owner_generation、稳定key和业务拒绝条件 |

表中“现有路由”只表示页面入口存在；实际接线状态继续查 `audit/admin-api-requirements.json` 的UI轴。本文新增规格均为设计要求，本轮不宣称浏览器、真实接口全链路、上游或部署验收通过。

## 页面清单

页面路由模式是设计标识；`[id]` 用于Next页面，API的 `{id}` 用于HTTP参数。每行都必须在对应领域规格中有页面设计；多个页面可共用组件，但不得丢掉新建/编辑/详情差异。页面中的页签/弹窗/步骤由领域规格展开，无需为每个动作新增URL。

### 客户、订单与服务

逐页规格：[打开客户、订单与服务设计](frontend-customer-service.md)。

| 页面路由 | 页面/视图 | 设计归属状态 |
| --- | --- | --- |
| `/admin/cancellations` | 取消处置 | 现有路由 |
| `/admin/certifications` | 实名认证审核 | 现有路由 |
| `/admin/customer-fields` | 客户自定义字段 | 现有路由 |
| `/admin/customer-files` | 客户文件 | 现有路由 |
| `/admin/customer-groups` | 客户分组与折扣 | 现有路由 |
| `/admin/customer-levels` | 客户等级 | 现有路由 |
| `/admin/customer-resources` | 未分配销售客户池 | 现有路由 |
| `/admin/customers/[id]/files` | 指定客户文件 | 现有路由 |
| `/admin/customers/[id]` | 客户详情及十个页签 | 现有路由 |
| `/admin/customers/new` | 新建客户 | 现有路由 |
| `/admin/customers` | 客户列表 | 现有路由 |
| `/admin/file-groups` | 公共文件分组 | 现有路由 |
| `/admin/orders/[id]` | 订单详情 | 现有路由 |
| `/admin/orders/new` | 管理员代下单 | 现有路由 |
| `/admin/orders` | 订单列表 | 现有路由 |
| `/admin/services/[id]` | 服务详情及六批管理动作 | 现有路由 |
| `/admin/services` | 服务列表 | 现有路由 |

### 商品、供应商与资源

逐页规格：[打开商品、供应商与资源设计](frontend-catalog-resources.md)。

| 页面路由 | 页面/视图 | 设计归属状态 |
| --- | --- | --- |
| `/admin/apps` | 编译模块目录 | 现有路由 |
| `/admin/apps/store` | 编译模块能力视图 | 现有路由 |
| `/admin/catalog-groups/new` | 新建目录分组 | 现有路由 |
| `/admin/catalog-groups` | 商品目录分组 | 现有路由 |
| `/admin/configurable-options` | 共享可配置项 | 现有路由 |
| `/admin/products/[id]` | 商品编辑及配置页签 | 现有路由 |
| `/admin/products/new` | 新建商品 | 现有路由 |
| `/admin/products` | 商品目录 | 现有路由 |
| `/admin/resources` | 已购服务资源视图 | 现有路由 |
| `/admin/manual-resources` | 独立人工库存列表及新建/编辑/详情/释放抽屉 | 现有路由 |
| `/admin/service-groups` | 客户服务导航组 | 现有路由 |
| `/admin/suppliers/api-settings` | 供应商协议与API设置 | 现有路由 |
| `/admin/suppliers/catalog` | 供应商商品与导入 | 现有路由 |
| `/admin/suppliers/jobs` | 供应商任务状态 | 现有路由 |
| `/admin/suppliers/orders` | 供应商订单 | 现有路由 |
| `/admin/suppliers` | 供应商账户 | 现有路由 |
| `/admin/suppliers/renewals` | 供应商续费 | 现有路由 |
| `/admin/suppliers/tasks` | 供应商交付任务 | 现有路由 |
| `/admin/traffic-orders/[id]` | 流量包订单详情 | 现有路由 |
| `/admin/traffic-orders` | 流量包订单 | 现有路由 |
| `/admin/traffic-products` | 流量包商品 | 现有路由 |

### 财务、工单与运营

逐页规格：[打开财务、工单与运营设计](frontend-operations.md)。

| 页面路由 | 页面/视图 | 设计归属状态 |
| --- | --- | --- |
| `/admin/affiliate` | 推介计划 | 现有路由 |
| `/admin/affiliate/settings` | 推介政策 | 现有路由 |
| `/admin/channels` | 统一渠道实例 | 现有路由 |
| `/admin/invoice-applications/[id]` | 发票申请详情 | 现有路由 |
| `/admin/invoice-applications` | 发票申请审核 | 现有路由 |
| `/admin/invoices/[id]/edit` | 编辑人工账单 | 现有路由 |
| `/admin/invoices/[id]` | 账单详情及退款核验 | 现有路由 |
| `/admin/invoices/new` | 新建人工账单 | 现有路由 |
| `/admin/invoices` | 账单列表 | 现有路由 |
| `/admin/marketing` | 营销推送 | 现有路由 |
| `/admin/notifications/new` | 创建通知模板 | 现有路由 |
| `/admin/notifications` | 通知模板与队列 | 现有路由 |
| `/admin/promo-codes` | 优惠码 | 现有路由 |
| `/admin/reports` | 六类统计报表 | 现有路由 |
| `/admin/sales-settlements` | 销售结算 | 现有路由 |
| `/admin/ticket-departments` | 工单部门 | 现有路由 |
| `/admin/ticket-presets` | 预设回复 | 现有路由 |
| `/admin/ticket-rules` | 工单规则 | 现有路由 |
| `/admin/ticket-statuses` | 工单状态 | 现有路由 |
| `/admin/tickets/[id]` | 工单详情与回复 | 现有路由 |
| `/admin/tickets/new` | 管理员代建工单 | 现有路由 |
| `/admin/tickets` | 工单列表 | 现有路由 |
| `/admin/transactions` | 统一交易流水 | 现有路由 |

### 工作台、内容与系统

逐页规格：[打开工作台、内容与系统设计](frontend-system.md)。

| 页面路由 | 页面/视图 | 设计归属状态 |
| --- | --- | --- |
| `/admin/access` | 管理员与角色 | 现有路由 |
| `/admin/access/staff/[id]` | 管理员编辑 | 现有路由 |
| `/admin/account` | 个人账户 | 现有路由 |
| `/admin/audit` | 操作审计 | 现有路由 |
| `/admin/auth-settings` | 登录注册与验证码 | 现有路由 |
| `/admin/blacklist` | 登录黑名单 | 现有路由 |
| `/admin/certification-settings` | 实名认证设置 | 现有路由 |
| `/admin/content` | 新闻与知识库 | 现有路由 |
| `/admin/contract-settings` | 合同设置（退役） | 退役，404 |
| `/admin/contracts` | 合同（退役） | 退役，404 |
| `/admin/credit` | 信用额度（退役） | 退役，404 |
| `/admin/credit-settings` | 信用设置（退役） | 退役，404 |
| `/admin/currencies` | 币种展示配置 | 现有路由 |
| `/admin/downloads` | 下载中心 | 现有路由 |
| `/admin/finance-settings` | 充值与财务设置 | 现有路由 |
| `/admin/friendly-links` | 友情链接 | 现有路由 |
| `/admin/invoice-settings` | 发票设置 | 现有路由 |
| `/admin/job-settings` | 调度配置 | 现有路由 |
| `/admin/jobs` | 任务概览 | 现有路由 |
| `/admin/jobs/queue` | 任务队列 | 现有路由 |
| `/admin/logs/[kind]` | 系统日志及兼容跳转 | 现有动态/兼容入口 |
| `/admin/logs/admin-login` | 管理员登录日志 | 现有路由 |
| `/admin/logs/api` | API日志 | 现有路由 |
| `/admin/logs/cleanup` | 日志清理 | 现有路由 |
| `/admin/logs/email` | 邮件日志 | 现有路由 |
| `/admin/logs/inbox` | 站内信日志 | 现有路由 |
| `/admin/logs/jobs` | 调度日志 | 现有路由 |
| `/admin/logs/sms` | 短信日志 | 现有路由 |
| `/admin/migrations` | 旧数据复核 | 现有路由 |
| `/admin/navigation` | 官网导航 | 现有路由 |
| `/admin/order-settings` | 订购设置 | 现有路由 |
| `/admin` | 工作台 | 现有路由 |
| `/admin/payment-settings` | 支付渠道实例 | 现有路由 |
| `/admin/sales-settings` | 销售规则 | 现有路由 |
| `/admin/security/mfa` | MFA政策 | 现有路由 |
| `/admin/settings` | 站点设置 | 现有路由 |
| `/admin/site-fields` | 官网自定义字段 | 现有路由 |
| `/admin/site-settings` | 站点设置兼容入口 | 现有兼容入口 |
| `/admin/sms-settings` | 短信渠道实例 | 现有路由 |
| `/admin/smtp-settings` | 邮件渠道实例 | 现有路由 |
| `/admin/system/about` | 关于系统 | 现有路由 |
| `/admin/system/database` | 数据库状态 | 现有路由 |
| `/admin/system/runtime` | 运行环境 | 现有路由 |
| `/admin/system/upgrade` | 升级检查 | 现有路由 |
| `/admin/theme-settings` | 官网主题 | 现有路由 |

### 独立登录

| 路由 | 状态 | 规格与承载方式 |
| --- | --- | --- |
| `/admin-login` | 现有独立登录入口，位于管理外壳之外 | [工作台与系统](frontend-system.md)：登录、验证码、MFA及安全跳转 |

### 必须单独设计的子视图

| 宿主页面 | 不可省略的视图或步骤 |
| --- | --- |
| 客户详情 | 摘要、资料、服务、账单、交易、工单、日志、通知、文件、推广；分组/销售/充值/未付充值/推荐人/重置密码/受控代登录分别授权 |
| 服务列表与详情 | 服务操作与任务；单服务/同客户批量代续费；长期改价/本次固定价；客户期限和上游观测；状态纠错；人工收款；双方范围/代次明确的转移 |
| 商品新建与编辑 | 基本、价格/账期、交付、配置项、升级策略、购买字段、推广、业务文件与链接；未批准旧功能不加入表单 |
| 供应商目录 | 选商品→比例和本地分组→完整账期/配置核对→选择可导入项→确认→逐项结果及原key恢复 |
| 账单与工单 | 余额退款/外部退款证据与核验分开；工单公开回复/内部备注、上传结果恢复、每条合格消息评分分开 |
| 报表 | 年度现金、客户全历史排名、产品、每日运营、销售、工单六种视图；各自筛选、权限、局部状态，绝不互相阻断 |
| 通知与渠道 | 事件/模板/队列、统一通知历史、渠道实例、配置schema、pending/unknown；营销与工单历史保持各自范围 |
| 系统与权限 | 管理员/角色/客户范围/部门范围；本人安全与系统政策；只读运行/数据库/升级；内部调度和任务核验 |

## 页面排版交付

每页按“页头/身份→筛选或摘要→主内容→次要信息/关联→结果和分页”说明顺序，桌面明确单/双栏及宽度，平板说明折叠，手机说明卡片/内部滚动或全屏Sheet。表格列、表单字段分区、按钮主次必须具体列出，不能只写“参考旧系统”。

复杂详情用文字线框表达区域，例如客户/服务：

```text
返回原列表及筛选  /  对象名称与编号  /  状态  /  受权主操作
身份与摘要：客户、金额口径、客户期限/上游期限、更新时间
主区：当前页签的表格/表单/任务                 侧栏：关联对象/说明
局部错误/待核对结果、下一步与明确的恢复入口
```

这是排版约束，不强制所有详情都有右栏；无关联数据的列表不新增空侧栏。高频数据优先于说明，帮助文本就近放字段旁。桌面表头/金额/主操作不能因栏宽不足挤成无法辨识的图标。

## 组件与交互交付

基础组件复用现有shadcn/Radix；页面头、筛选、对象摘要、状态、预览确认、批量结果、写入恢复和授权文件按[组件规范](../admin-frontend-design.md#11-组件与业务动作规范)组合。新业务组件必须标明拟实现，不能把组件清单当作现成库。

每个动作写清“何时可点→先读什么→输入/校验→是否预览→确认什么→发哪个请求→等待和失败如何显示→成功刷新哪些分区→如何返回”。读与写不同：查看/预览/GET进度不产生收款、续费、发消息或交付；提交后关窗也不代表取消在途业务。

完整结果状态包括加载、无数据、无筛选结果、无权限、401/404、字段错误、409、成功、异步处理中及未知写结果。颜色仅辅助，文字给出含义；无权限/null不能变成0。未知结果保留原意图，根据该动作的实际协议查询或原key恢复，不做全站统一自动重试。

## 实施顺序与验收交接

| 批次 | 页面与目标 | 交付要求 |
| --- | --- | --- |
| A | 公共外壳/组件＋客户列表/新建/详情/资料样板 | 首先打通真实摘要、固定客户上下文、局部状态、返回筛选、分组/销售/充值恢复；桌面与手机一起完成 |
| B | 服务详情与六批管理动作、订单和人工库存 | 先只读与单项，再批量；真实权限/版本/报价/归属围栏，不能仅靠禁用按钮 |
| C | 商品编辑、完整供应商导入、配置与流量包 | 完整价格/账期/选配、明确阻断原因、导入全批原子提交及原key恢复；异步交付另行核验 |
| D | 账单、发票、交易、工单 | 资金口径、退款分路、附件恢复、公开/内部信息、部门范围及评分 |
| E | 通知/渠道、报表、内容、安全/设置、日志与系统 | 统一目录/独立视图加载、密钥保护、无导出/无运行时安装、只读运行及升级边界 |

每批先按页面ID提供排版和全部适用状态稿，核对字段与API后实施。HTML/Figma等设计稿可作为样稿，但API、权限和状态契约继续有效；不能以视觉稿中的演示按钮创造后台能力。记录设计稿、React接线、E1数据库/权限测试、E2浏览器、E3真实外部及发布证据各自状态，未执行项标“未验证”。

浏览器至少覆盖1440/1024/768/390/320宽度、键盘Tab与弹窗焦点、长中文/金额/错误、慢请求与乱序响应；验证超级管理员、只读管理员、受限客户/部门范围、撤权和登录失效。复杂写入另验成功/拒绝/冲突/超时/重复提交和恢复。前端typecheck、lint与构建通过仅说明对应静态/构建范围，不替代上述运行验收。
