# 第 95 页「API 设置」批次验收

日期：2026-10-13。页面：`/admin/suppliers/api-settings`；旧版页面：`#/api-setup`。本批使用隔离 PG18 schema、独立 Next 预览和本地供应商协议夹具，未访问生产业务数据库，也未执行上游开通动作。

## 实现范围

- `133_resource_api_settings.sql` 增加资源 API 单例设置，保存 `enabled`、实名认证门禁、已验证手机号门禁、版本和更新时间。
- `GET/PUT /api/v1/admin/supplier-api-settings` 返回真实设置、编译供应商目录和供应商账户脱敏摘要；写请求校验 `products.manage`、版本、幂等键，记录审计和设置操作回执。
- 客户 API 密钥签发及已有 Bearer 调用读取同一资源 API 开关；实名和手机号条件在密钥签发事务内校验。
- `POST /api/v1/admin/suppliers/{id}/test-connection` 只读取上游目录。会话和权限在外部请求前复核，外部请求期间不持有数据库事务；结果记录成功/失败审计并支持幂等重放。
- 页面展示编译进 Go 二进制的魔方财务描述、字段 schema、能力集合、账户凭据是否已配置、本地商品数和只读连接测试；密钥不回显。

## 自动化检查

| 检查 | 结果 |
| --- | --- |
| `TestSupplierAPISettingsSchemaPermissionsAndConnectionProbe` | PASS |
| `TestResourceAPISettingsVersionIdempotencyAndBearerGate` | PASS |
| `TestResourceAPIKeyEligibilityGates` | PASS |
| `TestResourceAPISettingsBrowserFixture` | PASS（隔离 PG18，正常收尾） |
| 供应商连接测试真实协议夹具 | PASS，目录 1 条，HTTP 200 |
| `scripts/go.sh test ./... -p 1 -count=1 -timeout 20m` | PASS，`REQUIRE_INTEGRATION=1`，隔离 PG18；HTTP 包 734.465 秒 |
| `scripts/go.sh vet ./...` | PASS |
| `npm run typecheck --prefix web` | PASS |
| `npm run lint --prefix web` | PASS，0 错误、8 个既有警告 |
| 隔离前端副本 `npm run build -- --webpack` | PASS，副本 `src` 与仓库 `web/src` 一致 |

专项覆盖：管理员/只读管理员/客户权限边界、密钥脱敏、版本冲突、幂等回执、审计、总开关拒绝已有 Bearer、实名/手机号门禁。连接请求有 25 秒超时，但本批未增加超时场景的专项回归。

默认 Turbopack 构建在隔离副本中因 `node_modules` 符号链接指向构建根目录外失败，改用 Webpack 生产构建通过；不将默认构建登记为通过。本批未覆盖部署与运行环境升级。

## 浏览器验收

Playwright 使用系统 Chrome（`chromiumSandbox:false`）、隔离 Next 预览 `http://127.0.0.1:3099` 和真实 API 夹具。页面登录后加载真实设置和供应商账户，点击「测试连接」显示「连接成功」，目录 1 个；勾选手机号条件后保存，版本从 1 到 2；勾选实名认证后点击「取消更改」，确认弹窗关闭并恢复服务器值。

| 视口 | 页面级横向溢出 | 控制台/API | 截图 |
| ---: | --- | --- | --- |
| 1440×900 | PASS，`scrollWidth=1440` | PASS，无错误 | `output/playwright/admin-supplier-api-settings-20261012/api-settings-1440.png` |
| 1024×768 | PASS，`scrollWidth=1024` | PASS，无错误 | `output/playwright/admin-supplier-api-settings-20261012/api-settings-1024.png` |
| 390×844 | PASS，`scrollWidth=390` | PASS，无错误 | `output/playwright/admin-supplier-api-settings-20261012/api-settings-390.png` |
| 320×800 | PASS，`scrollWidth=320` | PASS，无错误 | `output/playwright/admin-supplier-api-settings-20261012/api-settings-320.png` |

表格在窄屏保持容器内横向滚动，页面根节点没有横向溢出。目视抽查 1440 和 320 截图；320 宽度的公共后台顶部退出按钮仍存在裁切，本批不将公共布局完整可用性登记为通过。截图为本机 gitignored 验收产物，不加入代码提交。

## 边界

本批不宣称真实魔方财务账户已验收；真实供应商账户的凭据和目录导入、续费、升级、暂停/恢复、终止等能力继续按供应商能力批次单独测试。加载、空态、错误恢复、只读权限、409 草稿恢复、未知请求重放和关闭确认未逐项完成浏览器验收，因此第 95 页登记为部分完成。`/admin/apps` 与 `/admin/apps/store` 仍是下一批编译模块目录页面。

全量测试后已删除本批隔离数据库及角色、临时凭据、三份隔离前端构建副本，保留截图和验收日志；未改动运行中的 `web/.next` 或共享服务。迁移 133 尚未应用到运行环境。
