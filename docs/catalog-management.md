# 商品目录与维护

源行为参考旧 `app/admin/controller/ProductController.php` 的一级/二级分组、排序、关联删除保护和商品编辑，以及 `app/home/controller/CartController.php` 的目录可见性。`ConfigGeneralController.php` 中的 `nav_group` 是已购服务导航，schema039已单独实现，见 [服务导航](service-navigation.md)。schema063新增五种普通[购买字段](product-purchase-fields.md)，商品复制包含其定义；旧购买模板、分组支付网关、密码/管理员专用字段、别名路由和供应商同步仍待实现。

## 分组与商品接口

迁移 `037_catalog_management.sql` 新增一级 `product_sections`，为原 `product_categories` 增加父分组、说明、标题、标语和版本，为商品增加排序。旧无父分类继续可见，新建/保存二级分类必须指定一级分组。未分类商品仍可购买。公开可见要求商品、二级分类及一级分组均启用；列表、详情、购物车添加/修改及结算统一应用此规则。隐藏不会改变已购服务、订单价格快照或成功结算的幂等重放。

| API | 行为 |
| --- | --- |
| `GET /api/v1/catalog/sections`、`/categories` | 按排序和 id 返回启用分组/可见分类 |
| `GET /api/v1/catalog/page` | `q/category_id/section_id/limit/offset`；返回 items/total/limit/offset |
| `GET /api/v1/admin/products/page` | 同上，额外 active=true/false；包括隐藏商品 |
| `GET /api/v1/admin/products/{id}` | 管理详情，含价格、分类可见性、库存和 revision |
| `POST /api/v1/admin/products` | 创建商品及默认账期价格 |
| `PATCH /api/v1/admin/products/{id}` | 全量替换界面可编辑字段，要求 expected_revision 和 nullable expected_stock |
| `POST /api/v1/admin/products/{id}/duplicate` | 复制目录状态（商品、账期价格、配置组/选项/账期价格、产品资料关联），使用新名称和标识；不复制订单、服务、购物车、旧身份或审计历史 |
| `GET/POST /api/v1/admin/catalog-groups/{kind}` | kind 只允许 section/category；读取或创建 |
| `GET /api/v1/admin/catalog-groups/{kind}/page` | 后台分组分页；q、active、limit/offset；category可用精确section_id，字段与错误见[后台API](admin/api.md#资源与商品读取) |
| `PUT/DELETE /api/v1/admin/catalog-groups/{kind}/{id}` | 保存/删除要求 expected_version；有关联分类/商品拒绝删除 |
| `POST /api/v1/admin/products/{id}/pricing` | 编辑账期价格/启用状态/默认账期；兼容可选 expected_revision |

原目录及后台商品数组接口保留。category_id=0 表示未分类，section_id 必须为正整数。搜索名称、标识、描述，按 sort_order/id 排序；后台页面每页 20，购买页面每页 12。页面筛选使用组件状态，暂不提供可书签化筛选 URL 或旧别名兼容。

后台读取要求 products.view，修改同时要求 products.view/manage。商品、价格和分组修改在事务中先获得管理员管理共享锁，再获得目录互斥锁，等待后重新验证账号及当前角色。购物车修改和结算先获取目录共享锁再锁商品，确保等待后检查最新可售状态。修改与审计原子提交，冲突不留下半成品。

商品字段为 name/slug/description/category_id/sort_order/active/billing_cycle/price_cents/setup_fee_cents/stock/provision_mode/approval_required。金额为整数分，0–100000000；库存 null 为不限量，整数范围 0–2147483647；排序 0–1000000。名称最长 180 字符、说明 10000；商品标识沿用小写字母、数字、短横线，最长 120。分组名称 255、说明 1000、分类标题/标语 255，标识以小写字母开头。商品 JSON 限 256 KiB。商品不提供删除，保留历史关联。

编辑商品推进 revision，并同步启用默认账期价格。即使价格接口 default=false，修改当前默认账期也同步商品展示金额；当前默认账期不能停用，必须先切换。界面切换默认账期预填已有金额。checkout 消耗库存不推进商品 revision，因此后台同时比对 expected_stock，过时库存返回 409 stock_changed；版本冲突返回 409 version_conflict，页面提供重载。此保护避免后台保存覆盖已售库存。旧服务的周期价格仍遵循服务/原订单项快照。

入口 `/admin/products`、`/admin/products/new`、`/admin/products/{id}`、`/admin/catalog-groups`，只读管理员可查看但不可保存。客户首页提供一级导航、分类介绍、搜索及分页；列表只展示商品摘要、库存、默认账期基础价和独立详情入口，账期、选配和购买资料统一在 `/products/{id}` 配置。后续 schema 038 已补齐当前单选配置组编辑/启停/排序/删除保护及禁用组公共过滤，见 [商品配置维护](product-configuration.md)，旧复杂配置类型/联动/共享组仍待迁移。

## 客户选购与购物车

参考旧主题 `product.tpl → configureproduct.tpl → viewcart.tpl` 的职责划分，当前使用“商品目录 → 独立商品详情/配置 → 购物车确认”。沿用数字 ID 的 `/products/{id}`，不改已有 canonical、JSON-LD、站点地图或公开目录可见性。

- 目录卡片展示名称、说明、库存与默认账期基础价；存在选配时明确配置费用另计，不能把基础金额称为最终应付。商品名称与“配置购买/查看详情”均进入详情页；列表不渲染规格或购买资料，不创建购物车项。
- 详情保留独立的商品介绍、付款周期、可配置规格、购买资料和购买件数。购买件数为 1–100，与每件服务的配置数量分开；数量型规格按范围、步长、零值能力和当前账期阶梯校验，多级选择和原生关系复用既有配置规则。
- 桌面使用主配置区与右侧摘要；移动端按单列展示，摘要和提交入口可操作且不撑破视口。摘要列出所选规格、周期费用、初装费和多件参考总额；客户组折扣与优惠码以购物车的服务端报价为准，详情不伪造净价或续费合同价。
- 加购前重新检查 `/me` 的客户身份，再读取购物车当前版本并提交。匿名可预览配置，提交时引导登录；后台 Cookie 不影响前台身份与配置；仅登录后台时，前台仍为匿名，可独立登录客户账号购买。购买资料只保存在当前组件内存和受认证的 POST/PATCH 中，不进入链接、浏览器持久存储、公开元数据或价格摘要。
- 库存不足、停售、缺失价格、无效规格/数量、必填资料、加载失败和购物车版本冲突提供明确状态；目录与详情重试不修改购物车。商品版本变化后重新核对配置。成功加购进入购物车，最终计价、库存、准入与规则仍由 API 复验。
- 购物车商品名称链接回详情；编辑配置保留现有行的账期、配置、数量和资料，保存 PATCH 原行，取消不修改。编辑、删除、刷新和窗口重新激活仍清空结算选择；仅当前非空所选项可以请求报价和创建订单，不沿用旧系统的“未传选择即结算全车”回退。

组件回归入口为 `node --test scripts/product-purchase-components.test.mjs scripts/customer-admin-isolation-components.test.mjs scripts/purchase-discount-components.test.mjs`。真实浏览器使用独立测试库的 `TestProductPurchaseBrowserFixture`，覆盖目录进入详情、账期/规格/资料/多件加购、购物车编辑及最终订单快照；浏览器与数据库证据放 `output/`，不等同于已部署或真实上游开通。

## 验证边界

2026-10-05 强制隔离 PG18 和真实旧库只读演练的全量 `go test ./... -count=1` 通过（HTTP API 包 40.042s、legacy import 4.439s），`go vet ./...` 通过。前端 lint、类型检查、Turbopack 主目录构建和 webpack 隔离预览构建通过。

数据库专项覆盖两级隐藏跨目录/详情/购物车/结算、成功订单重放、既有服务快照、版本与真实购买后的库存冲突、默认价格同步、账期切换、默认禁用拒绝、分页排序、权限及密钥撤销、并发分组/商品修改、审计失败回滚、旧平铺分类迁移、等待结算重新检查可见性及等待编辑撤权。

真实隔离生产构建浏览器验证两级分组和商品创建、基本金额/排序编辑、价格矩阵/年付设默认、父分组隐藏后公共列表为空及详情 404、恢复、客户分组/分类介绍/搜索/实际加入购物车、商品并发冲突后重载、13 个商品的 12/1 分页及搜索重置。年付显示 12345 分及初装 234 分，购物车总额 12579 分。只读管理员控件禁用、无保存按钮、直接修改 API 403、新建路由返回目录。390×844 商品编辑及客户目录的 scrollWidth 均为 390；分组弹窗在视口内。更多并发/购买库存/既有服务情形由数据库测试证明。fixture 899.108s 正常结束并删除随机 schema；浏览器证据位于忽略目录 `output/playwright/catalog-acceptance-20261005.md`。

商品复制的隔离测试验证了基本目录、全部账期价格、配置组/选项及配置账期价格、产品资料关联均被复制，副本 revision 从 1 开始，且订单、服务、购物车、旧身份和历史审计不会被复制；源商品与副本在同一目录事务和商品锁内完成。当前尚未做部署后真实浏览器点击验收。

复制 API 已部署到开发实例：schema 049、API/worker/Next.js 均 active，`/healthz`、`/readyz` 返回 200；root 登录后读取商品目录成功，非法复制请求返回预期 409，随后注销。部署前后 users/products/orders/order_items/services/invoices 摘要一致，备份为 `bin/previous/20261005T121724Z/schema049.dump`（255083 bytes），dump 目录可读取。尚未进行恢复/完整切换回滚演练。

开发实例已部署迁移 037 及新 API/worker/Next.js，healthz/readyz 为 200。部署前后原商品（忽略新增 sort_order 字段）、订单和服务完整 JSON 聚合校验一致；原 root、acceptance-customer 两个有效账户保留，零购买分组/分类、零内容/导入批次，站点版本 1、注册开启、维护关闭。备份 `bin/previous/20261005T035700Z/` 含 schema 036 custom dump 和三个旧二进制，dump 目录可读取；恢复/完整切换回滚尚未演练。root 部署后仅查看目录、商品基本及价格、空白分组弹窗，未提交业务修改；公共页面保留 4 个商品。临时 3089 进程、schema、预览文件已清理。旧 PHP/MySQL 未修改。
