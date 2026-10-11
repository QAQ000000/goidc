# 后台新建工单验收（2026-10-11）

本批补齐旧版第 100 页“新建工单”的 Go/Next.js 真实链路。页面使用现有 `/admin/*` 路由，所有客户、部门、服务和处理人选项均来自 Go API；未使用静态业务数据。

## 已交付

- `/admin/tickets/new`：客户、部门、关联服务和处理人搜索分页，优先级、抄送邮箱、初始邮件通知、附件选择和创建后跳转。
- `GET /api/v1/admin/tickets/create-options`：按管理员客户范围和工单部门范围返回真实选项；处理人必须属于所选部门且具备工单处理权限。
- `POST /api/v1/admin/tickets`：客户、部门、服务归属、处理人、字段长度、优先级和抄送邮箱在事务内重新校验；创建写入审计和幂等回执，客户范围、部门权限和版本/撤权边界由服务端执行。
- 邮件通知根据邮箱验证、启用的邮件渠道和 `ticket_created` 模板返回明确状态，并将实际收件人写入 outbox；未满足条件时不伪造发送成功。
- 创建结果不确定时锁定表单并允许使用同一幂等键重试；附件在工单创建后上传，上传失败时保留工单编号并提示人工复核。标题和正文草稿按管理员会话隔离保存在 `sessionStorage`，取消会确认并丢弃草稿。

## API 契约

| API | 用途 |
|---|---|
| `GET /api/v1/admin/tickets/page` | 工单列表真实分页、部门/客户范围、状态/优先级/关键词筛选 |
| `GET /api/v1/admin/tickets/create-options` | 默认优先级、邮件可用状态；`kind=customers/departments/services/assignees` 返回 `items/total/limit/offset` |
| `POST /api/v1/admin/tickets` | 事务创建工单，使用 `Idempotency-Key`，返回 `email_status`、`version` 和 `replayed` |
| `POST /api/v1/tickets/{id}/attachments` | 创建完成后上传附件，单文件上限 10 MiB |

## 验证

- 隔离 PG18 已验证迁移 125、客户/部门范围、选项分页、服务归属、处理人校验、邮件队列、审计和幂等重复提交；专项 Go 测试通过。
- `scripts/go.sh vet ./...` 通过；全量 Go 回归在本批前后均完成并通过，最终退出码为 0。
- `npm run typecheck --prefix web` 通过；订单创建表单修正 Chrome 新正则语法后重新检查通过。
- 强制 PG18 全量回归 `REQUIRE_INTEGRATION=1 scripts/go.sh test ./... -count=1 -timeout 20m` 全部通过，`internal/httpapi` 用时 591.444 秒；`scripts/go.sh vet ./...` 通过。
- Playwright 使用隔离 PG18 和隔离服务 `127.0.0.1:18088` / `127.0.0.1:13088`，工单列表和新建工单在 1440、1024、390、320 宽度下 `document.scrollWidth` 分别等于视口宽度，无横向溢出；页面创建链路成功跳转工单详情，控制台最终无错误。截图位于 `output/playwright/admin-ticket-create-20261011/`：`tickets-list-{1440,1024,390,320}.png`、`tickets-new-{1440,1024,390,320}.png`。
- 前端 typecheck、0 错误/8 条既有 warning 的 lint，以及独立隔离目录中的 Next 生产构建通过，产物包含 `/admin/tickets/new`。Next CSS 优化器对生成的 `animate-out` 规则另输出 1 条 warning，构建成功。短屏手机测试验证弹窗按钮可在弹窗内部滚动后访问。

## 尚未宣称完成

- 旧版完整字段表单、批量附件管理、附件替换/删除和发送日志详情仍待补齐。
- 本批没有在正式开发库创建业务数据；隔离测试库和临时服务收尾时删除。
- 全部 105 页深度还原、真实外部通知投递、正式切换和回滚演练仍未完成。
