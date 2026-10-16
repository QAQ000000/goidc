# 支付异常人工核验与责任退回契约

日期：2026-10-16，实施更新：2026-10-17。迁移151/152与真实API页面已集成，后端专项和竞态通过，最终集成与浏览器证据另列验收记录。承接迁移149/150，补齐G02未知关单及迟到收款的可审计处理流程；真实商户协议、公网和实际出款另列验收，不因本地模拟证明关闭。

## 方向与不可变事实

未知关单通过编译期适配器的只读`LookupClose`读取原操作可信终态；不再次调用Close，不生成新远端操作号，不用管理员勾选、截图、本地过期、404、status=0或当前新商户凭据推断关闭。原unknown及过租约checking保留；新结果只形成待复核证据，独立员工确认才原子应用最终关闭或正常可信收款结算。详见[核验设计](payment-close-verification-design-20261016.md)，本契约的明确补充优先。

关闭后迟到的成功支付未结算，属于待核验责任资金。实际已退回该笔款时，可登记外退证据，独立员工复核后追加责任解决事实。此流程不调用商户退款、不改变原invoice状态、原支付、用户余额、服务、佣金或原充值流水；不把该笔责任退款记入普通销售退款，防止凭空减少销售净收入。

所有新增写操作只允许有效员工浏览器会话；事务内复核权限、角色/员工启停、会话与客户范围。保存操作前固定body/key，未知回执保留；409显式重读，不自动换版本。危险写入有确认、幂等、审计和真实数据库故障回滚测试。独立复核限制同样约束超级管理员。

## A：未知关单可信核验，迁移151

采用设计中的三张追加事实表：核验请求、只读结果、独立决策。关联原payment/invoice/customer/instance/provider/operation/金额/凭据摘要；请求与结果不能由客户端指定终态或证据。新权限`payments.close_verify`、`payments.close_review`依赖`invoices.view`，不自动授予旧角色。

接口前缀：`/api/v1/admin/invoices/{id}/payments/{payment_id}`。

- GET `/close-verifications?limit&offset`：`items/total/limit/offset`，服务端能力、can_request、原操作与结果、复核、当前invoice版本；不回显密钥、凭据摘要或明文幂等键。核验申请仅向其本人返回`request_key_sha256`，用于精确恢复高熵原请求；其他员工不返回该值。
- POST `/close-verifications`：`{close_operation_id,expected_version}`，同键只读取原已存结果，不二次调用Lookup；新键可在资格允许时再次只读查询。合法的持久化unknown/unsupported/error也是已完成的“查询记录”，不等于业务解除冻结；以201返回记录，重放200，确实在途202。传输/存储结果未知才保留待核实请求。同键重放的只读查询租约已过期且无任何结果时返回409 `close_verification_expired`；明确拒绝后，员工必须按本人原键摘要、原支付/操作、金额/版本、已过租约及无结果精确重读，才能结束本地只读请求，再新建只读查询。不能据此解决原Close或复核写请求，也不解除业务保护。
- POST `/close-verifications/{verification_id}/confirm|reject`：`{expected_version,note}`，当前invoice版本。确认人不能是核验发起人或原关单员工；前端不能传入target_status。驳回仅追加事实，继续保护。

`CloseStatusGateway`必须只读，返回原三元组及closed/succeeded/unknown。succeeded必须有可信非空原渠道流水；closed必须是不可再收款的最终结果。原密文缺失或不匹配拒绝，不回落当前凭据。模拟适配器只有显式绑定原操作的已记录证据可返回终态；没有证据或模拟证据重启丢失返回unknown，不能“任何操作号默认closed”。真实易支付不新增虚构能力。

确认closed原子追加决策、payment.closed、invoice版本、审计；DB延迟约束同时接受原可信closed操作或关联完整的applied_closed决策。确认succeeded与正常回调/查单共用同一个事务内结算函数，决策、事件、余额/账单/通知/佣金/任务在同一事务提交；不能先确认再另开事务补账。回调先成功则保留真实成功，不关闭或重复入账；不同流水冲突。确认先关闭则后续通知走迟到异常。

统一SQL函数`payment_close_operation_unresolved(operation_id bigint)`：原状态checking/unknown、对应payment仍pending/expired、且无有效应用决策时才持续保护。兼容历史`succeeded/provider_paid`但原payment尚未结算的记录：保留原行并继续冻结，允许只读核验；新报告已付款而尚未结算的关单结果统一保存为`unknown/provider_paid`。所有资金/取消/删除和DTO使用一致谓词；原历史状态照常展示。活动checking未过租约时排斥其他核验；过租约不阻断只读状态/收款查询，不解锁付款。

旧关单未知请求的前端恢复不得永久卡死：原POST同key重放可查已追加的最终决策并返回有效解决回执（包含原operation_id及resolution关联/最终状态/版本），不再次调用Close、不改变原记录。UI严格验证同一原操作的合法解决回执后才能归档本员工原请求；无法核验继续保留。支付成功结果不能仅靠原Close报告paid就归档，必须有明确的可信结算/决策事实。

同步修复调用Close之后因版本冲突等不能应用却记录可重发error的边界：已发送结果无法可信应用须继续冻结，不能换键再次写渠道。原checking过租约不允许FailPaymentClose回写；新核验承担恢复。

## B：迟到收款责任退回，迁移152

新增`payment_close_anomaly_refunds`：以原payment、原商户scope、精确external_transaction_id组成责任组；绑定原凭据摘要、原金额、锚点anomaly与事件组版本。anomaly原表不改不删。组内重复event不等于多笔收款；新同交易号同金额event在已确认退回后不重新冻结，新交易号、空号、金额或原凭据关联不符仍保护。

空/不可核验原交易号禁止登记及解除，管理员不得自由填原流水修复证据。金额从原payment导出，全额退回；原交易与其他成功payment/已确认退款交叉占用须拒绝。原Epay带商户namespace的流水精确比较，不截前缀或trim成另一个身份。实际退回时间不得早于原payment创建，不能因通知晚到强制要求晚于anomaly被本站发现的时间。

同组至多一个非rejected退款；requested/reviewed独立，pending_review/confirmed/rejected及version，终态不可改删。字段沿用外退路径、退款流水、实际到账时间、受控凭证引用/SHA-256、说明；凭证由人工核实，不自动宣称真实到账。

共享`external_refund_payout_claims`以merchant_scope+provider_refund_id全局唯一认领，覆盖旧external_refunds和新异常退款。迁移回填，DB触发器同事务reserve/confirm/release；confirmed不可释放，rejected保留历史可释放，不只在Go层交叉查重。任何审计、认领或约束失败整体回滚。

新增独立权限`payment_anomalies.view/create/review`，均依赖`invoices.view`，旧角色不自动授权。API：

- GET `/admin/invoices/{id}/payment-anomaly-groups?limit&offset`：组、锚点、原payment/transaction、金额、event_count/first/last/group_version、merchant_verified、历史退款与can_create/can_review、安全状态。
- POST `/admin/invoices/{id}/payment-anomaly-groups/{anchor_id}/refunds`：`{method,provider_refund_id,received_at,evidence_ref,evidence_sha256,note,expected_group_version}`；原客户/金额/商户/交易从服务端事实读取，不可传入替换。
- POST `/admin/invoices/{id}/payment-anomaly-refunds/{refund_id}/confirm|reject`：`{expected_version,expected_group_version,note}`。待审时新增event使组版本变化则409；显式重读后用新审核请求可继续原pending申请，不要求驳回重建。
- GET `/admin/invoices/{id}/payment-anomaly-summary`：`anomaly_received_cents/anomaly_refunded_cents/anomaly_pending_cents/unresolved_groups/unverified_groups/unverified_amount_cents/currency`。可核验交易去重统计；空号组按payment单列未核验责任金额，不能将event数当实际收款笔数。
- GET `/admin/invoices/{id}/payment-anomaly-entries?limit&offset`：独立责任收/退方向流水。普通销售/网关收入/退款统计不回写，UI明确此为销售之外的责任资金。

统一SQL函数`payment_close_anomaly_unresolved(anomaly_id bigint)`用于全部原保护/DTO。仅匹配完整原组、凭据、交易及金额的confirmed退款才解决；任意其他组确认不能整单解除。历史count全部保留，额外显示未解决组，不靠删除历史解冻。

## 前端与验收

在账单详情原付款表后新增支付异常核验区，保留原关单/查单历史和普通已付退款区。分别展示关闭状态核验、迟到收款责任组、异常资金汇总与收退流水。手机单列卡片，分页及选中上下文保留URL；只显示服务端授权动作和真实能力，不生成虚构成功数据或模拟证据生产入口。

新增流程可共用小型持久化mutation工具，现有已验收页面避免无关重写。先核对身份、保存固定path/body/key，再发请求；回执严格核对关联、版本及实际终态，复验身份再清理。业务unknown记录可确定收到，但不解除业务冻结；传输未知则保留。损坏存储或存储失败不得发新请求。原请求重放先持久化rejected=false，防止旧拒绝状态误清理未知结果。

隔离PG18验证迁移及不可变/关联/唯一约束、原凭据、回调竞争、独立复核/撤权/客户范围/过期会话/API Key拒绝、8路同异键并发、跨新旧退款认领、不同event同交易去重、不同交易继续冻结、审计及结算失败回滚。执行全量、相关race、Go build/vet、前端typecheck/lint/build；四尺寸/短屏真实API覆盖保存取消、加载/空/错误、403/409、身份隔离、丢回执与刷新恢复；记录截图摘要、金额和清理证据。真实外部条件继续单列，不用模拟结果替代。
