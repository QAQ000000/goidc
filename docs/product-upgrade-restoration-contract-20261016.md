# 商品升级编辑与客户变更闭环契约

状态：下一批只读契约规划，未开发、未重新验收。起点 `a69bc3b`。本批不把现有升级API或历史验收重复登记为新完成。

## 当前能力

商品升级策略已有真实管理员API：`GET /api/v1/admin/products/{id}/upgrade-targets`（`products.view`）返回源revision、targets和兼容性候选；`PUT`（`products.manage`）提交`expected_revision`、目标及`allow_downgrade`，限制重复、自身、不兼容和最多100项。配置升降级策略已有`PUT /api/v1/admin/products/{id}/configuration-upgrade-policy`（`products.manage`）。对应源码为 `internal/httpapi/product_upgrades.go`、`internal/httpapi/configuration_upgrades.go`，路由见 `internal/httpapi/server.go:346-349,571`。

客户流程已经存在，不能表述为整体缺失：`GET /api/v1/services/{id}/product-upgrade-options`和`POST /api/v1/services/{id}/product-quotes`只允许服务本人；客户随后通过已有 configuration-change、账单付款和人工/模拟交付流程完成变更。接口和经济边界见 `docs/service-product-changes.md`。已有测试覆盖报价、取消、提交、付款、人工确认、导航恢复、终止及原订单退款防重复：`internal/httpapi/product_upgrades_test.go`、`configuration_upgrades_test.go`、`product_upgrades_browser_test.go`。

现有商品编辑器已实现基本信息、定价、服务归属，以及本地商品的配置项、配置规则、购买字段；供应商品有供应来源只读摘要。源码 `web/src/components/product-editor.tsx:25,35,41`。升级目标与配置策略组件虽已存在（`web/src/components/product-upgrades.tsx:18`、`web/src/components/configuration-upgrades.tsx:19`），但未挂载到商品编辑器的分区。旧规范十区见 `/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:2182`。

## 本批最小范围

1. 在 `/admin/products/{id}` 增加“升级选项”分区，挂载现有目标路径与配置升级策略组件，保持产品revision分区草稿、取消、显式重新加载和冲突提示。
2. 目标路径编辑提供目标搜索/兼容性、降级允许开关、保存/取消；沿用现有API、权限、`expected_revision`、最多100项和不兼容拒绝。供应商商品不假定支持真实升级；能力未知时显示只读/拒绝配置并由后端再次拒绝。
3. 将同一分区接入现有客户报价入口，明确商品revision、服务version、目标价格和报价有效期；客户只能提交本人服务。报价取消、提交、付款、人工/模拟交付及未知结果沿用既有状态机，不重写升级引擎。
4. 补齐浏览器可见的加载/错误/空状态、撤权、版本409、报价过期、目标下架或改价、库存不足、重复付款和交付失败恢复提示。

## 十区边界

详情、定价、自动开通、产品配置、自定义字段已有部分实现；试用、独立链接、编辑器内商品推介计划、文件下载分区仍是未完整映射。升级选项是本批重点；推介、下载、试用和链接不在本批顺带补齐。独立下载中心、affiliate规则、配置项高级字段及升级优惠已有其他API/页面，不能据此声称商品编辑十区完整。

## 经济与交付保护

目标周期价、剩余周期差额、初装费不收取、零金额账单、库存预留与释放、源/目标商品切换、续费价格、原订单快照和服务合同快照必须沿用 `docs/service-product-changes.md` 的已实现语义。目录改价不改变已生成报价；交付确认前不得切换服务商品；未知上游结果不得自动重发、扣款或提前改变本地服务。真实供应商升级能力不由本地适配器注册信息推断，需单独能力和回执契约。

## 权限与接口边界

管理员查看/保存路径分别需`products.view`/`products.manage`；客户升级选项和报价需本人服务。只读管理员可查看策略和历史，写接口必须403；撤销权限、服务归属变化、商品/服务revision变化、报价过期均由服务端拒绝。继续使用现有幂等键、版本字段、账单和配置变更接口；不新增删除API，不修改原订单经济条款，不接逾期暂停worker。

## 验收标准

- 隔离PG18：目标兼容/不兼容、上架状态、价格和revision变化、库存预留/释放、报价过期、双编辑409、重复付款、未知交付结果、审计失败回滚、权限撤销和客户越权。
- 浏览器1440/1024/390/320：后台升级分区搜索/保存/取消/409；客户目标选择、报价、取消、付款、进度、失败重试和刷新恢复；只读和撤权隐藏/拒绝。记录pageerror、console error、HTTP403/409及所有未验证项。
- 回归：改价不改已生成报价，交付成功后才切换服务商品，续费使用新周期价，原订单及已生成合同快照不变；重复终止/退款不重复释放库存。

本契约只描述下一批最小经营闭环；历史升级实现与旧十区均不得在未重新运行隔离验收前标为本批完成。
