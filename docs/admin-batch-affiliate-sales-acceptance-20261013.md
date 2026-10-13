# 推介规则与销售结算批次验收

日期：2026-10-13。范围是后台第 29 页推介设置的商品/阶梯规则，以及第 48 页销售设置关联的销售结算页面。本批代码包含迁移 134、135，未部署到运行 API/Web，未把迁移应用到运行数据库。

## 交付内容

- 推介商品规则支持 `inherit`、`disabled`、`rate`、`fixed`，可配置佣金上限；月营业额阶梯按上海自然月计算，区间下限含、上限不含。
- 商品规则优先于营业额阶梯，结算时保存规则版本、阶梯版本、销售归属和金额快照。规则保存会产生新版本，历史佣金不重算。
- 销售结算支持预览、确认、详情和分页。只计算已结束月份的新购订单；信用消费全部还清后按最终到账时间计入营业额。
- 结算不打款、不修改客户余额，只保存不可变结算快照。订单取消、退款和发票退款通过数据库触发器原子冲正对应结算条目。
- 正式结算要求销售查看/管理权限和全部客户范围，使用预览摘要、幂等键、审计记录和 409 冲突保护。未知响应会保留原请求，避免重复建账。
- 后台导航、订单页入口、销售设置入口和 `/admin/sales-settlements` 页面已接入真实 API；页面无静态业务数据。

## API 与迁移

- `GET /api/v1/admin/affiliate/rules/schema`
- `GET /api/v1/admin/affiliate/rules`
- `PUT /api/v1/admin/affiliate/rules`
- `POST /api/v1/admin/sales-settlements/preview`
- `POST /api/v1/admin/sales-settlements`
- `GET /api/v1/admin/sales-settlements`
- `GET /api/v1/admin/sales-settlements/{id}`
- `db/migrations/134_affiliate_turnover.sql`
- `db/migrations/135_sales_settlements.sql`

## 自动化检查

| 检查 | 结果 |
| --- | --- |
| `scripts/go.sh test ./... -count=1 -timeout=25m` | PASS；隔离 PG18，包含迁移历史数据回归 |
| `scripts/go.sh vet ./...` | PASS |
| 相关 `-race` 专项 | PASS；包含销售结算并发确认和并发退款 |
| `npm run typecheck --prefix web` | PASS |
| `npm run lint --prefix web` | PASS；0 错误，8 条既有 warning |
| 独立副本 `npm run build -- --webpack` | PASS；副本 `/tmp/zjmfgo-commission-web-20261013` |
| `git diff --check` | PASS |

## 浏览器验收

使用隔离 PG18 fixture 的真实 Go API 和独立 Next 生产预览 `http://localhost:3101`，登录账号为临时验收账号。推介设置和销售结算均检查 1440×900、1024×768、390×844、320×800：`scrollWidth` 与视口一致，退出按钮在视口内，无 JavaScript 错误和失败 API。

已验证的业务状态包括：

- 推介规则添加、保存、取消恢复、默认计划保存后的共享版本刷新；零佣金规则的上限保持；规则 409 冲突时草稿保留。
- 未知保存响应（503）会保留原请求；随后 403 不会丢弃请求和幂等键；刷新页面可以恢复原请求。客户范围变窄时会明确提示恢复全部客户范围后重试。
- 销售结算预览、确认、详情、列表分页和 URL 状态恢复；规则变化后确认返回 409 且输入保留；未知响应和后续 403 保留原结算请求，刷新后只回放一次幂等请求。

截图证据保留在 `output/playwright/affiliate-sales-20261013/`：

- `affiliate-policy-{1440,1024,390,320}.png`
- `affiliate-rules-{1440,1024,390,320}.png`
- `sales-detail-{1440,1024,390,320}.png`
- `rules-conflict.png`、`rules-unknown-403.png`、`sales-preview.png`、`sales-conflict.png`、`sales-unknown-403.png`

服务端 `TestSalesSettlementConcurrentRefund` 使用真实退款/结算并发请求验证最终没有残留 confirmed 条目，结果 PASS。浏览器端本批未形成一个全新、独立的退款展示截图，因此不把浏览器退款标记为 PASS。

## 未收口边界

- 完整 105 页权限矩阵仍需继续按业务域补齐。
- 销售续费、升级等非新购结算仍未纳入本批正式结算。
- 真实生产商户、供应商和正式部署未在本批执行。
- 迁移 134、135 仅在隔离 PG18 数据库验证，尚未应用到运行数据库。
