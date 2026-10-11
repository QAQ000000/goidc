# 第六批报表与任务运维验收

日期：2026-10-11

范围：我的业绩、工单统计、定时任务配置、系统日志、日志清理、管理员登录日志、邮件日志、短信日志。

## API 与权限

- 销售报表使用 `GET /admin/reports/sales?year=`，工单统计使用 `GET /admin/reports/tickets?year=&month=`；均执行 `reports.view` 和客户/部门范围过滤，空数据返回稳定结构。
- 定时任务使用 `GET/PUT /admin/scheduler-settings`，要求 `@super`，写入携带 `expected_version` 和 `Idempotency-Key`，审计并返回冲突 `409`。
- 系统日志使用 `GET /admin/logs?kind=system`，要求 `audit.view`，只返回安全元数据；日志清理保留 24 小时窗口，要求 `@super`、确认字段和幂等键。

## 浏览器验收

隔离 PG18 fixture：`TestAdminOperationsBrowserFixture`，账号为测试专用管理员，未使用开发环境管理员凭据。Next 预览通过同源 `/api/*` 代理连接 fixture API。

页面组合：8 个；视口：1440、1024、390、320；共 32 次加载。每次均确认标题存在、API 返回真实空态或数据，且 `document.documentElement.scrollWidth === clientWidth`。结果全部通过，无横向溢出。截图位于 `output/playwright/admin-ops-20261011/`，文件名带 `-verified`。

## 自动化检查

- `npm run typecheck --prefix web`：通过。
- `npm run build --prefix web`：通过。
- `scripts/go.sh vet ./...`：通过。
- 隔离 PG18 全量测试：发现并修正旧管理员迁移测试对后续新增 `customer_levels.*` 权限的断言遗漏；该权限不会被 031 迁移隐式授予。

本批仍未覆盖任务队列统一重试、通知投递结果和其余运维报表，继续列为后续补齐项。
