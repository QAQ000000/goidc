# 第 96/97 页「我的应用 / 应用商店」验收

日期：2026-10-13。页面：`/admin/apps`、`/admin/apps/store`；旧版页面：`#/app-store`、`#/app-store/app-list`。使用隔离 PG18 数据库和独立 Next.js 生产构建，未访问生产业务库，也没有在线安装或执行第三方代码。

## 实现范围

- `GET /api/v1/admin/apps` 是唯一数据来源，支持 `view=configured|catalogue`、类别、状态、关键词和分页，返回 `items/total/limit/offset/build`。
- 目录来自编译进 Go 二进制的 `channel` registry；配置聚合只读取 `channel_instances` 与 `supplier_accounts` 的计数，不把 `config`、账户地址、用户名或密钥读入响应。
- `factory_available` 来自实际注册工厂。元数据存在但没有工厂时显示“未注册”，不提供配置 CTA；不把未编译的数据库记录显示成应用。
- 页面仅允许 `super_admin` 访问，提供真实空态、加载态、错误态、权限拒绝、搜索、类别/状态筛选、分页和 URL 状态恢复；配置入口使用白名单 `return_to` 返回应用列表。
- 应用商店没有上传、安装、在线升级或运行时加载动作，符合编译期模块模型。

## 自动化检查

| 检查 | 结果 |
| --- | --- |
| `TestCompiledAppsHTTPContractFilteringAndRedaction` | PASS，隔离 PG18；聚合、筛选、分页和敏感信息脱敏 |
| `TestCompiledAppsSuperAdminPermission` | PASS，未登录、普通管理员、只读管理员和客户均拒绝；写方法不存在 |
| `TestCompiledAppsBuildMetadataAndQueryValidation` | PASS，构建信息缺失保持 `null`，非法查询返回 400 |
| `TestAdminRBACRouteCoverage` | PASS |
| `TestCompiledAppsBrowserFixture` | PASS，隔离 PG18，正常结束 |
| `npm run typecheck --prefix web` | PASS |
| `npm run lint --prefix web` | PASS，0 错误、8 个既有警告 |
| 隔离副本 `npm run build -- --webpack` | PASS |
| `git diff --check` | PASS |

## 浏览器验收

Playwright 使用隔离 Next.js 生产构建、真实 API 夹具和 `compiled-apps-super`。`/admin/apps` 展示真实 SMTP、易支付、模拟支付、魔方财务和通用短信实例聚合；`/admin/apps/store` 展示编译目录，搜索 `SMTP` 后只返回 SMTP，配置入口带回原筛选 URL。两页均检查控制台错误和请求，实际请求均为 200。

“我的应用”四尺寸截图和根节点布局检查：

| 视口 | 结果 | 截图 |
| ---: | --- | --- |
| 1440×900 | PASS，无页面级横向溢出 | `output/playwright/admin-apps-20261013/apps-my-1440.png` |
| 1024×768 | PASS，无页面级横向溢出 | `output/playwright/admin-apps-20261013/apps-my-1024.png` |
| 390×844 | PASS，无页面级横向溢出 | `output/playwright/admin-apps-20261013/apps-my-390.png` |
| 320×800 | PASS，无页面级横向溢出 | `output/playwright/admin-apps-20261013/apps-my-320.png` |

四尺寸截图中卡片内容可换行，320px 下公共后台顶部退出按钮仍有既有裁切；该公共 shell 问题不归入本页实现通过。应用商店页面已在 1440×900 完成视觉和筛选操作验收，窄屏复用同一卡片组件并保留为部分完成登记。

## 边界与清理

本批不宣称任何支付、短信或供应商已经完成真实生产连通性验收；计数只表示数据库配置状态。全量 Go 专项和前端构建结束后已停止隔离服务，删除本批隔离数据库、角色和临时凭据；截图保留在 gitignored `output/playwright`，未改动运行中的 `web/.next` 或共享服务。
