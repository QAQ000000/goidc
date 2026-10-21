# 后台页面复用

- 修改后台页面先读 [开发与复用规范](../docs/admin/frontend-development.md) 及对应页面/API契约。
- 新常规列表优先组合 `AdminListPage`、`AdminListRegion`、`ResourceTable` 和 `AdminPagination`；扩展现有公共组件，避免另一套近似表格和状态框架。特殊布局及底层UI原语允许领域渲染。
- URL白名单、权限/身份隔离、服务端分页、响应校验和业务状态文案属于领域；公共展示组件不猜API或实现通用CRUD。
- 表单局部state可以保留；版本、幂等键、即时身份核验、凭据不落存储及未知结果恢复继续复用领域mutation。Toast不能替代持续错误与恢复入口。
- 新业务样式使用语义颜色令牌；组件和浏览器证据分别记录，不以typecheck代替交互验收。

<!-- BEGIN:nextjs-agent-rules -->

# This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` (resolved from this file's directory; in monorepos the `next` package may not be visible from the repo root) before writing any code. Heed deprecation notices.

This block is written and re-added by `next dev` — verify at `node_modules/next/dist/server/lib/generate-agent-files.js`. Removing it from a diff only re-creates the uncommitted change; committing it with your work keeps the tree clean.

<!-- END:nextjs-agent-rules -->
