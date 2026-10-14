# 2026-10-14 数据迁移问题决定与切换演练

## 结论

10-09 预演的 245 条问题已按业务类别作出处置决定，但这些决定是迁移策略，不能把缺失或矛盾数据伪装成已核实数据。当前源库在 10-14 的只读快照已发生漂移：源记录从 12 张账单、11 个订单、1 个服务变为 13 张账单、12 个订单、2 个服务，问题从 245 条变为 247 条。新增 1 条待付订单/已取消账单、1 条历史账单和 1 条上游资源核验问题。正式切换必须以新快照重新签字。

当前决定仍保持 `import_review_required` 约束；没有向开发库、正式 PG、旧 PHP 或源 MySQL 写入业务数据。所有“丢弃”都只表示不进入可付款、可退款、可开通的业务表面，源行和审计归档必须保留。

## 逐类业务决定

| 问题 | 10-09 数量 | 10-14 数量 | 决定 | 执行边界 |
| --- | ---: | ---: | --- | --- |
| `missing_host_record` | 206 | 206 | 丢弃运营关联，保留归档 | 不合成服务、主机或资源标识；账单项目保留在历史归档，关联账单继续待复核，不可付款、退款或触发交付 |
| `missing_invoice` | 5 | 5 | 合成取消/待复核账单 | 使用负旧 ID 的合成账单承接原项目和客户归属；状态为取消/待复核，不产生应收、余额、支付意图或开通任务。现有 importer 已采用该边界 |
| `pending_order_cancelled_invoice` | 10 | 11 | 丢弃待付款资格 | 订单与账单均保留原状态和源映射，禁止重新生成待支付入口、支付意图或供应商任务；不会把取消账单恢复为 unpaid |
| `invoice_item_total_mismatch` | 5 | 5 | 人工修正 | 不自动选择账单总额或项目合计；业务负责人必须提供支付/退款/折扣凭证，修正后保留 before/after、凭证和操作者。无凭证则归档并持续锁定 |
| `order_invoice_amount_mismatch` | 4 | 4 | 人工修正 | 不以“差 1 元”推断正确值；订单、账单及项目金额必须由原始收款或退款凭证共同确认。未确认前禁止支付、退款、佣金结算和资源交付 |
| `historical_invoice_requires_review` | 12 | 13 | 丢弃运营动作，保留历史账单 | 保留原状态、金额、项目和源摘要；不重发催缴、不创建支付意图、不允许退款或余额抵扣。需要财务使用时仅能通过复核记录读取 |
| `standalone_invoice` | 1 | 1 | 丢弃可付款资格，保留归档 | 没有订单来源，不能合成为有效订单或推断客户应收；保留账单和项目归档，状态取消/待复核 |
| `active_host_without_paid_invoice` | 1 | 0 | 人工修正 | 10-14 新快照未再出现该 issue；10-09 的服务仍按先核对收款、服务归属和到期日处理，未提供证据前保持 `import_review_required`，不自动激活、不续费、不暂停、不终止 |
| `upstream_resource_requires_verification` | 1 | 2 | 人工修正 | 只能用供应商查单/资源详情或业务凭据确认真实资源；不得猜测 `upstream_id`。无法确认时保持 manual + 待复核，禁止 worker 动作 |

问题类别按 issue 计数；10-09 合计 `206+5+10+5+4+12+1+1+1=245`，10-14 当前快照合计 `206+5+11+5+4+13+1+0+2=247`。`historical_invoice_requires_review` 与 `pending_order_cancelled_invoice` 是独立约束，当前报告 issue 列表以 247 条为准。

## 10-14 隔离切换演练

证据目录：`output/cutover-rehearsal-20261014/`，不进入 Git。

### 源 MySQL

- 使用旧库只读账户和 `--single-transaction --no-tablespaces` 生成一致性快照 `mysql-idc.sql.gz`，SHA-256 记录在 `mysql.sha256`。源库未执行全局只读开关，因为该账户没有 `SUPER/REPLICATION CLIENT`，且生产旧 PHP 正在使用；`SET SESSION TRANSACTION READ ONLY` 已验证，源全局 `read_only=0` 作为阻断项留档。
- 将快照恢复到临时 MySQL 5.7 实例，执行 `SET GLOBAL read_only=ON`；`@@global.read_only=1`，尝试写入返回 `ERROR 1290`。后续导入只连接这个只读临时实例。
- `SHOW MASTER STATUS` 对源账户因权限不足不可读取，因此没有伪造 binlog/GTID；正式切换必须补齐复制位置权限或提供 DBA 导出的 GTID/binlog 证据。

### PG 冷启动、导入和恢复

- 临时 PG18 数据库完成全部迁移至 `137_sms_delivery_evidence.sql`，当前源码 dry-run 报告 247 条 issue，apply 成功并写入 18 张账单（含 5 张合成孤儿账单）、2 个服务，未生成 provision job。
- PG custom dump `pg-cutover.dump` 已恢复到独立 `cutover_restore_test`；schema 版本、用户、商品、订单、账单、服务、归档记录和 ID 映射摘要一致。
- 只读快照和 PG 恢复均未接触开发库或生产库；运行中的 `zjmfgo-api/worker/web` 未停止。

### 增量导入闸门

- 临时源实例解除只读后仅加入测试商品 `products/999999`，再恢复只读；向同一已导入 PG 运行当前 `legacy-import --mode apply`，明确失败：`source changed: products/999999 added; incremental migration requires review`，目标商品数保持不变，失败运行被记录。
- 这不是增量导入成功。当前代码没有可执行的增量导入工具，不能用全量重跑冒充增量。正式切换必须先实现带 binlog/GTID 或冻结快照边界、增量映射、冲突处理和幂等回放的专用工具；在此之前切换闸门保持关闭。

### Go/旧 PHP 切回

- 临时 API 使用恢复 PG 启动，`/healthz` 和 `/readyz` 均 200；临时旧 PHP 7.2 使用原副本目录启动，`/` 和 `/login` 均 200。
- 生产 Nginx 配置只做了 `nginx -t` 语法检查，没有 reload、切流或维护页变更。尝试用面板 Nginx 二进制启动独立临时监听时被其缺失的 `resty.core` 运行模块拒绝；生产 Nginx 未受影响。正式演练需在发布机提供完整 OpenResty/Lua 运行库，或使用已验证的 Nginx 发布副本。
- 因此本次完成了 Go ready、旧 PHP 可用和回滚目标验证，但不能声称完成真实 Nginx 切流。

### PG 回滚

- 在独立 rollback target 中先写入标记审计事件，随后使用切换前 `pg-cutover.dump` 执行 `pg_restore --clean --if-exists`。标记事件从 1 条恢复为 0 条，schema 仍为 137，用户 6、账单 18 摘要保持一致。
- 回滚只恢复 PG 隔离目标，不把 Go 写入反向复制到 MySQL；未知支付/供应商结果仍需人工核验。旧 PHP 冒烟已通过，但真实生产 Nginx 切回没有执行。

## 正式切换前的阻断项

1. 以当前源库重新生成并签字的 247 条问题处理清单；旧 245 条报告不能代替新快照。
2. 逐条完成人工金额、在运主机、上游资源和历史账单凭证复核；策略决定不等于业务证据。
3. 实现并演练真正的增量导入和 GTID/binlog 边界；当前失败行为必须保持，不能绕过 source changed 保护。
4. 由 DBA 提供全局 MySQL 只读/`super_read_only` 和 GTID/binlog 记录权限，正式窗口再执行生产只读。
5. 修复或提供独立可运行的 Nginx/OpenResty 发布副本，完成维护页、Go 切流、旧 PHP 切回和五分钟窗口证据。
6. 保持真实支付、供应商、通知和未知外部结果门槛；任何未确认外部动作都不能在回滚中重放。

本次演练结论：**数据决定已形成，隔离 PG 恢复和回滚通过；正式切换仍为 NOT READY。**
