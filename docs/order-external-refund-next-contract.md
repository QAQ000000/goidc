# 下一批订单外部退款设计契约（2026-10-17）

**当前状态：首批已实现并集成，最终验收进行中，G03仍未关闭。** 实际范围、修补及最终证据以[专项验收](archive/README.md#f-a73a0cdfb751)为准；下文保留实施前的设计与门槛，不把“拟议”“建议”视为当前未实现清单。原设计基线 `aa397f2158bffe8553898bae0809c33a51e539cb`。本文不授权真实商户退款、资源调用、生产迁移或部署。

推荐下一批仅支持：**已全额外部收款、仍待审核、完全未交付且无佣金关联的人工交付新购订单整单退款。** 其中“未交付”要求所有服务仍为 `pending_payment`，整个订单从未有交付任务；不接受“任务还没发出”“后台已手工取消”或“现在没有资源”作为等价证明。供应商、NOKVM、simulation和已审核排队订单均不在首批。

## 代码事实与旧系统参照

下列是本次静态阅读事实，不是运行验收：

| 实际代码 | 已有行为与设计影响 |
| --- | --- |
| `internal/store/payments.go:184` `settleExternalInvoice` | 成功收款将账单paid、订单paid；review_status=pending时不创建任务，其他情况将服务queued并插入create任务。因此“已收款仍待审核”存在真实可到达的无任务状态。 |
| `internal/store/order_review.go:20` `ReviewOrder` | 先锁账单再订单；审核通过改订单active、服务queued并插入任务。退款申请必须持久阻止这一批准路径，不能等确认才检查。 |
| `internal/store/services.go:603/629/712/887` | create仅允许queued/failed/provisioning；ClaimProvisionJob直接锁job、增加attempts，不先锁服务；ProcessProvisionJob再锁service→job；人工完成要求waiting_manual。仅检查external_started_at为空会漏掉已领取或人工开通。 |
| `internal/store/supplier_delivery.go:44/209` | 供应商任务分为持久标记external_started_at后外部调用和结果回写；未知结果不能当作未交付。 |
| `internal/store/finance.go:351` `RefundInvoice` | 本地退款会加客户余额或减信用债务、修改paid_at、处理期限/任务/库存/佣金。成功外部收款明确拒绝。新外部退款不能直接调用该函数。 |
| `internal/store/orders.go:147` `cancelUnpaidOrder` | 仅未付款订单；服务/库存释放可参考，但不能直接用于已收款订单，也不能继承它取消running任务的宽条件。 |
| `internal/store/renewals.go:183` | 本地续费按GREATEST(原到期日,now)+周期，可能消耗限次续费权益；不是简单减周期的可逆事实。 |
| `internal/store/supplier_renewals.go:113` | 本地退款帮助函数允许failed或未派发pending续费；failed本身未证明外部从未生效，不能原样提升为外部退款标准。 |
| `internal/store/finance.go:520`、`configuration_upgrades.go` | 本地升级退款限定queued/pending/attempts=0并释放变更库存；仍需补持久交付冻结与升级差价/权益原事实，另批处理。 |
| `internal/store/affiliate.go:478/595/645` | 收款事务可同时写营业额和pending佣金，订单待审核不代表无佣金。即使无commission，也可能已有turnover；两者都必须检查。 |
| `internal/store/affiliate_lifecycle.go:30/156` | ReverseAffiliateCommissions可反转pending/available，并为reserved/paid追加clawback；追加追偿不等于追回现金。release按invoice→order→service→account加锁，实际发放另有提现锁序。 |
| `internal/store/finance.go:445` | 信用本地退款仅允许未偿金额等于原金额；已还款转人工核对。信用付款无独立网关入款可直接据以外退；不能把外部出款当作降低used_cents。 |
| 旧系统 `app/admin/controller/InvoiceController.php:1151` | `only`以原accounts入款减历史出款限制金额，追加amount_out并改账单Refunded；不等于资源注销，且支持部分金额/合并账单，不能直接照搬。 |
| 旧系统 `app/admin/controller/ClientsServicesController.php:1452` | 资源退款按天/全额/自定义追加账务，本段未建立可信资源终止与独立复核事实。 |
| 旧系统 `app/common/logic/Invoices.php:495/506` | 汇总accounts和credit计算可退额，显示混合资金需要专门分摊，不能把全部账单额推给网关。 |
| 旧系统 `app/admin/controller/SaleController.php:984/1006` | 退款参与销售阶梯/产品类别佣金计算，不等于已提现佣金原子追回。 |
| 旧系统 `app/admin/controller/AgentController.php:938` | 资源退款走上游resource/resourcerefundfromagent；上游退款与本站账单退款是独立边界。本次仅阅读，没有调用。 |

旧系统路径均相对于 `/www/wwwroot/zjmf-manger-decoded/`。只使用已读代码支撑上述判断，不假设空hook已执行第三方退款/佣金逻辑。

## 可拆分路线与拒绝边界

| 方向 | 可安全拆分的必要条件 | 此次推荐 |
| --- | --- | --- |
| 待审核未交付新购订单 | 唯一全额收款、manual服务均pending_payment、无任何任务、无营业额/佣金、订单/服务/库存事实齐全；申请起冻结批准/交付 | **第一批，见下文** |
| 已审核queued但任务尚未派发 | 所有create任务pending、attempts=0、external_started_at/locked_at为空；必须先解决Claim竞争、重试/恢复/人工入口冻结与旧worker租约 | 后续独立批次，不能仅放宽status |
| 已交付服务或上游订单 | 可信资源销毁/供应商取消及结算退款事实、不可恢复交付、数据处置与库存重新销售证明；终止状态/管理员备注不足 | 拒绝，缺少完整跨系统证明 |
| 本地续费 | 原期限before/after、实际生效边界、后续续费/权益消费、自动续费与逾期挂起竞争、退款时已消费期限处理 | 独立契约；禁止直接next_due_at减一个周期 |
| 供应商续费 | 从未派发的原任务及全程冻结，或可信上游撤销/期限回滚事实；未知或已派发失败拒绝 | 独立于本地续费，不复用failed=安全 |
| 升级/降级 | 原配置/价格/周期/库存/负差额信用事实，升级从未执行；取消pending任务必须与worker互斥；已执行需可信回退 | 拒绝，不复用充值冲正 |
| 信用 | 消费、还款及网关收款各自关联、混合资金分摊、债务原始与已偿事实，防双退 | 拒绝，不外退信用消费或直接减used_cents |
| 佣金 | 未发放pending可设计原子反转；available/reserved/paid涉及提现竞争与追偿，不能把clawback当追回完成 | 第一批全部排除，后续优先纯pending且无turnover阶梯副作用的专项 |
| 部分退款/多账单/部分服务 | 整数分分摊、累计出款上限、税票/优惠/佣金/库存分摊与剩余交付 | 全部拒绝；第一批只匹配原单全额 |

## 首批准入：每项均在申请与确认事务内重验

1. 沿用external_refunds商户身份、原支付与证据摘要保护。唯一succeeded原支付属于同客户和同账单，金额等于账单/订单全额，账单paid、order_id非空、automatic新购账单、订单status=paid、review_status=pending、未软删除；本地paid_at不变。拒绝其他成功支付、余额payment/refund、credit_invoice_payments、其他未可信关闭的pending/expired意图、未解决关单/迟到收款异常、活动发票申请、迁移待复核及历史导入来源。
2. 订单只有这一张业务账单。真实 cart/admin 新购路径以 `order_items` 保存商品、安装费及数量，订单保存优惠，并按逐单位建立 `services`；它们不写 `invoice_items`。因此本批要求 `sum(order_items.total_cents)-orders.discount_cents=orders.total_cents=invoices.amount_cents`，逐项校验 `(price_cents+setup_fee_cents)*quantity` 与服务完整集合，并要求该新购账单的 `invoice_items` 为空。不能套用续费或人工账单的分项结构。正式验收仍须从真实 cart/admin 路径各生成一例证明映射。无续费/升级/流量/配置变更、无其他已付或待付服务账单、无服务取消/迁移/转移/外部操作关联。
3. 所有订单服务均manual、pending_payment、next_due_at=NULL、upstream_id/username为空、无资源/凭据/外部绑定有效载荷、未曾交付/导入、inventory_released=false；服务数和order_item数量/逐unit映射完整。缺少服务、数量不符、关联其他客户/订单/商品/单位序号均拒绝。若现有库不足以证明从未改回pending_payment，则首批只允许新迁移后创建、受新防护覆盖的订单；不以当前空字段替代历史证明。
4. 整个订单全部服务**没有任何provision_jobs历史**，不接受cancelled/failed任务。拒绝supplier/NOKVM/simulation和手工等待、已激活/挂起/失败后改回等情况；没有“人工确认未交付”例外。
5. 无affiliate_turnover_entries、affiliate_commission_entries及相关提现/追偿事实。佣金为0或已reversed仍属于关联，不接受。无pending也不能跳过turnover检查；不得删除营业额来获得资格。
6. 商品库存预留可以从订单/服务单位事实完整证明，库存数值不会溢出，商品/关联记录存在；预先inventory_released或有异常库存补偿记录拒绝。当前无限库存(stock=NULL)继续NULL，但仍标记单位已释放；有限库存按实际尚未释放单位加回一次。

错误应区分（建议码，未实现）：`external_refund_order_not_eligible`、`external_refund_delivery_started`、`external_refund_affiliate_linked`、`external_refund_order_hold`、`external_refund_amount_mismatch`、`version_conflict`；不把敏感资源或凭据回传。

## 申请冻结、拒绝释放和确认

**申请：** 在现有退款三权限外，需相应订单/交付管理权限以授权阻止与取消交付；建议新增专用 `external_refunds.order_reverse`，默认不分配普通角色，确认者也必须拥有。保留有效浏览器会话、客户销售范围、独立员工复核、原商户证据、原版本与载荷幂等。申请同步写订单交付hold与完整snapshot，不改变原invoice/order/service业务状态，不加减客户余额，不释放库存、不产生出款。不复用149余额预约表，订单资金未入客户余额，无冻结余额语义。

hold自pending生效直至独立reject或confirm；API超时/5xx/未知结果保持hold和原请求键，不通过浏览器关闭窗口、换键、超时自动释放。必须阻断审核通过、生成/重试/人工完成交付、服务/订单成员与交付模式改变、删除/导入覆盖等入口。退款驳回是退款结束，仅将hold released并审计；不把订单审核改approved，不自动开通，不更改任何余额/库存/原收款，后续走原订单审核。订单备注可允许编辑，但会推动版本；确认要求显示变更并重验，用独立明确接口补正快照，不能静默换版本。

**确认：** 在锁内确认hold pending和全部快照/版本/准入仍一致，独立一次性把invoice refunded、order refunded、服务cancelled并推进version；保留订单review_status=pending作为原审核历史（UI明确退款终态优先），不写伪造approved/rejected。逐服务inventory_released false→true，按商品排序聚合释放库存、追加不可变库存释放事实；退款/hold applied、业务冲正与员工审计同事务提交。原支付succeeded/completed_at/商户流水、invoice.paid_at、原收入/回调原响应全部保留。**不追加正向余额退款、不扣客户余额、不改信用、不创建terminate任务、不执行外部I/O。** 财务统计仍以external_refunds.confirmed出款一次；库存事实不作为第二笔资金出款。

任何快照漂移、旧写入绕过hold、库存/服务/审计错误整体回滚并保留pending；不得一部分服务取消后仍确认整单。拒绝在变更资格后仍应可执行以解除hold，但不可绕过独立人/权限/范围/版本和不可变历史。

## 锁序与防护实现前置条件

沿用 `aa397f2` 的 sales scope共享advisory→actor/role/session行锁→新statement重验scope；再按现有退款 invoice-customer advisory→financial customer→原payment→invoice→order→服务id升序→必要job id升序→product id升序→hold/冲正事实→审计。幂等仍在事务内验权限/范围后重放。相同客户/原单关联锁不得在锁后切换；若lookup发现归属漂移拒绝。

此顺序是**目标契约，不声称所有现有路径已一致**：ReviewOrder当前缺客户资金锁且requireAdminOrdersManager在范围锁前；需针对审核/退款共享范围和invoice→order顺序核对改造。服务动作目前service→job；不能在已锁service后再用阻塞FOR UPDATE回拿order，否则会与order→service退款死锁。为首批hold，入口可在取得业务锁前锁同order，或在service锁内只读hold并在同service锁内完成变更，使退款持有服务锁重新检查无任务；选择必须用实际等待图与race证明，不临时添加第二套全局锁。

首批无任务，因此Claim/lease恢复不需要取消已有worker；但新任务插入必须被hold/退款终态阻断。DB写守卫须与应用锁一致：任务插入可通过其service行锁和hold查询防phantom；服务状态/关联变更在同service行锁下检查hold，订单审核在同invoice/order锁下检查。不要在普通行级UPDATE触发器隐式反拿上级order锁。跨表资格谓词独立SELECT不能阻止并发插入；需要guard与父行锁协议共同完成证明。无法为所有插入/重试入口建立该协议时保持本功能关闭。

未来佣金批次才追加invoice→order→service→affiliate account/id→entry/id，与releaseAffiliateEntry对齐；本批无佣金，无需新账户锁。持有行锁期间不调用商户/供应商、不等待网络。

## 实施前数据库迁移建议（已分配154，实际以SQL为准）

建议在合入时分配153之后可用编号，新增：

- `external_refund_order_holds`：refund_id主键；invoice/payment/order/customer/amount；期望invoice/order版本；服务集合/资格快照摘要；pending/released/applied；时间与独立reviewer；每订单唯一非released活动hold。关联、原快照/创建时间不可修改，终态禁止改删。可以先持有行锁生成快照，单事务插入hold和external_refund，避免无hold待审事实。
- 不可变逐服务snapshot表：hold/refund/service唯一，绑定order_item/product/unit与原status/version/inventory_released等白名单；不存密钥/完整凭据。服务成员必须与实际全集一致，不能仅信任JSON给出的子集。
- 不可变订单冲正/库存释放事实：每refund/order唯一，每refund/service唯一，记录前后状态/版本、释放单位和商品、库存有限/无限的before/after、确认员工和时间；同一次确认不可第二次追加。保留订单原价/优惠快照，不把释放数量当钱。
- 校验函数与DB触发器绑定external_refunds同原payment/invoice/customer/amount，创建hold必须pending_review；应用必须存在完整服务/库存事实并匹配状态；拒绝错误订单/客户/服务集合、非法终态、删除历史、直接跳confirmed而未业务冲正。明确事务内顺序及必要deferred约束，避免先应用再校验把合法原子事务锁死。
- 资格查询/阻断读取索引按order_id、service_id、pending状态；利用现有唯一性，不建通用资金/资源工作流框架。新增迁移前后真实PG18升级与CheckSchema，旧149/153充值路径不得受影响。

## 实施验收门槛（结果以专项验收为准）

1. **真实创建闭环：** cart及管理员路径各创建manual待审订单（含多商品/数量、安装费和优惠），本地签名网关收款，证明paid+pending_review无任务；登记hold→审核通过/任务插入均拒绝→独立确认→整单/服务终态、库存仅释放一次，客户余额/信用不变、原paid_at/支付/收款保留、出款一次。
2. **驳回闭环：** pending hold阻断交付；驳回只释放hold，订单仍原paid/pending；原审核通过后正常queued/create，证明没有自动开通或永久卡死。旧申请/拒绝键仍重放历史响应。
3. **实际竞争：** 登记与ReviewOrder批准在invoice/order/service锁真实等待，两种胜序：批准先赢则退款拒绝无部分事实；hold先赢则批准明确拒绝无任务。登记/确认与直接任务插入、service成员变更、库存编辑、invoice申请、销售范围撤销互斥；要求pg_blocking_pids证据及限时race无死锁。未知请求重放不能建立第二hold。
4. **故障原子性：** 对每阶段注入DB/审计故障，断言退款/hold/订单/服务/库存/冲正操作全快照回滚；确认重复/不同键并发只应用一次；禁止部分成功。数据库直接错关联、缺一服务、重复库存事实、改终态/历史删除均拒绝。
5. **保守拒绝矩阵：** approved/queued、任何任务历史（含pending attempts0、failed/cancelled）、external_started_at未知、manual已交付或被改回、供应商/NOKVM/simulation、credit/余额混合、pending/expired其他意图、关单异常、active发票、迁移待核验、跨客户、成员缺失、库存异常、turnover存在但无佣金、pending/available/reserved/paid/reversed佣金、续费/升级/部分退款全部拒绝且无残留。
6. **安全与幂等：** 三权限+专用订单冲正权限、有效浏览器会话、API-key拒绝、自审拒绝、scope撤销/permission撤销在锁等待后重验、expected invoice/order/service版本冲突；同键异参409、同键原响应、不同键同单冲突；证据/响应无敏感资源字段。
7. **回调与恢复：** pending与confirmed期间原签名成功通知重放，不能生成新job、佣金、恢复paid或覆盖原退款/hold；模拟响应丢失前端保留原键/版本/载荷、刷新读取最新事实，再用原请求核实；不自动换键/解冻。worker故障/恢复不能凭空创建无任务订单的资源。
8. **检查范围：** 专用PG18库+独立schema、迁移与DB断言、store/HTTP race、Go build/vet，前端lint/typecheck/build及四尺寸真实API浏览器流程。任何只读设计、编译或模拟网关不作为真实商户退款、实际资源销毁或生产验收。

只有上述门槛闭合后，才能标记“待审核、manual、从未有交付任务且无佣金的新购订单整单冲正”完成；其他复杂业务继续按原保护拒绝，G03总体仍未完成。
