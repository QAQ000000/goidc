# API 日志导航权限一致性验收（2026-10-16）

基线：`f651dfa`。范围为 API 日志菜单、页面门禁、共享日志页签的权限一致性，以及专用验证。后台 `GET /api/v1/admin/logs/api` 继续要求 `audit.view`。

## 修复行为

| 身份 | API 日志菜单与页签 | 直接打开 API 日志页 | API 日志接口 | 短信、站内信、邮件日志 |
| --- | --- | --- | --- | --- |
| 仅 `audit.view` 的管理员 | 显示 | 允许 | 200 | 菜单及页签隐藏，接口 403 |
| 仅 `notifications.view` 的管理员 | 隐藏 | 重定向至其首个授权入口 | 403 | 显示，接口 200 |
| `super_admin` | 显示 | 允许 | 200 | 显示，接口 200 |

API 日志页面在服务器端使用 `requireUser(true)` 与 `can(user, "audit.view")` 检查。共享日志组件的页签从现有导航项取得权限，按当前管理员身份过滤；当前日志失去权限时显示拒绝信息，并停止发起该日志的数据请求。此行为也覆盖打开页面后管理员权限刷新的情况。

## 自动验证

| 检查 | 结果 | 边界与证据 |
| --- | --- | --- |
| `node --test scripts/api-log-navigation.test.mjs` | PASS（11 项） | 执行真实导航模块、真实 API 页面函数、真实日志组件渲染；仅替换框架 I/O、查询初始化状态及显示组件。比对四个日志菜单权限与 Go 路由权限，覆盖 audit-only、notification-only、super、无权限管理员、带相关权限字符串的客户；验证重定向、页签及未授权请求抑制。 |
| `cd web && npm run typecheck` | PASS | 使用主项目现有依赖的只读用途软链接，无依赖安装。 |
| `cd web && npx eslint src/lib/admin-navigation.ts src/app/admin/logs/api/page.tsx src/components/admin-notification-logs.tsx` | PASS | 本次改动的三个前端文件。 |
| `git diff --check` | PASS | 无空白错误。 |
| `REQUIRE_INTEGRATION=1 ./scripts/go.sh test ./internal/httpapi -run '^TestAPILogNavigationHTTPPermissionBoundary$' -count=1 -v` | PASS（三身份子测试） | 使用主代理新建的专用 PostgreSQL 18 测试角色及 `_test` 数据库；真实 HTTP 验证 API 200/403、1 行专用样本及其余三日志反向权限边界，测试结束清理独立 schema。 |
| 完整生产构建 | 未验证 | 由主代理在隔离环境统一运行。 |

`TestAPILogNavigationHTTPPermissionBoundary` 使用既有 `testServer` 为 `_test` 数据库创建独立 schema，建立权限互斥账号并写入专用日志样本。样本指定管理员 actor，遵循既有 API 日志查询只返回管理员请求记录的规则。验证三身份的真实 HTTP 200/403，以及成功响应确实包含筛选后的日志行；同时验证其他三个日志接口的反向权限边界。运行方式：

```bash
REQUIRE_INTEGRATION=1 ./scripts/go.sh test ./internal/httpapi -run '^TestAPILogNavigationHTTPPermissionBoundary$' -count=1 -v
```

调用方须先配置隔离 PostgreSQL 18 的 `ZJMFGO_TEST_DATABASE_URL`，测试框架拒绝数据库名不以 `_test` 结尾的目标。本修复没有读取部署配置、写入业务库、部署或调用外部服务。

## 真实浏览器验收准备

独立 fixture：`internal/httpapi/api_log_navigation_test.go` 的 `TestAPILogNavigationBrowserFixture`，复用公共 fixture 启停机制，未修改公共 fixture 文件。

- `api-log-audit`：仅 `audit.view`。
- `api-log-notifications`：仅 `notifications.view`。
- `api-log-super`：`super_admin`。

账号仅在隔离测试 schema 中存在；凭据使用现有测试用户生成器。设置全新 `ZJMFGO_BROWSER_READY_FILE`、`ZJMFGO_BROWSER_FINISH_FILE`、前端 `ZJMFGO_BROWSER_ORIGIN` 和隔离测试数据库后，运行：

```bash
REQUIRE_INTEGRATION=1 ./scripts/go.sh test ./internal/httpapi -run '^TestAPILogNavigationBrowserFixture$' -count=1 -v -timeout=30m
```

ready 文件包含真实测试 API origin 和 schema。前端通过 `API_INTERNAL_URL` 连接该 origin。验收结束创建 finish 文件，由公共 fixture 关闭服务并清理 schema。

| 浏览器检查 | 1440×900 | 1024×768 | 390×844 | 320×800 |
| --- | --- | --- | --- | --- |
| audit-only 菜单、页签、直接访问、数据 | 未验证 | 未验证 | 未验证 | 未验证 |
| notification-only 菜单及页签隐藏、直接访问重定向、API 403 | 未验证 | 未验证 | 未验证 | 未验证 |
| super 菜单、全部日志页签及数据 | 未验证 | 未验证 | 未验证 | 未验证 |

真实浏览器、响应式交互与完整生产构建由主代理继续验收；自动模块测试不替代这些检查。
