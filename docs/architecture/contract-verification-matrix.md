# 领域验收矩阵

本文是[工程治理契约](engineering-governance.md)的可执行索引。它规定每个高风险领域的最小行为问题、测试入口和证据边界；具体断言仍放在代码测试和领域契约中。

## 使用方式

1. 变更前选择受影响的领域行，补齐规则、非目标和回滚边界。
2. 变更中让测试覆盖该行的行为问题；需要外部系统时明确隔离测试和真实链路的差别。
3. 评审时填写实际命令、测试库、账户、尺寸和提交，不能用历史截图或生成物代替当前证据。
4. 发布时只提升到实际达到的证据等级，未执行的前置条件保持“未验证”。

## 最小矩阵

| 领域 | 必须回答的问题 | 测试/审计入口 | 证据边界 |
| --- | --- | --- | --- |
| 编译模块与配置 | 二进制实际模块是否匹配声明且不读配置或联网？配置/凭据是否按 schema 校验并隔离？缺失实现能否安全停用？新增同类适配器能否复用运行链路？ | [模块契约与测试](../compiled-module-architecture.md#验证入口与发布边界)、`scripts/check-module-binaries.mjs`、`scripts/test-compiled-notifications.mjs` 的 unit/integration 严格入口、渠道组件测试 | 必需入口缺失或跳过须失败；清单不证明外部健康，组件事件不代表浏览器；新支付协议仍需资金全链路验收 |
| 订单与支付 | 金额是否以整数分计算？重复键是否重放同一结果？外部未知是否停止重发？ | `./scripts/go.sh test ./...`；独立 PG18 使用 `./scripts/test-all.sh`；[支付契约](../payments.md) | 模拟支付只能证明适配器和状态处理，不能证明真实渠道可用 |
| 余额与发票 | 借贷、冻结、退款、发票版本和审计是否同事务提交？ | `internal/store` 事务/幂等测试；[交易契约](../transactions.md)；[发票](../invoice-applications.md) | 无独立 `_test` 数据库不得声称数据库集成通过 |
| 服务生命周期 | 交付、续费、变更、取消和失败恢复是否保持状态机约束？ | Go 业务回归、worker 恢复脚本、[服务契约](../service-configuration-changes.md) | worker 启动或编译成功不代表恢复和外部交付通过 |
| RBAC 与后台 | API 与页面是否执行同一角色、资源范围和撤权规则？ | API 权限测试、`node scripts/admin-page-contracts.mjs`、[RBAC](../admin-rbac.md) | 静态登记不等于运行时强制；浏览器验收需单独记录 |
| 经营报表与评分 | 折扣分摊/期间去重是否守恒？预计与确认是否分离？评分是否按原身份一次写入？真实现金与费用是否可追溯且不越权？ | [报表口径与测试入口](../credit-cash-reporting.md#回归入口)、[恢复契约](../report-restoration-contract.md)、`reports_regression_test.go`、`sales_report_details_test.go`、`ticket_ratings_test.go`、`annual_report_test.go`、`report_expenses_test.go`、`product_report_details_test.go` | 必须强制独立PG18；导入夹具不代表真实旧全量迁移，四尺寸浏览器与外部现金凭证另验 |
| 供应商与 NOKVM | 超时、重试、凭据、未知结果和人工复核是否安全？ | 适配器测试、隔离运行、[供应商协议](../supplier-protocol.md)、[NOKVM](../nokvm-delivery.md) | 模拟供应商不证明真实商户、配额、网络或生产权限 |
| 统一通知 | 渠道是否独立于事件？仅绑定 Telegram 能否收到实际业务转换和提醒？扩大付款订阅是否须主动确认？付款/续费后旧提醒是否失效？退订/解绑是否阻止旧任务，未知结果是否停止重发？ | [通知契约](../notification-architecture.md)、PG18 严格 integration 门禁、`TestNotificationLifecycle*`、`TestLifecycleBusiness*`、`TestNotificationCenterProcessRecovery`、通知组件测试 | SIGKILL 是生产循环的测试子进程，HTTP 为本地模拟器、租约 SQL 加速；供应商与 NOKVM 测试边界不能替代真实交付，真实回调及客户接收另验 |
| 迁移与切换 | 旧数据、密文、队列及 checksum 是否保留？旧 schema 是否被当前候选拒绝？既有付款订阅是否保持原范围？冻结、回退和切回是否可重复？ | 独立 PG 的 `TestNotificationCenterUpgradeFrom160`、`TestNotificationLifecycleUpgradeFrom162`；CI 提供实际双二进制强制启动门禁；`cmd/migrate`、[迁移入口](../migration/README.md) | 合成旧夹具和正向升级不证明旧二进制降级或生产备份恢复；静态盘点只能是 E0 |
| 前端与 API | 加载、空、错误、冲突、响应式和错误码是否可见且一致？ | `npm --prefix web run lint`、`typecheck`、`test:proxy`、组件测试 | lint/typecheck 不等于浏览器或真实 API 验收 |
| worker 与容量 | 强杀、重启、队列、锁、延迟和资源预算是否有边界？ | `./scripts/test-worker-recovery.sh`、[容量验证](../capacity.md) | 只读负载不能证明支付、写入或生产容量 |

## 证据记录模板

将记录写入发布或审计制品，而不是活动文档目录：

```text
commit: <40 位提交>
scope: <领域和场景>
level: E0 | E1 | E2 | E3 | E4
command: <实际执行命令>
environment: <隔离库、账户、浏览器尺寸或外部依赖>
result: PASS | FAIL | SKIPPED | UNKNOWN
artifacts: <output/ 或外部制品路径>
rollback: <失败时的回退步骤>
```

`SKIPPED`、`UNKNOWN` 和缺少外部条件都不能折算为 `PASS`。原始 JSON、截图、日志和压缩包应留在 `output/` 或外部只读制品存储，并在摘要中记录来源和校验值。
