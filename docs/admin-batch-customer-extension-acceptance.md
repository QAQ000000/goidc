# 客户扩展批次验收

本批覆盖客户等级、客户分组、实名认证审批与实名认证设置四个后台页面，并补齐实名认证筛选、审核历史、幂等审核和客户等级权限契约。页面数据均来自 Go API，未使用静态业务数据。

## 浏览器验收

已登录本地开发实例并使用真实 API 在 1440、1024、390、320 四种宽度检查页面加载、空态、筛选、分页容器、权限控件、错误态和无横向溢出。截图证据位于 `output/playwright/admin-customer-extension-20261011/`：

- `*-customer-levels.png`
- `*-customer-groups.png`
- `*-certifications.png`
- `*-certification-settings.png`
- 客户自定义字段截图：`output/playwright/admin-restore-20261011/customer-fields-{1440,1024,390,320}.png`

商品分组和折扣子页已完成代码验收，但尚未生成专属四尺寸截图。

## 后端验证

- 客户等级真实 PostgreSQL 18 隔离库 CRUD、统计、权限、幂等和版本冲突通过。
- 客户分组与自定义字段分页、筛选、幂等、审计和版本冲突通过。
- 实名认证类型/接口筛选、详情和历史记录权限边界通过。
- 实名审核在事务内写入状态、审计和幂等记录；重复请求可重放，版本冲突返回 `409`。
- `scripts/go.sh test ./...`、`scripts/go.sh vet ./...`、`npm run typecheck --prefix web`、`npm run build --prefix web` 通过。

## 已知未覆盖

- 实名个人转企业流程尚未实现。
- 客户分组的商品分组与折扣子页尚未实现。
- 自定义字段定义尚未接入客户资料与注册表单的值读写。
- 本批未覆盖正式库切换和真实第三方实名调用。
- 商品分组与折扣子页尚未完成 Playwright 四尺寸验收。
