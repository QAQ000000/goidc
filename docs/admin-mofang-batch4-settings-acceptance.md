# 后台深度还原第一批：设置与渠道验收

日期：2026-10-10。

## 本批实现

- 新增 105 页逐页还原台账，记录旧版 hash 路由、新版 `/admin/*` 路由、状态、API、权限和验收范围。
- `site_settings` 新增 `admin_settings` JSONB，补齐常规设置的本地化、显示、安全、维护和 Debug 分区。
- 站点设置继续使用版本号和 `expected_version`，后台偏好不会进入公开 `/api/v1/site` 响应。
- 新增 `/admin/auth-settings` 注册登录与验证码入口，并接入全局后台导航和 `settings.view` 权限。
- 渠道目录增加开通接口分类，继续使用编译进 Go 的注册表和数据库实例配置。

## 验证结果

- `scripts/go.sh test ./...`：通过。
- `scripts/go.sh vet ./...`：通过。
- `npm run typecheck --prefix web`：通过。
- `npm run build --prefix web`：通过，包含 `/admin/auth-settings`。
- `git diff --check`：通过。
- PostgreSQL 18 迁移 `102_site_admin_settings.sql`：已应用。
- API 服务、Web 服务、worker：已重启并为 active。
- `GET /readyz`：返回 `postgresql18 / ready`。
- `GET /api/v1/site`：未暴露 `admin_settings`，公开资料响应保持原边界。

## 未验证项

本轮终端 Playwright 启动 Chromium 时因 root 环境缺少 sandbox 支持而退出，未将浏览器四尺寸结果标记为通过。待使用非 root 浏览器会话后，继续验收 1440、1024、390、320 四种宽度，以及设置切换、保存、取消、版本冲突、权限拒绝和移动端无横向溢出。
