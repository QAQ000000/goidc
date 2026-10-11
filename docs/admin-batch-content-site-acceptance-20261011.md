# 官网配置与内容域批次验收

日期：2026-10-11。范围：后台第 76-81 页（基础信息沿用站点设置、主题模板、官网自定义字段、导航管理、友情链接、文件分组）。页面使用现有 `/admin/*` 路由，API 均来自 Go `/api/v1/admin/*`，无静态业务数据。

## 实现

- `120_site_content_settings.sql` 新增主题单例、官网字段、官网导航、友情链接表；导航父子关系由触发器阻止循环。
- 主题使用 `GET/PUT /admin/theme-settings`，第 76 页继续使用 `GET/PUT /admin/site-settings`。
- 官网字段使用 `GET/POST/PATCH/DELETE /admin/site-fields`；导航使用 `/admin/navigation`；友情链接使用 `/admin/friendly-links`。
- 第 81 页 `/admin/file-groups` 复用下载中心分类实体和权限，提供分页、筛选、版本保护和审计，不复制文件分类数据。
- 写请求要求 `Idempotency-Key`，版本化更新/删除携带 `expected_version`，冲突返回 409；读写权限分别为 `content.view/content.manage`、`downloads.view/downloads.manage`、`settings.view/settings.manage`。

## 自动化证据

| 检查 | 结果 |
| --- | --- |
| PG18 隔离 `TestSiteContentVersionedCRUDAndPermissions` | PASS |
| 权限路由登记 `TestAdminRoutePermissions` | PASS |
| `scripts/go.sh vet ./internal/httpapi` | PASS |
| `npm run typecheck --prefix web` | PASS |
| `npm run build --prefix web` | PASS |
| 真实部署迁移 120、`/readyz` | PASS |

## 浏览器验收

使用 Playwright 会话 `content-site-acceptance`，管理员真实会话访问主题、官网字段、导航、友情链接和文件分组页面。五个页面均验证真实空态、关键词/状态筛选入口、刷新、新增/编辑权限入口、版本字段展示和移动端菜单；友情链接页在 320px 下 `document.documentElement.scrollWidth=320`，无横向溢出。

四种尺寸截图共 20 张，位于 `output/playwright/admin-batch-content-20261011/`：每页 `1440`、`1024`、`390`、`320` 四个视口。开发数据库只创建了用于验收登录的 `content-browser-admin` 管理员，未写入内容实体。

## 限制

主题配置目前作为独立站点主题契约保存，官网渲染层后续可按模板字段消费；自定义字段定义尚未接入客户注册/资料值表。导航和友情链接目前提供后台维护 API，公共官网消费可在站点导航渲染批次接入。
