# 后台设置与营销推送批次验收

日期：2026-10-11

本批覆盖 `/admin/marketing`、`/admin/currencies`、`/admin/finance-settings`、`/admin/invoice-settings`、`/admin/contract-settings` 和 `/admin/credit-settings`。页面数据全部来自 Go API；营销活动使用真实客户筛选与邮件、短信、站内信 outbox，设置写入使用版本号、幂等键和审计记录。

## 自动化验收

- `REQUIRE_INTEGRATION=1 ZJMFGO_TEST_DATABASE_URL=... scripts/go.sh test ./...`：通过，PG18 隔离 schema，`internal/httpapi` 通过（437.571s）。
- `scripts/go.sh vet ./...`：通过。
- `npm run typecheck --prefix web`：通过。
- `npm run lint --prefix web`：通过，保留既有 6 条 `next/no-img-element` 警告，无错误。
- `npm run build --prefix web`：通过，生成上述 6 个管理路由。

## 浏览器验收

使用独立 PG18 测试库、真实 API 和 Next 代理，以管理员会话访问页面。营销页验证空态、筛选栏、分页；财务页验证 CNY 初始数据、版本字段、保存/取消控件、发票快递空态。

| 页面 | 1440 | 1024 | 390 | 320 | 结果 |
| --- | --- | --- | --- | --- | --- |
| 营销推送 | 通过 | 通过 | 通过 | 通过 | 空态、分页、导航和 API 数据正常 |
| 货币配置 | 通过 | 通过 | 通过 | 通过 | CNY 真实数据、无横向溢出 |
| 充值与财务 | 通过 | 通过 | 通过 | 通过 | 版本和整数分字段正常 |
| 发票设置 | 通过 | 通过 | 通过 | 通过 | 快递空态和添加控件正常 |
| 合同设置 | 通过 | 通过 | 通过 | 通过 | 版本保存控件正常 |
| 信用额设置 | 通过 | 通过 | 通过 | 通过 | 版本保存控件正常 |

四种宽度均满足 `document.documentElement.scrollWidth === clientWidth`，控制台错误数为 0。截图证据保存在 `output/playwright/admin-marketing-1440.png` 和 `output/playwright/admin-marketing-320.png`；其余页面完成同样的尺寸与溢出检查。

## 数据库迁移

- `110_financial_settings.sql`：货币、充值、发票、快递方式、合同及设置幂等记录。
- `111_marketing_campaigns.sql`：营销活动、受众投递记录、操作幂等记录，并放宽邮件 outbox 类型约束以支持营销投递。

105 页台账和页面契约已同步更新；本批页面状态为“部分完成”，仍需后续补齐旧版字段深度和完整危险操作浏览器流程。
