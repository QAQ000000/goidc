# 客户扩展与工单设置批次验收

本批实现客户自定义字段、工单状态和工单传递规则的真实 API 与后台入口。数据库迁移为 `106_customer_custom_fields.sql` 和 `107_ticket_status_rules.sql`，均在隔离 PostgreSQL 18 `_test` 数据库中通过重复迁移检查。

## 已验证

- `TestAdminCustomerCustomFieldsVersionedCRUDAndPermission`：客户自定义字段分页、类型/正则校验、创建、编辑、删除、权限拒绝和 409 版本冲突。
- `TestAdminTicketRulesVersionedIdempotentCRUD`：默认状态读取、自定义状态创建、重复幂等重放、幂等键请求冲突、状态版本冲突、规则创建/编辑/删除、重复删除重放和审计记录。
- 所有工单写接口在事务内完成版本锁定、审计和幂等操作记录；关联部门/产品不存在时拒绝写入。

## 浏览器验收

已登录本地开发实例 `http://localhost:3088`，使用真实 API 检查页面加载、空态、分页、权限控件、无 JavaScript 异常和无 API 失败；1440、1024、390、320 四种尺寸均通过，截图保存在 `output/playwright/admin-restore-20261011/`：

- `ticket-statuses-{1440,1024,390,320}.png`
- `ticket-rules-{1440,1024,390,320}.png`
- `customer-fields-{1440,1024,390,320}.png`

## 尚未宣称完成

- 工单状态/传递规则页面目前覆盖真实列表、空态、错误态和创建入口，完整旧版字段表单、编辑弹窗、筛选控件和四尺寸浏览器证据仍待补齐。
- 客户自定义字段定义尚未接入客户资料与注册表单的值读写，因此台账保持“部分完成”。
- 本批不包含正式库切换、真实通知/供应商调用或旧 PHP 路由兼容。

## 命令

```sh
REQUIRE_INTEGRATION=1 ZJMFGO_TEST_DATABASE_URL=postgres://.../_test?sslmode=disable \
  scripts/go.sh test ./internal/httpapi -run 'TestAdmin(CustomerCustomFieldsVersionedCRUDAndPermission|TicketRulesVersionedIdempotentCRUD)$' -count=1
```
