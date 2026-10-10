# 设置表单保存契约

`/admin/settings`、`/admin/site-settings` 和 `/admin/auth-settings` 共用站点设置表单与真实 `GET/PUT /api/v1/admin/site-settings` 接口。管理读取要求 `settings.view`，写入要求 `settings.manage`；保留浏览器会话、同源请求和版本检查。

- 切换分区保留当前内存草稿；本批不将设置草稿写入浏览器持久存储。
- 取消修改恢复最近一次成功保存的设置，不回退到初次进入页面时的旧版本。
- 没有修改时禁用保存及取消；写入期间禁止重复提交。
- 重新加载未保存的修改前要求确认。只读用户仍可重新加载。
- HTTP 409 保留未保存修改并提示重新加载最新版本，不静默覆盖其他管理员的修改。

本批仅修复表单生命周期，没有增加设置开关或改变注册、验证码、登录 IP 检查等后端语义。

验证：前端 TypeScript、组件 ESLint、隔离 PostgreSQL 18 的 `TestSiteSettings|Test.*Maintenance` 定向测试通过。浏览器保存后取消、重新加载确认与冲突提示仍未验证；跨页面离开草稿提示与持久草稿恢复未实现。
