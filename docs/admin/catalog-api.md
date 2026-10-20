# 后台商品 API 接线契约

本文供后台商品列表、商品编辑器及其关联设置接线，范围为 `catalog.core`。共同响应、管理员 Cookie、CSRF、分页和错误协议见[后台 API](api.md)；金额、目录可见性和历史快照见[商品目录](../catalog-management.md)。当前代码、迁移和测试优先于旧页面；下面“可用”指接口和规则已实现，不代表所有页面已实现，浏览器和真实供应商验证仍须单独验收。

实际 HTTP 前缀为 `/api/v1`，下面接口省略该前缀。浏览器使用现有 `api('/admin/...')`，得到解包后的 `data`。商品读取要求 `products.view`；商品写入同时检查 `products.view`、`products.manage`。分销、下载等关联领域使用各自权限，不能因为能编辑商品就默认有权修改这些设置。

## 旧编辑器十个分区如何接入

旧分区名称来自实际编辑器 `EditProduct` 的十个页签及 `ProductController`，不从旧路由数量反推需求。一个旧页签可以复用多个现有领域；不要求把所有逻辑重新塞进商品保存接口。

| 旧分区 | 当前入口和字段 | 权限与范围决定 |
| --- | --- | --- |
| `details` 详情 | `GET /admin/products/{id}`；`POST /admin/products`；`PATCH /admin/products/{id}`；分类接口见下表；准入为 `require_verified_phone`、`require_certification`、`approval_required` | 商品权限；基本资料、库存、排序、启停可用。主机名/密码生成策略、每商品购买上限、精选/隐藏/退役三个独立开关未等价恢复；不能用一个 `active` 冒充三项 |
| `pricing` 定价 | `POST /admin/products/{id}/pricing`；读取详情的 `pricing` | 商品权限；当前 CNY 整数分、多账期价格、初装费、默认账期可用。普通商品四种账期；供应商商品按已导入的支持账期。多币种、小时/日付、后付费另行设计 |
| `ontrial` 试用 | 当前没有试用资格/数量/期限接口 | 待产品确认；不显示可保存的假表单，不把价格设为零等同试用 |
| `module` 模块 | 基本资料中的 `provision_mode`、NOKVM 的 `delivery_config`；`GET /admin/provision-profiles`；供应商来源及导入接口 | 商品权限；现有人工、模拟、NOKVM、供应商交付按各自能力处理。沿用付款后或人工审核流程；未付款即开通不在当前恢复范围。供应商商品不能改写来源或通过复制绕过导入 |
| `configuration` 配置 | 详情中的 `option_groups`、`configuration_rules`；组/选项/阶梯/原生规则接口；全局共享组接口 | 商品权限；独立组、数量、层级、原生关系、全局投影可用。旧高级联动不能无损解释时明确拒绝；独立折扣/返利标志、OS/资源池同步未等价恢复 |
| `upgrade` 升级 | `GET/PUT /admin/products/{id}/upgrade-targets`；`PUT /admin/products/{id}/configuration-upgrade-policy` | 商品权限；当前人工/模拟支持的变更范围可用。供应商、NOKVM 升级能力没有因显示候选商品而自动开放 |
| `custom` 自定义字段 | `purchase_field_definitions`；`PUT /admin/products/{id}/purchase-fields` | 商品权限；购买、管理员、客户资料作用域及敏感加密字段可用；不是新增任意 `password` 字段类型，具体类型见下文 |
| `proRecommend` 推介 | `GET /admin/affiliate/rules/schema`；`GET/PUT /admin/affiliate/rules` | `affiliates.view/manage`；已有商品/续费等事件规则，复用分销领域，不重复建商品推介表或保存接口 |
| `serviceSupport` 服务支持 | `GET /admin/download-products`；`GET/POST /admin/downloads`；`GET/PUT/DELETE /admin/downloads/{id}`；文件定义的 `product_ids` 建立商品关联 | `downloads.view/manage`；业务文件下载保留，复用[下载中心](../download-center.md)。商品权限不授予文件权限 |
| `link` 快捷链接 | 客户商品页面 `/products/{id}`；公开分组读取为 `/catalog/sections`、`/categories`、`/catalog/page` | 复制商品 URL 是前端操作，无需新写接口。分组筛选公开 API 可复用；现有商城分组状态尚不是固定可书签页面 URL，不能向客户提供 API JSON 地址冒充购买页面 |

信用、合同、数据导出已经排除；客户退款/停用申请入口不恢复，客户通过工单联系管理员，管理员沿用财务和服务操作。业务下载仍保留。商品不提供物理删除；停用保留订单、服务和历史引用，不能恢复旧版通过 GET 删除商品的设计。

## 列表、基础资料与复制

| 接口 | 请求及返回 |
| --- | --- |
| `GET /admin/products/page` | 查询 `q/category_id/section_id/active/limit/offset`；返回 `{items,total,limit,offset}`。`active` 只能为空、`true`、`false`；`category_id=0` 为未分类；`section_id` 为正整数 |
| `GET /admin/products` | 兼容数组接口，保留筛选和分页参数；新列表优先使用 `/page` 获取总数 |
| `GET /admin/products/{id}` | 返回商品完整后台投影；无该商品404。停用商品仍可读取和管理 |
| `POST /admin/products` | `ProductInput`；成功201 `{id}`；可选稳定 `Idempotency-Key` |
| `PATCH /admin/products/{id}` | `ProductInput` 和最新 `expected_revision`、`expected_stock`；成功200 `{id}`；按现有表单保存完整可编辑资料，不把它当任意字段部分更新 |
| `POST /admin/products/{id}/duplicate` | `{name,slug}`，源商品编号取路径；成功201 `{id,source_product_id}`；可选稳定 `Idempotency-Key` |

字段定义以 [ProductInput](../../internal/store/catalog.go)、[ProductDuplicateInput](../../internal/store/product_duplicate.go) 为准：

| 字段 | 类型和行为 |
| --- | --- |
| `name/slug/description` | 名称非空，最长180字符；标识为1–120个小写字母、数字或短横线；说明最多10000字符。创建和复制提供新公开标识，不能只改显示名 |
| `category_id` | 正整数或null；null为未分类。一级/二级分组是否可见由服务端统一判断 |
| `sort_order/active` | 排序0–1000000；启用布尔值。创建未传active兼容默认true，编辑必须明确提交 |
| `billing_cycle/price_cents/setup_fee_cents` | 默认账期、周期价、初装费；金额0–100000000整数分，不能传元或浮点数 |
| `stock/expected_stock` | 整数或null，null为无限库存；编辑两个值分别表示新库存和原读取库存，不能把null替换成0 |
| `provision_mode` | `manual/simulation/nokvm/supplier`；supplier必须走导入，新建普通商品不能伪造来源 |
| `delivery_config` | 仅NOKVM接受，使用已配置profile及合法固定规格；其它模式不随意提交交付配置或供应商密钥 |
| `require_verified_phone/require_certification` | 可空布尔值；省略/null保持已有准入条件，明确布尔值才修改 |
| `approval_required` | 是否需要人工审核；不等于试用或免付款开通 |
| `expected_revision` | 编辑时原商品revision；新建不依赖一个尚不存在的商品版本 |

详情字段来源为 [CatalogProductProjection](../../internal/store/catalog_projection.go)，前端不要反向拼表或从公开投影补后台信息：

| 读取字段 | 用途 |
| --- | --- |
| `revision/stock` | 保存各分区的原版本和库存 |
| `category_id/category_name/section_id/category_visible` | 分类归属与当前可见性 |
| `pricing` | 账期行 `{id,billing_cycle,price_cents,setup_fee_cents,active}`；默认账期取商品的 `billing_cycle` |
| `option_groups` | 配置组、选项、层级、价格矩阵、阶梯、升级标志；组内部 `notes` 只用于后台 |
| `configuration_rules` | 原生规则及其conditions/results；公开侧只返回启用规则 |
| `purchase_field_definitions` | 字段定义，不是客户已经提交的字段值 |
| `service_group_id/service_group_name` | 已购服务导航归属，与购买分类不同 |
| `config_upgrade_enabled/config_downgrade_enabled` | 配置变更政策 |
| `supplier_catalog_mode` | `configurable` 表示导入可配置供应商商品；规格/字段按供应商导入规则维护，不当普通原生配置随意编辑 |
| `delivery_config` | 后台仅为NOKVM返回固定规格；不能把它当读取供应商凭据的通用接口 |

复制包含商品目录状态、全部账期价、配置和层级、原生规则、购买字段、升级目标及业务资料关联。副本从revision1开始；订单、服务、购物车、旧系统身份和历史审计留在源商品。供应商来源返回409 `supplier_import_required`；无法支持的旧高级联动返回409 `unsupported_product_duplicate`。

## 定价与两类分组

`POST /admin/products/{id}/pricing` 请求取 [ProductPricingInput](../../internal/store/catalog_pricing.go)：`billing_cycle/price_cents/setup_fee_cents/active/default/expected_revision`。`active`省略默认true；`expected_revision`为兼容可选，新界面始终提交。

成功200返回 `{id,product_id,billing_cycle,price_cents,setup_fee_cents,active,default}`。修改当前默认账期，即使请求 `default=false`，也同步商品基础价格。当前默认账期不可停用，先切换其它已启用账期。普通商品接受 `monthly/quarterly/annually/once`；供应商价格必须是本商品已导入并支持的周期，不能仅靠全局账期枚举生成可保存的行。具体支持周期以供应商数据和 [BillingCycles](../../internal/supplier/billing.go) 为准。

| 接口 | 字段、版本和结果 |
| --- | --- |
| `GET/POST /admin/catalog-groups/{kind}` | `kind=section/category`；创建用 `CatalogGroupInput`，成功201 `{id}`；创建可选稳定key |
| `GET /admin/catalog-groups/{kind}/page` | `q/active/limit/offset`，category另可用 `section_id`；返回分页对象 |
| `PUT /admin/catalog-groups/{kind}/{id}` | `name/slug/description/active/sort_order/expected_version`；category另有 `section_id/headline/tagline`；成功200 `{id}` |
| `DELETE /admin/catalog-groups/{kind}/{id}` | `{expected_version}`；有关联分类/商品返回409 `catalog_group_in_use` |
| `GET/POST /admin/service-groups`；`PUT /admin/service-groups/{id}` | 服务导航组字段 `name/icon/sort_order/expected_version`；icon为 `server/cloud/database/globe/box`，没有购买分类的slug或active字段；创建201、修改200 `{id}` |
| `DELETE /admin/service-groups/{id}` | `{expected_version,target_group_id}`；按[服务导航](../service-navigation.md)迁移归属，成功200 `{deleted:true}` |
| `PUT /admin/products/{id}/service-group` | `{expected_revision,group_id}`，group_id可为null；成功200 `{id}` |

购买分组字段见 [CatalogGroupInput](../../internal/store/catalog.go)，服务导航见 [ServiceGroupInput](../../internal/store/service_navigation.go)。两个资源的id、version、用途不可互换；商品归属服务组按商品revision保存。

## 配置、原生规则与购买字段

配置读取统一使用商品详情，每次成功保存后重新读取详情，获得新的revision。详细金额、数量、联动和删除约束见[商品配置](../product-configuration.md)、[购买字段](../product-purchase-fields.md)。

| 接口 | 请求字段与返回 |
| --- | --- |
| `POST /admin/products/{id}/option-groups` | `ProductOptionGroupInput`；成功201 `{id,product_id}`；兼容创建时内联options，内联项不接受阶梯变更 |
| `PUT /admin/products/{id}/option-groups/{group_id}` | 同类型，非空options不能通过更新组批量改选项；成功200 `{id,product_id}` |
| `DELETE /admin/products/{id}/option-groups/{group_id}` | `{expected_revision}`；受历史引用和全局投影保护 |
| `POST /admin/products/{id}/option-groups/{group_id}/options` | `ProductOptionInput`；成功201 `{id,product_id,group_id}` |
| `PUT /admin/products/{id}/option-groups/{group_id}/options/{option_id}` | 同类型；成功200同上 |
| `DELETE /admin/products/{id}/option-groups/{group_id}/options/{option_id}` | `{expected_revision}`；引用中的选项只能停用，不能物理删除 |
| `PUT /admin/products/{id}/option-groups/{group_id}/options/{option_id}/price-tiers` | `{billing_cycle,tiers,expected_revision}`；替换该账期全部阶梯，空数组清除该账期，其它账期不变 |
| `POST /admin/products/{id}/configuration-rules`；`PUT /admin/products/{id}/configuration-rules/{rule_id}` | `NativeConfigurationRuleInput`；新建201/修改200 `{id,product_id}` |
| `DELETE /admin/products/{id}/configuration-rules/{rule_id}` | `{expected_revision}`；成功200 `{deleted:true}` |
| `PUT /admin/products/{id}/purchase-fields` | `{expected_revision,fields}`；整体替换字段定义，最多20项；成功200 `{product_id,revision}` |

[配置组/选项类型](../../internal/store/configuration.go)字段：组使用 `name/slug/required/sort_order/active/expected_revision/options/option_type/qty_minimum/qty_maximum/qty_stage/unit/notes/upgrade_enabled/parent_group_id/option_parents`；选项使用 `label/value/price_delta_cents/setup_delta_cents/sort_order/active/expected_revision/qty_minimum/qty_maximum/parent_option_id/upgrade_enabled/price_tier_changes`。`notes`省略/null保留，空字符串清除。原生parent引用必须属于同商品；不把旧legacy ID当当前ID。

选项 `price_tier_changes` 是“账期→完整阶梯数组”，未出现的账期不变；每段字段为 `pricing_mode/qty_minimum/qty_maximum/price_cents/setup_fee_cents`，见[阶梯输入](../../internal/store/option_pricing.go)。一次选项保存原子处理元数据和所有明确提交的账期，不能先改标签再逐个保存价格并宣称整体成功。

[原生规则](../../internal/store/native_configuration_rules.go)字段为 `name/enabled/conditions/results/expected_revision`；conditions/results分别1–20项，元素为 `group_id/option_id/operator/qty_minimum/qty_maximum`。运算符为 `absent/equals/not_equals/quantity_in/quantity_out`；不同运算符对选项、数量的要求不同，由服务端验证；UI不能将无效关系静默删掉继续保存。

[购买字段定义](../../internal/store/purchase_fields.go)使用 `key/label/type/description/required/active/show_invoice/max_length/pattern/choices/sort_order/scope/encrypted/customer_profile_key`。type为 `text/textarea/select/checkbox/url`；scope为 `purchase/admin/customer`。管理员字段不能要求客户必填或进入发票；客户资料映射使用合法 `customer_profile_key`；加密字段强制不进入发票，敏感值不放URL或浏览器持久存储。不能把加密占位符当新明文提交。

## 升级与共享配置

| 接口 | 字段和版本 |
| --- | --- |
| `GET /admin/products/{id}/upgrade-targets` | 返回 `{revision,targets,candidates}`，候选包含active与compatible，候选列表不等于已授权目标 |
| `PUT /admin/products/{id}/upgrade-targets` | `{expected_revision,targets:[{product_id,allow_downgrade}]}`；最多100目标；200 `{product_id}`；可选稳定key |
| `PUT /admin/products/{id}/configuration-upgrade-policy` | `{enabled,allow_downgrade,expected_revision}`；200 `{product_id}`；可选稳定key；先启用才可允许降配 |
| `GET /admin/global-configuration-groups`；`GET /admin/global-configuration-groups/{id}` | 共享定义及关联商品当前revision；独立于具体商品组id |
| `POST /admin/global-configuration-groups`；`POST /admin/global-configuration-groups/copy` | 必须稳定key；`GlobalConfigurationInput`，复制使用source_id |
| `PUT /admin/global-configuration-groups/{id}`；`PUT .../{id}/assignments`；`DELETE .../{id}` | 必须稳定key；共享组expected_version、受影响商品expected_revisions映射，必要时acknowledge_detach |

输入来源：[升级目标](../../internal/store/product_upgrades.go)、[配置升级政策](../../internal/store/configuration_upgrades.go)、[共享配置](../../internal/store/global_configuration.go)。共享输入为 `name/description/definitions/expected_version/source_id/product_ids/expected_revisions/acknowledge_detach`。definitions是共享配置组定义和items，价格在items.prices中；不能放入商品局部层级ID。源共享组更新、投影、审计和回执同事务，脱离后的独立组不会继续被源覆盖，见[全局配置](../product-configuration.md)。

供应商目录预览/批量导入另用 `POST /admin/suppliers/{id}/import-preview`、`POST /admin/suppliers/{id}/import-batch`，来源显示用 `GET /admin/products/{id}/supplier-binding`；复用[供应商契约](../supplier-protocol.md)，不在商品编辑页重新实现上游报价、周期或选配映射。真实上游各provider是否通过需单独证据。

## 版本、未知结果与错误恢复

商品revision被基础资料、定价、配置、规则、字段、归属和升级政策等多个分区推进。库存销售不推进revision，因此基础资料同时使用expected_stock。分类、服务组、共享定义有各自version；不要共用某个全局“当前版本”。所有编辑/删除及新增选项提交最新商品版本；历史新增配置组、定价接口允许省略版本仅供兼容，新页面始终提交。

| 操作类型 | 前端恢复规则 |
| --- | --- |
| 商品创建、复制 | 一次意图预先生成8–128字符key并保留原始请求体；超时/断网用同key同请求重发。201仍返回原id，不能依据201认为生成第二个商品。更改请求体使用原key返回409 `idempotency_conflict`；新意图才换key |
| 分类创建、升级目标/政策 | 支持可选key；新页面应使用稳定key。升级表单已有原请求恢复机制，可复用 |
| 全局共享组写操作 | key必填，结合expected_version和expected_revisions；不能丢弃旧请求后自动覆盖 |
| 其余CAS接口 | 当前没有普遍的结果回执。未知结果先重新读取并核对，不能宣称“同key必定可恢复”；409保留草稿，让用户重载核对，不自动改版本重试 |
| 无key的兼容创建/复制 | 保持旧行为；相同slug可能409 `constraint_conflict`，不能从此推断上次是否成功，也不能猜测返回id |

创建/复制回执存放统一 `mutation_operations`，不新增旁路表。返回历史回执仍重新检查当前管理员主体、角色和请求原凭据；注销、撤销API key、会话/key自然过期都可能返回401。`CatalogTx`按RBAC→主体→目录→凭据锁定，等待后和提交前重新读数据库真实时钟；审计或回执等待期间过期，整个修改回滚。内部actor调用兼容与HTTP身份绑定分开，前端没有无凭据模式。

供应商连接测试是只读外部请求：`POST /admin/suppliers/{id}/test-connection`要求浏览器会话、expected_version和key；若外部读取期间注销/过期，最终记录拒绝提交，原key可能保留 `supplier_test_in_progress`。重新认证后可用新key发起一次新的只读测试；原key不能伪装为成功回执。此规则不可套用到开通、付款等可能产生外部业务副作用的操作。

| 状态/错误 | 界面行为 |
| --- | --- |
| 400 `invalid_cycle/invalid_pricing`或非法JSON/路径 | 指出输入问题；不覆盖已有表单 |
| 401 `unauthorized` | 原身份失效，停止保存并引导后台重新登录；重新认证不代表旧未知业务已失败 |
| 403 `permission_denied` | 显示权限变化，禁用操作；不能呈现为空白商品或成功 |
| 404 `not_found` | 对象不存在或当前不可读取；不自动创建替代对象 |
| 409 `version_conflict/stock_changed` | 保留当前分区草稿，重新读取并人工核对；不使用新版本静默重发 |
| 409 `configuration_in_use/catalog_group_in_use` | 展示引用约束，建议停用或调整归属；不能强制删除 |
| 409 `supplier_import_required/unsupported_product_duplicate`等领域错误 | 展示真实限制；不降级为不完整副本 |
| 500或网络异常 | 结果可能未知，按上表恢复；展示X-Request-ID，不暴露SQL、凭据或原始供应商响应 |

## 验证入口与证据边界

`TestCatalogHTTPAuthorityBindings`静态推导CatalogTx调用图，检查24个Store的HTTP边界、22个handler的原凭据绑定及路由权限，已在本轮运行通过。它证明源代码边界，不证明数据库并发或真实供应商行为。

新数据库回归入口为 `TestCatalogIntentRecovery`、`TestCatalogIntentRecoveryAfterProviderRemoval`、`TestCatalogIntentAuditRollback`、`TestCatalogCredentialAuthorityWaits`、`TestCatalogCredentialPasswordRotationLockOrder`、`TestCatalogSupplierProbeRevokedFinalizationRecovery`。其中provider移除回归验证已提交的创建回执仍可恢复，新请求继续检查当前provider与密钥。既有目录/配置/共享/购买字段/导航/供应商回归与这些新增入口使用隔离PG18验证，执行结果以严格runner逐入口run/pass及零skip为准。浏览器接线、部署和真实外部provider单独验收，不能从源码或本地数据库结果推断通过。最终证据状态以[领域验收矩阵](../architecture/contract-verification-matrix.md)和本轮实际门禁结果为准，机器工件放output。
