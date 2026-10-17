# 本地续费外部退款与精确冲正契约

基线 `9fca0d2`，状态：设计与实施中，未验收。承接 G03；不授权真实商户退款、资源写入或部署。当前 `finance.go` 退款按现周期减期不具备可逆事实，本批一并修复，不能只增加一个绕过原保护的外部退款入口。

## 经营行为

本地 manual / simulation / nokvm 的续费只改变本站期限，supplier 续费依赖上游，不混用。支持完整的一期全额冲正：原期限存在且尚未到期，新续期尚未开始，当前仍精确持有该期生效后的期限、权益及关键配置，没有未处理依赖。原服务继续使用原有期限；不删除服务、不取消原新购订单、不释放库存。已消费续期、部分退款、供应商续费和其他配置/信用/佣金冲正继续单列，不能宣称整个 G03 完成。

**经营规则调整：** 对有不可变事实且完全未消费的本地续期，精确恢复原到期日与原 `renewal_discounts_used`，包括零额、余额和未还款信用续费的既有退款路径；替代原“减当前周期且不返优惠次数”的行为。回滚期限的同时返还对应未使用权益，不允许保留新增期限再返次数。已使用、缺事实或后续依赖无法证明时拒绝，不能用当前数据猜测。

外部收款仍复用原 `external_refunds`：登记的是已经由外部完成并核实的退款，不实际调用渠道；另一员工独立确认才记录冲正与出款事实。原支付 succeeded、收款记录、paid_at 均保留，不往客户余额充值。确认本地余额退款则沿用既有正额余额退款账本；信用未还消费沿用既有债务冲销，不能把它说成现金退款。

## 不可变事实与冻结

新增迁移155，在 `extendRenewalDue` 同一结算事务记录 `service_renewal_effects`：invoice/item/service/user、身份与关键配置、实际 billing_cycle、单次数据库结算时刻、before_due / period_start / after_due、before_used / after_used。延期计算使用同一捕获时刻。effect 只能由真实延期事务写入，身份、期限、权益、时间不可更新删除；无历史回填。supplier 不写本地效果。

外部登记建立 `external_refund_renewal_holds`，绑定 effect/refund/payment/invoice/service/customer、申请服务与账单版本、原始快照。状态 pending → applying → applied，或 pending → released，具备唯一 live 约束、不可变关联和 deferred 完整性校验。pending/applying 阻止服务语义写入、任务及新增业务依赖；applied 解除服务冻结但永不允许重复冲正同 effect。驳回只释放，不改期限/次数/余额；即使已跨截止时间仍能驳回。

确认和既有余额/信用退款都调用同一精确逆操作，恢复 before_due / before_used；成功冲正关闭 auto_renew，并递增 renewal_generation，保留关闭原因，防止立即再次购买。自动续费幂等键带 generation，恢复旧 due 后重新启用不会重放已退款账单；原账单和键不得删除。旧 generation=0 的既有键兼容；候选与创建时重新核对 generation，不得把旧候选应用到新代。

## 两次资格校验

登记和确认均从服务端核对；不只信前端禁用：

- 一个真实 renew item、一个对应 effect，本地服务 active/suspended、已完成导入复核；当前身份/关键配置、due 与 used 与 effect.after 一致。
- 原 due 非空且在数据库当前时刻之后，period_start 未到。返回 `review_before`，审核跨截止拒绝确认并保留待核实状态，允许驳回释放；不伪造延期或自动退付。
- 无尚有效的后续续期 effect；已完整冲正的后继可按可验证的 LIFO 顺序解除依赖，未冲正后继必须先处理。
- 无未付续费/升级/流量账单、配置变更、待处理取消，或 pending/running/waiting_manual/waiting_verification 任务。与原新购正常历史任务不混淆。
- 外部路径唯一全额成功收款、无余额/信用混合、无有效发票、佣金/业绩关联，原商户身份匹配；pending/expired 意图、unknown/checking 关单和未解异常保留原有保护。
- 申请携带原账单版本、effect ID 和服务版本；过期返回409。确认用退款原版本、独立操作者、权限及会话。拒绝不应因业务资格变更而被阻断。

缺 effect、已消费、上游权益、后续依赖、配置变化和在途操作各返回稳定 reason_code。错误不泄露 SQL 或其他客户资料。金额继续整数分。

## 并发与数据库边界

保留资源范围 → actor/session → invoice-customer advisory → financial customer → payment → invoice → service → hold/effect 的锁序；服务写入不得在持 service 锁后阻塞等待原账单。关联触发器通过父服务锁串行，反向已持子对象路径使用 NOWAIT 返回可重试冲突，不制造死锁。

DB 保护覆盖 services、provision_jobs、invoice_items、configuration/traffic/cancellation、原 invoice/payment、balance/credit、发票、commission/turnover 的 INSERT/UPDATE/DELETE，包括旧关联和新关联；不得只检查 HTTP 入口。effect/hold 身份保护、状态机、快照校验与 deferred 检查必须防直接 SQL 半成品。不可用会话级开关绕过保护。

## API 与界面契约

- 新增 `GET /api/v1/admin/invoices/{id}/external-refund-renewal-context`，沿用会话、external_refunds.view 和客户范围，返回 invoice_id / invoice_version / eligible / reason_code / reason / effect / service_version / review_before / renewal_reversal；不存在或非范围不可泄漏。
- `effect` 包含 id / service_id / billing_cycle / before_due / after_due / period_start / before_used / after_used，时区使用标准 RFC3339；上下文必须与当前账单绑定。
- 新增权限 `external_refunds.renewal_reverse`；所有续费外部登记、确认、驳回都要求此权限及原 create/review 权限。查看仍沿用 view。撤权在下一请求及等待锁后重新生效。
- 外部登记在原结构上增加 `expected_renewal_effect_id` 与 `expected_renewal_service_version`；非续费请求不得接受这些字段来混淆幂等。退款列表/详情增加 `renewal_reversal`：status / effect_id / service_id / invoice_version / service_version / before_due / after_due / before_used / after_used / review_before / created_at / applied_at / released_at。
- 后台沿用账单退款面板，显示原到期、续费后到期、恢复后到期、优惠次数、复核截止和冻结状态；明确“服务保留原期限，自动续费将关闭，客户余额不变”。成功、驳回和重新进入页面均从真实持久事实读取。
- 保存/取消、权限拒绝、资格失败、409、未知响应、刷新及原请求恢复沿用已有规则：保留原载荷/键/版本，禁止自动换新版本重发；恢复校验必须接纳且严格校验新增两字段，不能丢弃旧请求。

## 完成标准

隔离PG18迁移与升级/重复迁移、Go build/vet、强制全量、相关race、前端typecheck/lint/独立build；真实API四宽浏览器覆盖申请/确认/驳回/期限与次数投影、权限、409、未知恢复、取消、空态/错误及跨页历史。后端必须覆盖月底/闰年、零额/折扣/多周期、NULL/过期原期限、跨复核截止、LIFO/后继、自动续费重启新代、晚回调、直接SQL、worker/配置/取消竞争、审计失败回滚和dump/restore。

外部协议夹具不证明真实出款到账。真实商户退款、公网通知、生产迁移和部署不在本批本地验收中完成，不缩减总体目标。
