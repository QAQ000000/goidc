# 逾期暂停候选推进契约（2026-10-16）

## 状态与目标

状态：**实现并待主分支复核**。本契约对应差距审计 G05，范围仅为逾期账单暂停候选的扫描推进、能力门禁、事务一致性和隔离回归；不为任何供应商适配器增加 `suspend` 能力，也不宣称真实云主机已经暂停。

目标是让不可执行的候选完成一次可追溯的处理后退出当前扫描窗口。当前批次有 100 个仍不支持暂停的 supplier 服务时，第 101 个支持暂停的服务必须在后续扫描中入队，不能因为前 100 个长期存在而永久饿死。

## 数据与行为契约

- 候选必须同时满足：服务为 `active`、服务和账单未标记导入复核、账单 `unpaid` 且逾期超过 24 小时、账单与服务存在订单或允许的账单项关联，并且没有运行中的服务交付任务或相同幂等键任务。
- 每次扫描最多读取调用者给定的 `limit`（非法值归一为 100，最大 500）。结果按 `invoice.id, service.id` 稳定排序。批次完成后由下一次扫描继续推进，不依赖内存游标或 offset；这避免候选在并发写入下发生 offset 漂移。
- 当前交付模式能力规则与人工服务动作共用。supplier 不支持 `suspend` 时不创建必然失败的任务，而是在同一事务写入 `service.overdue_suspension.unsupported` 审计，保留服务 `active` 状态。
- 已存在且仍匹配当前不支持模式的审计组合（服务、账单、当前 `provision_mode`）会在 `LIMIT` 前排除。模式改为支持模式、模式切换或新增能力后，旧审计不再阻止重新评估。
- 审计去重键的语义为服务、账单、当前交付模式；重复运行不增加审计。支持模式使用固定 `overdue-suspend-service-{service_id}-invoice-{invoice_id}` 幂等键，重复运行不重复入队。
- 事务先锁候选账单（按查询顺序），再按服务 ID 顺序锁服务，随后复核账单状态、服务能力和忙碌任务。付款与调度竞争时，付款优先改变账单状态，调度不会为已付款账单入队。外部供应商调用仍在任务处理阶段，不在此事务内。
- 任一候选处理失败，整批事务回滚：已写的 unsupported 审计和已插入的暂停任务都不得残留，下一次扫描可以完整重试。系统审计使用 actor `0`，不授予普通管理员绕过能力门禁的权限。

## 相关实现

- 候选扫描与入队：`internal/store/notification_scheduler.go` 的 `EnqueueOverdueServiceSuspensions`。
- 能力规则：`internal/store/services.go` 的 `modeSupportsServiceAction` 与 `unsupportedServiceActionModes`。
- 任务处理及付款后取消：同一文件中的 `lockOverdueSuspensionInvoice`、`cancelObsoleteOverdueSuspension`。
- 人工接口继续使用同一能力门禁；supplier 的人工 `suspend/resume/terminate` 被拒绝，调度器不得绕过该权限边界。

## 验收条件

1. 100 个 supplier unsupported 候选后存在支持模式候选：第一轮只写 100 条唯一审计，第二轮为后续支持项入队，第三轮不重复；supplier 保持 active 且无 suspend 任务。
2. unsupported 模式切换为 manual/simulation 后可重新入选；其他模式的历史审计不能屏蔽当前模式的判断。
3. 多账单、多服务交叉关联和并发扫描下，审计按服务/账单/模式唯一，支持服务至多一个任务，且无死锁或重复资金/资源动作。
4. 故意让后续任务插入失败时，前一候选的审计和任务全部回滚；解除故障后重试可完整应用。
5. supplier 人工动作与调度动作均受能力门禁约束，不因调度身份而创建不可执行任务。
6. `scripts/go.sh build ./...`、`scripts/go.sh vet ./...`、相关 `go test -race` 及强制隔离 PG18 测试通过。此契约不包含浏览器页面或真实供应商验收；那些边界仍按阶段一和 G07 文档单列。

## 当前边界

本契约只证明数据库候选推进和任务入队。任务是否能在真实供应商执行暂停、供应商返回未知结果时的人工核验、生产规模容量和正式切换，均不能由本批测试推断。测试使用隔离 PG18 schema，不读取或写入业务数据库。
