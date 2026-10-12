# 供应商订单与续费列表首批

旧规范第 92/93 页对应 `/admin/suppliers/orders`、`/admin/suppliers/renewals`。真实分页 API 为 `GET /api/v1/admin/supplier-orders` 与 `GET /api/v1/admin/supplier-renewals`，均要求 `orders.view`，复用 `admin_customer_allowed` 客户范围。

供应商归属只由 `services.provision_mode='supplier'` 且不可变 `delivery_snapshot.supplier_id` 证明，不读取当前商品绑定。订单筛选包括关键词、状态、客户 ID、供应商 ID、上海日期区间、付款状态；续费提供关键词、付款状态、客户、供应商、日期。总数与分页项目使用同一 SQL 快照，空页保留总数。订单金额为完整客户订单金额（混合订单不是供应商分摊金额）。成本仅按服务显示已成功开通任务保存的 CNY 成本；缺失返回 null，不估算成本或利润。

订单页复用现有创建、核验、取消与删除界面及后端保护（权限、确认、版本、幂等、审计）；不会额外调用或伪装上游动作。详情通过 `source=supplier` 和 URL 筛选/页码恢复列表上下文。续费页展示供应商服务关联的本地续费账单，不代表上游续费完成；成本返回 null，未调用上游读写。账单详情使用受限站内返回路径恢复续费筛选。

不新增迁移，不修改恢复台账。普通订单列表和详情原有行为保留。缺少快照证明的历史订单不会进入供应商列表；上游续费尚不支持，列表允许真实空态。浏览器验收由主代理合入后执行，本分支未宣称浏览器验收通过。

验证：强制隔离 PostgreSQL 18 专项及 race（`TestSupplierOrders|TestOrderMutations`）；前端 typecheck；diff whitespace 检查。专项覆盖非空订单/本地续费、没有当前绑定仍按快照入列、客户归属、权限撤销、筛选校验、空页总数、真实成本及续费 null 成本、敏感字段不泄露。
