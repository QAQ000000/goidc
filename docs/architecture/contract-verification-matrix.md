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
| 订单与支付 | 金额是否以整数分计算？重复键是否重放同一结果？外部未知是否停止重发？ | `./scripts/go.sh test ./...`；独立 PG18 使用 `./scripts/test-all.sh`；[支付契约](../payments.md) | 模拟支付只能证明适配器和状态处理，不能证明真实渠道可用 |
| 余额与发票 | 借贷、冻结、退款、发票版本和审计是否同事务提交？ | `internal/store` 事务/幂等测试；[交易契约](../transactions.md)；[发票](../invoice-applications.md) | 无独立 `_test` 数据库不得声称数据库集成通过 |
| 服务生命周期 | 交付、续费、变更、取消和失败恢复是否保持状态机约束？ | Go 业务回归、worker 恢复脚本、[服务契约](../service-configuration-changes.md) | worker 启动或编译成功不代表恢复和外部交付通过 |
| RBAC 与后台 | API 与页面是否执行同一角色、资源范围和撤权规则？ | API 权限测试、`node scripts/admin-page-contracts.mjs`、[RBAC](../admin-rbac.md) | 静态登记不等于运行时强制；浏览器验收需单独记录 |
| 供应商与 NOKVM | 超时、重试、凭据、未知结果和人工复核是否安全？ | 适配器测试、隔离运行、[供应商协议](../supplier-protocol.md)、[NOKVM](../nokvm-delivery.md) | 模拟供应商不证明真实商户、配额、网络或生产权限 |
| 迁移与切换 | 旧数据映射、迁移 checksum、冻结、回退和切回是否可重复？ | 独立 PG 演练、`cmd/migrate`、[迁移入口](../migration/README.md) | 静态盘点和生成报告只能是 E0 |
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
