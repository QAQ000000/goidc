# 待审核未交付人工订单外部退款验收（2026-10-17）

状态：本批实现及隔离验收通过；范围仅限下述未交付人工新购订单，不据此关闭G03或部署。契约见[订单退款设计](order-external-refund-next-contract-20261017.md)。迁移154只在隔离PG18测试库应用，没有应用到业务运行库。

## 实际实现范围

只接受迁移154保护下新建、纯外部全额收款、订单仍待审核、全部服务为manual及pending_payment、从未存在交付任务、无佣金/营业额关联的整单退款。真实cart与管理员下单均以order_items保存商品/数量/安装费，订单保存优惠，服务按单位映射；不伪造新购invoice_items。

登记只写持久交付冻结及原版本快照，阻止审核/交付，不改变资金或库存。另一名有权限员工确认后，账单/订单标为refunded，全部服务cancelled，库存按单位仅释放一次；驳回只释放冻结，保留paid与原待审核历史，不自动开通。客户余额/信用、原支付succeeded、原paid_at与收入事实不变，不生成terminate任务，也不调用商户退款或供应商。

新增不可变订单/服务出生记录、交付历史、真实库存预留及释放事实。资格同时检查当前状态和历史，事后清除导入标记、移入移出服务、删除任务均不能恢复本批资格；任务service_id/action禁止事后修改。有限库存由事实触发器实际扣减/回补，无限库存保持NULL。原快照使用UTC/ISO，跨连接时区与恢复后仍可比对。

新增`GET /api/v1/admin/invoices/{id}/external-refund-order-context`；登记请求绑定账单、订单及完整服务版本。服务数组乱序规范化后可原键重放，重复/缺项/错项拒绝。沿用有效员工会话、销售客户范围、独立复核，订单写入额外要求`external_refunds.order_reverse`。订单/服务列表和详情展示数据库持久冻结与退款终态，不从本地弹窗推断交付可用性。

## 交叉审查与修补

- 新生订单事后设置再清除legacy来源曾可重新符合资格；现order/invoice来源变化永久留痕，当前来源非空也拒绝。
- 新增关联守卫曾对既有佣金纯金额/状态UPDATE反拿来源账单锁，导致无退款冻结的正常提现冲突；现仅明确桶/状态允许字段变化且其余完整快照相同时跳过父锁，关联移动、插入、删除仍受保护。
- 普通create任务原可修改service_id后移回，绕过出生历史；现身份更新直接拒绝，不在worker的普通status/attempt/lease更新中新增service父锁。
- 既有并发佣金登记先共享锁账单，再等待客户锁；新增INSERT守卫曾错误升级到排他锁，现只对佣金/营业额INSERT沿用SHARE NOWAIT。并发共享读者可以继续登记，退款的FOR UPDATE及持久hold仍阻止新增关联；既有并发佣金测试修后连续3次通过。
- 历史数据库升级只为154后的新对象记录出生，不追认旧单；由schema153真实升级与当前checkout验证。

以上是本批新增的保护。任意数据库管理员直接交换两张同客户同金额账单的订单归属并不属于受支持业务操作；当前生产Go没有该关联修改入口。本次未新增通用财务关联重写接口或承诺抵御数据库超级用户绕过触发器。

## 最终验收记录

产品/SQL基线`0d12e4a2338d602777a10f66f689153a5c58d8a6`；迁移154 SHA-256为`5ff26f58ad73693390b15fd1e35a6d3014107eaced6b15df2a4ad2403995687a`。后续纯文档提交不改变此基线。

| 检查 | 当前实测结果 |
| --- | --- |
| Go build / vet | 最终产品代码通过，命令退出0 |
| Store专项race与两类恢复 | 最终SQL通过，`-race -p 1 ./internal/store -count=1 -timeout=10m -v`，使用`-run`限定订单退款、checkout库存及当前schema恢复专项，109.664秒，退出0；显式开启恢复opt-in |
| schema153→154升级 | 3.89秒通过；使用真实embedded迁移建立153与合成旧checkout，升级/重复Migrate保持旧订单、收款、回调、paid_at和库存，旧单拒绝；升级后当前checkout真实扣库存且可申请 |
| 当前schema代表性恢复 | 10.04秒通过；211表、281行、135序列、6223结构记录、156个迁移文件，全快照一致；不把小夹具等同业务快照 |
| 新购订单待审冻结恢复 | 9.16秒通过；持久hold阻止交付，自审拒绝，恢复后独立确认，2单位释放一次、零交付任务；原请求/确认响应重放保持不变 |
| 相关旧业务回归 | 子代理最终SQL聚焦HTTP整组93.295秒通过：Affiliate、CreditLifecycle、供应商导入下单交付、管理员调价/延迟账单、ConfigurationUpgrade；不替代主线全量 |
| HTTP订单专项race | 最终SQL通过，`-race -p 1 ./internal/httpapi -run '^TestOrderExternalRefund' -count=1 -timeout=10m -v`，223.985秒，退出0；包括49项保守拒绝矩阵、8例真实锁等待后撤权/范围撤销、真实cart/admin签名付款与独立确认/驳回、原回调重放、版本及乱序同键重放、故障整体回滚与并发唯一确认；默认SKIP的浏览器opt-in另验 |
| 强制隔离PG18全量 | 最终产品代码及夹具修复`c2806f9`通过：`scripts/test-all.sh`，HTTP1738.826秒、store190.118秒，全部测试包ok，命令退出0；专用PG18数据库验后随机测试schema残留0、其他连接0 |
| 前端 | 最终UI `beec800` typecheck/lint退出0；lint 0错误、7个既有warning。独立worktree webpack build通过；随后两个纯布局修补已重建复验，源版本与BUILD_ID分别登记 |
| 浏览器及资金复核 | [最终浏览器验收](order-external-refund-browser-acceptance-20261017.md)通过：五尺寸含320短屏、真实API丢回执/409/权限/独立复核/冻结投影；68张截图与101个证据文件由主代理独立校验摘要，10项只读数据库投影与final_db一致。专用API/预览/浏览器/schema均已回收，截图保留 |
| 集成测试夹具 | 原两个供应商测试直接将create任务改为sync，被新身份保护正确拒绝。`c2806f9`改为通过正常action API新建sync；主代理独立PG18复跑两项供应商及Traffic退款测试均通过（5.905秒），没有放宽生产保护 |

测试仅使用loopback隔离PG18（server_version_num=180000）和本批专用`_test`库；每例自有随机schema。恢复source/target独立，未连接业务库。代表性恢复archive SHA-256为`5cf18ffa75bf153a488aea97cf3160b7bf90e73cb16dd15c627ead946008d4e2`，snapshot为`fb7d61e2cb179ad6cc6e43b2ea08baf47c6b7c124fcde840855357240e6ff01c`；订单hold恢复archive为`c3b37dbacc580ae28afba708072284e5efa5b76874609f4186f6d0bb8d8c2782`。HTTP最终race日志本地`/tmp/zjmfgo-order-refund-http-final3-race-20261017.log`，SHA-256为`853b95916fe9bea3f12b762f0877d63cb1418109a9f24d2bdf53ef6fac7d988a`。主代理store/race日志本地`/tmp/zjmfgo-order-refund-store-race-final3-20261017.log`，SHA-256为`3b249ece78c29618495fce2beba0c1da45c60c88f0a1bdf361f99c7f2a5d9177`。

最终全量日志为本地`/tmp/zjmfgo-order-refund-full-final4-20261017.log`，SHA-256为`4c0aba2dc8d03777de340ac4a862d8bf9f08bcb22a6e8b896e8d57092879adcf`。验收后原API/worker/Web PID仍为608393/608413/1647696，没有部署、重启或更改业务库。

旧SQL版本的专项成功、因修补主动终止的全量、预验截图都不冒认为最终版本通过。浏览器与Go的opt-in夹具默认SKIP不等于通过；最终单独启用并核验。

此前final3全量不计通过：两个旧供应商测试通过修改任务action构造夹具，与迁移154的身份保护冲突；随后HTTP包达到原30分钟上限，Traffic退款测试被整体超时终止。修复测试夹具后，三项专项已独立执行通过；强制全量脚本的显式包超时改为45分钟，以容纳逐例迁移隔离schema的完整HTTP套件，不以延长超时代替失败修复。

## 保留边界

已审核/已交付、上游/NOKVM/simulation、续费、升级、信用或混合资金、任何佣金关联、部分退款继续拒绝。真实商户出款与付款人到账、公网通知、正式业务快照升级、生产切换均未据本地协议夹具验收；外部退款登记不是自动原路退款。G03仍未关闭，105页台账仍45完成/60部分。
