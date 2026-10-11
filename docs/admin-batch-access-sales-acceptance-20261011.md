# 第七批：权限、员工与销售设置验收（2026-10-11）

本批新增销售设置页 `/admin/sales-settings`，覆盖商品提成组、营业额阶梯、销售人员绑定和下单/注册销售分配。页面数据全部来自 Go API；写操作使用 `sales.manage`、版本号、`Idempotency-Key`、事务和审计，读取使用 `sales.view`。

## 自动化与数据库

- PG18 隔离回归：`source /tmp/zjmfgo-restore-test.env && scripts/go.sh test ./...`，通过。
- 销售契约测试：设置权限、幂等重放、409 版本冲突、提成组/阶梯 CRUD、销售人员绑定，通过。
- `scripts/go.sh vet ./...`，通过。
- `npm run typecheck --prefix web`，通过。
- `npm run build --prefix web`，通过。
- 迁移 118 已应用到开发数据库，API、Worker、Web 重启后均为 active，`/readyz` 返回 PostgreSQL 18 ready。

## 浏览器

使用独立 Playwright Chrome 会话登录管理员，访问真实部署的 `/admin/sales-settings`，检查四个标签、真实空态、设置表单、导航高亮及 API 加载。四种视口均无页面横向溢出：

| 视口 | 结果 | 截图 |
|---:|---|---|
| 1440×900 | PASS | `output/playwright/admin-sales-20261011/sales-1440.png` |
| 1024×768 | PASS | `output/playwright/admin-sales-20261011/sales-1024.png` |
| 390×844 | PASS | `output/playwright/admin-sales-20261011/sales-390.png` |
| 320×800 | PASS，`document.documentElement.scrollWidth === 320` | `output/playwright/admin-sales-20261011/sales-320.png` |

删除和保存确认、版本冲突、幂等重放由隔离 HTTP 契约测试覆盖；真实开发库没有写入销售组或阶梯测试数据。
