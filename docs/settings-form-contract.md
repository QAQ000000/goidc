# 设置表单保存契约

信用和合同设置已随模块整体退役：专属API鉴权后410、页面404，通用站点设置不再提供前台信用显示开关。迁移184只关闭旧开关，保留结构不表示可恢复功能；当前规则见[信用退役](customer-credit-portal-contract.md)及[合同退役](domain/contract-settings.md)。
`/admin/settings`、`/admin/site-settings` 和 `/admin/auth-settings` 共用站点设置表单与真实 `GET/PUT /api/v1/admin/site-settings` 接口。管理读取要求 `settings.view`，写入要求 `settings.manage`；保留浏览器会话、同源请求和版本检查。

- 切换分区保留当前内存草稿；会话草稿恢复行为见下文。
- 取消修改恢复最近一次成功保存的设置，不回退到初次进入页面时的旧版本。
- 没有修改时禁用保存及取消；写入期间禁止重复提交。
- 重新加载未保存的修改前要求确认。只读用户仍可重新加载。
- HTTP 409 保留未保存修改并提示重新加载最新版本，不静默覆盖其他管理员的修改。

本批仅修复表单生命周期，没有增加设置开关或改变注册、验证码、登录 IP 检查等后端语义。

验证：前端 TypeScript、组件 ESLint、隔离 PostgreSQL 18 的 `TestSiteSettings|Test.*Maintenance` 定向测试通过。浏览器保存后取消、重新加载确认与冲突提示仍未验证；草稿和离开提醒已实现，浏览器交互尚未验收。

普通非密钥设置草稿保存到 sessionStorage，键绑定当前管理员 ID，并记录 baseVersion。恢复/丢弃草稿有明确入口，旧版本草稿须显式确认；保存和取消清除草稿。刷新/关闭有 beforeunload 提醒，普通内部链接离开有确认。重新加载直接 GET 最新设置并同时替换表单及 baseline；服务端版本号不变也会重置当前修改。恢复草稿只接受当前 SiteSettings 已有字段，不保存 SMTP、短信或支付密钥。浏览器后退等路由切换拦截仍未验证。
