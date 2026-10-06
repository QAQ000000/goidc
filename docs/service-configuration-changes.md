# 服务配置变更

## 范围

schema054 支持原生月、季、年服务的配置升降级。商品策略默认关闭；组与选项的 `upgrade_enabled` 可在商品配置编辑器维护。服务需已启用、未到期、完成导入复核且具有可核对的原配置快照。缺失快照拒绝报价，不根据当前目录编造历史价。

当前实现人工交付与显式模拟交付；schema055 增加复用同一账单/交付状态机的 [跨商品升级](service-product-changes.md)，schema057实现 [账期变更](service-billing-cycles.md)。升级优惠码/折扣、外部供应商/模块适配、已开始交付的变更撤销或逆向恢复、真实网关退款仍未完成。旧系统及数据只作业务参考。

## 接口

| 方法与路径（前缀 `/api/v1`） | 行为及权限 |
| --- | --- |
| `GET /services/{id}/upgrade-options` | 本人服务的变更商品目录，需商品已开放策略 |
| `POST /services/{id}/configuration-quotes` | 本人报价，提交 `option_ids`、`option_quantities`、`expected_version`、`expected_revision` |
| `POST /services/{id}/configuration-changes` | 本人提交 `quote_id` 及 8–128 字符 `Idempotency-Key`，生成账单 |
| `DELETE /services/{id}/configuration-changes/{change_id}` | 本人取消报价/未付款变更，同时取消未付款账单 |
| `PUT /admin/products/{id}/configuration-upgrade-policy` | `products.view/manage`，提交 `enabled`、`allow_downgrade`、`expected_revision` |
| `POST /admin/services/{id}/configuration-changes/{change_id}/retry` | `services.view/manage` 和客户范围；仅已付款、排队状态且交付任务失败时重试 |

付款沿用 `/invoices/{id}/pay` 或支付回调；人工确认沿用 `/admin/services/{id}/delivery`。服务详情返回有效配置、选项及数量、续费金额和最近 20 条已提交变更，管理员详情仍受客户范围限制。客户不能读取其他客户的变更目录、报价或付款；只读管理员无策略保存、交付或重试操作。

## 计价

只对变更组计算当前所选账期的周期增价差额，数量/范围/阶梯/父子分支和已实现的联动规则统一走购买校验。未变更组沿用服务合同快照，即使目录改价也不重新定价。更换选项还需原选项及新选项允许升级，新选项不能隐藏。关闭降配时，单组删除、降价、数量减少均拒绝，即使整单净差额为正。

变更后周期金额为原服务周期金额加各组差额；初装费不收取。按服务到期时间向前减一个日历账期，计算整期秒数；剩余秒数最多按整期计算。最终金额使用任意精度整数乘除，四舍五入到分，负差额对称舍入。正差额生成应付，负差额生成零金额账单并记录待返还余额。零金额账单仍需客户确认付款，付款本身不返还余额。

schema062的 [循环续费权益](recurring-promotions.md) 在交付后保留，下一次续费按变更后的周期原价减免；变更差价及降配返还仍按原价计算，报价的 `new_recurring_cents` 表示周期原价。服务详情分别显示周期原价与优惠后续费金额。

报价绑定商品 revision、服务 version、到期时间，十分钟有效；提交时重新检查版本、到期及互斥操作。提交后商品改价不改变此账单。报价记录尚无定期过期清理；报价接口有请求限额，定期清理仍待实现。

## 状态与事务

`quoted → unpaid → queued → waiting_manual → completed`；模拟模式从 `queued` 直接完成。`quoted/unpaid → cancelled`。付款只排队 `upgrade`，不改变服务配置。完成交付时在同一事务更新服务续费金额、有效配置/选择/数量、服务 version 和变更状态，降配还原子更新客户余额及唯一流水。历史订单与下单快照保持不变；后续续费使用有效服务周期金额。

`unpaid/queued/waiting_manual` 变更阻止创建或支付续费账单、其他服务动作、取消申请及原订单退款。已有任务、未支付续费、取消申请阻止报价和提交。服务行锁、账单锁、目录锁及幂等键锁负责序列化，数据库唯一索引确保每个服务最多一条待处理变更。报价提交与取消额外共享同一服务操作锁，在发现账单之前序列化 `quoted→unpaid` 转换，防止并发取消留下未关闭账单。

变更账单仅在任务未领取（`pending`、attempts=0）时可原子退款到余额，同时取消交付及变更。已开始或完成交付不能直接退款。管理员可重试失败任务，不重置 attempts，因此旧 worker 租约不能再次生效。重试事务持有角色管理共享锁并复核当前服务管理权限及客户范围。降配交付先锁客户余额，再锁服务，与财务锁顺序一致；余额锁仅关联指定任务及服务。

提交、付款及交付审计失败会回滚所有业务写入。最终金额、余额、原订单、任务与变更状态在集成测试中核对；人工交付证明需要管理员实际资源核验，模拟测试不证明供应商资源已变更。

## 验收与部署

2026-10-06：强制隔离 PG18 全量 Go 回归通过（最终 HTTP 81.676s、legacy-import 23.769s），权限/限制/审计回滚/并发专项通过（最终 HTTP 4.422s）。专项还覆盖八轮报价提交与报价取消竞态，均无可支付的孤立账单。Go vet、Next.js lint/typecheck 和 webpack 生产构建通过。浏览器 fixture 使用独立随机 schema，483.055s PASS；预览、浏览器和测试库均收尾清理。

Playwright 实际客户数量 2→5、报价、生成账单、余额付款、管理员人工确认、5→2 零金额付款及确认通过。周期价 2250→2535→2250 分，升配收取 142 分，降配交付仅一条流水返还 142 分，余额 97501→97643 分；原订单仍为数量 2、周期价 2250、初装费 107 分。只读管理员查看配置/历史通过，写接口返回 403；390×844 弹窗 x=16、width=358、height=439，无横向溢出。预览为 webpack dev；正式部署为通过构建的生产产物。

备份 `bin/previous/20261005T191700Z-configuration-upgrades/` 保存 schema053 dump、目录清单、四个旧二进制、前端压缩备份与实际 `.next`、SHA256、原业务摘要。独立 `_test` 库实际恢复053、旧API读取、升级054、新API读取及旧API在054上的只读兼容均通过；原业务摘要一致。此验证不等同于已有配置变更数据后的旧worker兼容或全站恢复演练。

开发实例已部署054，BUILD_ID `6d-p3isqqD7f1hKVyqHRQ`；报价提交/取消并发修正版替换前另备份 schema054 与四个二进制到 `bin/previous/20261005T192400Z-configuration-race-fix/`。三个项目服务 active，healthz/readyz正常，root实际登录/目录及服务读取/注销通过。部署前后原字段完整摘要一致（仅排除新产品策略及服务有效配置字段），2账户/4商品/2订单/2服务/2账单；新策略均 false、新服务配置字段均 null、变更表为空。没有向开发库写入测试业务或提取真实旧数据。证据位于 `output/playwright/configuration-upgrades-acceptance-20261006.md` 和 `output/configuration-upgrades-20261006/`。
