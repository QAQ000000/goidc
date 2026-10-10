# 后台深度还原第二批：客户资源池

日期：2026-10-10。

## 本批实现

- 新增 `/admin/customer-resources` 页面，沿用旧版资源池的客户、收入、余额、创建时间和销售分配布局。
- 数据来自 `/api/v1/admin/customers/page?sales_owner_id=none`，不注入静态客户数据。
- 销售选择来自 `/api/v1/admin/sales-owners`，分配使用既有 `PUT /api/v1/admin/customers/{id}/sales-owner`。
- 分配请求携带 `expected_version`，后端继续执行销售范围、管理员状态、版本冲突和审计校验。
- 页面加入客户域后台导航和分页、搜索、空态、错误态、刷新状态。

## 验证结果

- `npm run typecheck --prefix web`：通过。
- `npm run build --prefix web`：通过，包含 `/admin/customer-resources`。
- `git diff --check`：通过。
- Web 服务已重启并为 active。

浏览器四尺寸验收仍待非 root Chromium 会话；当前终端 Chromium 因 sandbox 环境限制无法启动。
