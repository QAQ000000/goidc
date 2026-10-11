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

## 断点续作：订单列表与流量账单结算

- 新增注册 `GET /api/v1/admin/orders/page`，返回准确 `items/total/limit/offset`，旧数组接口保留兼容。
- 普通订单显示真实产品快照、服务域名、已付款渠道/余额、账单状态、订单备注和客户销售归属；它们分别来自订单/服务/账单/客户记录，未把管理员备注当成客户提交备注。
- 普通/续费列表支持 25/50/100 页容量，保留关键词、状态、客户 ID、页码；详情返回保留这些条件。续费“下单时间”使用账单创建时间，不将付款截止时间当续费时间。
- 单个取消、批量取消和订单审核增加确认，取消成功刷新列表；订单写操作版本保护及旧版新建/删除仍待后续实现，第8页调整为部分完成。
- 流量包账单复用现有余额/在线支付，付款仅进入待人工交付；未交付余额退款与取消各释放一次库存，已核验交付拒绝自动退款。自动上游流量额度交付尚未实现，见 [流量包验收](admin-batch-traffic-acceptance-20261011.md)。

### 本轮验证

- 强制隔离 PG18 `scripts/go.sh test ./...`：全部通过，httpapi 524.121s；后续幂等修正/续费关联收紧再次专项通过。
- `TestAdminOrderPagesScopeFiltersAndTotals`：普通/续费列表总数、offset、关键词/状态/客户筛选、真实空数组、销售客户范围、缺失权限和非法筛选。
- `go test -race` 流量包及订单分页专项：通过。
- Go vet、前端 typecheck/build：通过，迁移121与API/worker/web已部署，三服务active、PG18 ready。
- Playwright CLI 四尺寸：普通列表真实18条、续费取消筛选真实1条，1440/1024/390/320页面宽度等于scrollWidth、控制台0错误，请求均成功。截图在 `output/playwright/admin-batch-orders-followup-20261011/`。
- 浏览器确认取消后选择取消弹窗，未发送取消写入；续费账单28点击详情并返回 `/admin/orders?status=cancelled&kind=renew&limit=50`，保留状态和50条页容量。
- 流量包实际资源交付/真实收款未验证；未对供应商执行资源写入。
