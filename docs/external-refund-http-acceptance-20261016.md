# 外部退款 HTTP 隔离验收（2026-10-16）

本次只实现普通人工账单全额外部退款登记与独立复核，不代表支付异常闭环全部完成。不执行真实退款或调用商户。

## HTTP 契约

全部路由仅接受有效浏览器会话，拒绝 Bearer API key。独立权限不默认授予既有员工角色：

- `GET /api/v1/admin/invoices/{id}/external-refunds`：`external_refunds.view`，返回 `items/total/limit/offset`。
- `POST` 同路径：`external_refunds.create`。
- `POST /api/v1/admin/invoices/{id}/external-refunds/{refund_id}/confirm` 或 `/reject`：`external_refunds.review`。

写操作首次成功 201、相同输入幂等重放 200；版本或幂等冲突 409；自审 403。会话摘要传给 store，在资金事务内重验会话、权限、范围。

## 已执行的隔离检查

在独立 PostgreSQL 18 `_test` 数据库随机 schema 执行：

- 三组 `TestExternalRefundHTTP*` 全部通过：只读/客户/API key 拒绝、scope 越界、撤权和过期会话重放、自审、409、同键重放/异参冲突、并发不同键申请与确认、驳回、审计写入故障后无部分记录且同键可恢复重试。
- 确认保留原支付成功状态，退款不生成余额流水；原成功通知重放保留 refunded。确认前仅收入、确认后收入与退款支出分别保留，净额归零；客户交易追溯原账单，收入报表原毛收入保持。
- 现有人工账单、RBAC 权限与网关外部退款拒绝回归通过。

## 浏览器 fixture

`TestExternalRefundBrowserFixture` 已验证启动与收尾，尚不等于浏览器页面验收。使用原公共 fixture 的环境变量：`ZJMFGO_BROWSER_READY_FILE`、`ZJMFGO_BROWSER_FINISH_FILE`、可选 `ZJMFGO_BROWSER_ORIGIN`；fixture 必须使用隔离测试数据库。

测试生成器账户 `refund-requester`（登记）、`refund-reviewer`（独立复核）、`refund-reader`（只读）、`refund-denied`（仅账单查看）、`refund-super`（super admin）。登录使用既有测试登录生成器的固定测试密码，不涉及部署账号。

创建五张已付款人工账单：前三张空退款记录、第四张待审核、第五张 26 条真实 API 产生的驳回历史加一条待审核，用于真实分页。发票 ID 由测试日志给出。四尺寸和 UI 行为由主验收另行记录。
