# 工单通知 UI（2026-10-16）

本批沿用 shadcn UI 与 `/console/account`、`/console/tickets/new`、客户及管理员工单详情、通知模板现有路由。

- 本人偏好使用 session-only `GET/PATCH /me/ticket-notification-preferences`，保存携带 expected_version。取消重新取服务端值；409 保留草稿，显式加载最新版本后再保存。邮件要求已验证邮箱；短信默认关闭且要求已验证手机号。联系人订阅独立。
- 客户创建及客户/客服公开回复携带 idempotency_key。发送前经 usePendingSetting 保存原请求到按 /me 用户 ID、create 或 ticket ID 隔离的 sessionStorage。未知结果（网络/5xx）锁定内容，仅重试原 body/key/version；刷新后可确认原请求。成功及明确 4xx 解除待确认状态。存储禁用时只能在当前页内恢复。不会把新 GET version 替换到原请求。
- 工单详情从真实分页 `/tickets/{id}/notifications?limit=20&offset=...` 读取摘要。queued 明示不等于送达；skipped 展示 API 原因。地址只在 API 返回时显示，不拼接客户或客服地址；权限由 API 判定。手动刷新适用于等待异步 worker。
- 模板类型输入提供三个新 kind 的建议及方向说明，保留管理员代建 ticket_created，不假造模板记录。
- 布局使用原有容器、flex-wrap、sm 断点与 break-all；按钮及提示可在窄屏换行。未增加本人 inbox。

验证边界：本 worktree TypeScript、ESLint 通过（全量 7 条既有 warning，变更文件无 warning）；Turbopack 因跨根 node_modules 符号链接失败，独立 `next build --webpack` 通过。四尺寸真实 API 浏览器流程由主代理集成后验收。未外发邮件/SMS，未重启生产。
