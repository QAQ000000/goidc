# 后台安全设置批次验收（2026-10-11）

本批完成后台第 56 页“登录黑名单”：Go API、PG18 迁移、登录链路阻断、连续失败自动封禁和 Next.js 页面均使用真实数据。范围不包含 105 页整体完成声明。

## 实现

- 迁移 `db/migrations/119_auth_blacklist.sql`：`auth_blacklist` 表、唯一主体约束、版本字段及过期索引。
- API：`GET /api/v1/admin/blacklist`、`POST /api/v1/admin/blacklist`、`PATCH /api/v1/admin/blacklist/{id}`、`DELETE /api/v1/admin/blacklist/{id}`。
- 权限：`security.view` 读取，`security.manage` 添加/编辑/解除；超级管理员自动拥有全部权限。
- 登录保护：认证前匹配用户名/IP；管理员连续三次失败自动封禁 3 小时，写入 `auth.login_blocked` 审计事件。
- 写入保护：幂等键、`expected_version` 冲突返回 `409`、危险解除确认和审计记录。

## 验证结果

- 黑名单专项 PG18 测试：权限拒绝、创建、查询、更新、版本冲突、幂等重放、手动解除、登录阻断、连续三次失败自动封禁均通过。
- `scripts/go.sh vet ./...` 通过。
- `npm run typecheck --prefix web` 通过。
- `npm run build --prefix web` 通过。
- 全量 `source /tmp/zjmfgo-restore-test.env && scripts/go.sh test ./...` 通过。
- 三项服务重启后 `zjmfgo-api`、`zjmfgo-web`、`zjmfgo-worker` active；`/readyz` 返回 PostgreSQL 18 ready。

## 浏览器验收

使用已登录超级管理员会话访问 `http://localhost:3088/admin/blacklist`：

- 空态加载、导航菜单和管理表单可见。
- 添加用户名阻断“blacklist-browser-check”成功，列表显示真实记录。
- 点击“解除”弹出确认；确认后记录删除并恢复真实空态。
- 1440x900、1024x768、390x844、320x800 均无横向溢出（`document.documentElement.scrollWidth === innerWidth`）。
- 截图：
  - `output/playwright/admin-blacklist-20261011/1440x900.png`
  - `output/playwright/admin-blacklist-20261011/1024x768.png`
  - `output/playwright/admin-blacklist-20261011/390x844.png`
  - `output/playwright/admin-blacklist-20261011/320x800.png`

## 边界

本批只新增登录安全黑名单能力；验证码通知、MFA 页面、统一安全事件报表和其他设置页面仍按 105 页台账分批推进。开发库中的浏览器验收记录已清理，未保留测试黑名单主体。
