# 后台客户与订单批次验收（2026-10-11）

本批优先补齐客户/订单域的续费订单缺口。续费发票不在 `orders` 表中生成普通订单行，因此新增独立真实 API 和后台视图，避免把续费账单误作为可取消或可审核的首购订单。

## 已交付

- `GET /api/v1/admin/renewal-orders`：从 `invoice_items.item_type='renew'` 读取真实数据。
- 支持客户范围、状态、关键词、`limit/offset`，返回 `items/total/limit/offset`。
- 后台 `/admin/orders?kind=renew` 显示客户、产品、IP/服务域名、续费时间、金额、付款方式和账单状态。
- `kind=renew`、状态、关键词和分页均保留在 URL；列表只提供账单详情入口。
- `orders.view` 权限和普通管理员客户范围由 Go API 执行，客户账号访问返回 `403`。

## 验收证据

- PG18 隔离专项：`TestServiceRenewalInvoiceAndPayment` 覆盖待支付续费列表、关键词/状态筛选、分页字段和客户越权拒绝。
- `scripts/go.sh vet ./...`：通过。
- `npm run typecheck --prefix web`：通过。
- `npm run build --prefix web`：通过。
- Playwright 四尺寸截图：
  `output/playwright/admin-batch-customer-orders-20261011/renewal-1440.png`
  `output/playwright/admin-batch-customer-orders-20261011/renewal-1024.png`
  `output/playwright/admin-batch-customer-orders-20261011/renewal-390.png`
  `output/playwright/admin-batch-customer-orders-20261011/renewal-320.png`

运行库部署后页面显示真实续费账单空态/记录，320px 页面 `scrollWidth` 等于视口宽度；本批未执行取消、退款或供应商写操作。
