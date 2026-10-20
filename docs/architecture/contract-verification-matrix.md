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
| 后台 API 与客户详情 | 全部生产注册和旧显式/动态声明是否准确区分？实际页面候选是否保留未知表达式？摘要是否按分区权限返回真实事实、工单部门和客户范围？现金与钱包是否分离，信用还款是否可追溯且不重复？客户日志是否按实体归属？精确客户筛选及创建选项是否拒绝非法/范围外编号且不回退全局？资源与分组分页是否保持同快照及范围，内部配置备注是否与公开购买隔离？附件上传未知是否绑定稳定业务意图、查询仍鉴权且不重Put？管理员续费是否锁后撤权并保持账号→凭据锁序？代登录能否派生长期密钥？ | [后台需求](../admin/api-requirements.md)、[API契约](../admin/api.md)、`scripts/test-admin-api.mjs --integration`、`scripts/test-file-storage.mjs --unit/--integration`、`scripts/test-captcha.mjs --integration`、`scripts/admin-api-inventory.test.mjs`、`scripts/admin-api-consumers.test.mjs` | E0盘点与E1独立PG18分开；需求组、候选请求和注册数均不作为业务完成率；UI接入、浏览器、外部通知与发布分别验收 |
| 编译模块与配置 | 二进制实际模块是否匹配 profile 声明且不读配置或联网？配置/凭据是否按 schema 校验并隔离？缺失实现能否安全停用？新增同类适配器能否复用运行链路？ | [模块契约与测试](../compiled-module-architecture.md#验证入口与发布边界)、`scripts/check-module-binaries.mjs`、`TestDefaultProfileMatchesCompiledModules`、`scripts/test-compiled-notifications.mjs` 的 unit/integration 严格入口、渠道组件测试 | 完整清单以 `audit/module-build-profile.json` 为准，不固定数量/位置；必需入口缺失或跳过须失败；清单不证明外部健康，组件事件不代表浏览器 |
| 验证码与认证 | 图片/点击/widget 是否经服务端验证？动作、浏览器、过期、并发和配置轮换是否拒绝绕过？登录/MFA 是否按物理客户/管理员实表与入口认证？跨表相同标识是否必须指定入口，省略时是否仅唯一匹配可登录？bootstrap 是否只建管理员，密码/MFA/API 密钥归属是否独立？ | [验证码接入契约](../auth-captcha.md)、`scripts/test-captcha.mjs --unit/--integration`、验证码组件测试 | 本轮严格独立 PG18 race 实际运行 82 项，全部通过且零跳过；schema182 登录入口与客户下单的独立库真实 Chrome 为既有验收，本轮未重跑；测试提供方不证明真实厂商，MFA 浏览器、SDK 和生产配置另验 |
| 订单与支付 | 金额是否以整数分计算？幂等是否绑定实例？原快照是否抵抗当前配置轮换/损坏？流水与退款商户是否隔离？外部未知是否继续冻结？ | `scripts/test-compiled-payments.mjs` 的 unit/integration 严格入口；`./scripts/test-all.sh`；[支付契约](../payments.md)与[开发契约](payment-module-development.md) | 第二协议仅测试装配，隔离 PG18 的真实 run/pass 不代表真实商户付款/到账；查询组件与浏览器分开验收 |
| 余额与发票 | 借贷、冻结、退款、发票版本和审计是否同事务提交？管理员代下单原键是否只产生一次库存/优惠/账单/通知副作用，撤权与客户范围是否仍阻止重放？ | `internal/store` 事务/幂等测试；`scripts/test-admin-orders.mjs --integration`；[交易契约](../transactions.md#管理员代下单与回执)；[发票](../invoice-applications.md) | 独立回环 PG18、严格 run/pass 零 skip；schema178 合成旧 writer 与冲突回滚不证明生产恢复或旧二进制降级；浏览器/外部渠道另验 |
| 应用文件存储 | 历史字节能否继续读取？五类文件是否保留原权限、Range 和审计？对象位置是否固定？未知上传、替换、级联删除与并发回收是否安全？ | [存储契约](file-storage.md)、`scripts/test-file-storage.mjs --unit` / `--integration`，真实 schema166→167 升级及本地 S3 协议模拟 | 必需 run/pass 零 skip；本地模拟不代表真实 S3/MinIO/R2、浏览器或生产切换；HTTP 仍有界整文件缓冲 |
| 服务生命周期 | 待审取消是否允许续费？批准后是否拒绝新续费及未付单的余额/信用/在线付款？未决真实支付是否阻审批？锁序、执行前权益与人工终止状态是否保守？ | [取消续费契约](../transactions.md#服务取消与续费互斥)、`scripts/test-service-cancellations.mjs --integration`；Go 业务回归、worker 恢复脚本、[服务变更](../service-configuration-changes.md) | 归现有 `transactions` 契约；独立 PG18、指定 run/pass 零 skip；本地回调/资源夹具不代表真实渠道、浏览器或已部署 |
| RBAC 与后台 | API 与页面是否执行同一角色、资源范围和撤权规则？`users/administrators` 是否分离且 principal 仅有不可变全局 ID/kind，展示视图是否只读无秘密且不参与认证？双 Cookie、身份查询和退出是否独立，后台登录时前台是否仍按客户/匿名显示，个人安全/通知、后台工单及显式代登录是否保留？管理员重置客户密码是否原子撤销旧会话、API 密钥及未完成 MFA，并在锁后复核授权？MFA 完成与换密是否统一账号→挑战锁序且不误消费恢复码？历史管理员工单列表、回复和通知是否兼容且不授予新客户资格？ | API 权限测试、`scripts/test-captcha.mjs --integration`、`TestAdminPasswordReset*`、`TestMFALoginSerializesPasswordRotation`、`TestTicketAccountUpgrade181PreservesHistoricalAdministratorTickets`、`scripts/customer-admin-isolation-components.test.mjs`、`scripts/admin-proxy-auth.test.mjs`、`node scripts/admin-page-contracts.mjs`、[RBAC](../admin-rbac.md) | schema182 既有独立库 Chrome 验证管理员前台匿名、跨入口拒绝、双端独立退出及客户下单，管理员无客户副本或商务归属；本轮最终代码严格 PG18 race 门禁 82 项全部通过且零跳过，覆盖重置撤权/审计回滚、MFA 并发锁序及历史工单真实升级；本轮浏览器、真实通知渠道及生产发布未重验，具体边界见 RBAC 契约 |
| 经营报表与评分 | 折扣分摊/期间去重是否守恒？预计与确认是否分离？评分是否按原身份一次写入？真实现金与费用是否可追溯且不越权？JSON 查询是否保留、CSV 等非 JSON 格式是否明确拒绝？ | [报表口径与测试入口](../credit-cash-reporting.md#回归入口)、[恢复契约](../report-restoration-contract.md)、`reports_regression_test.go`、`report_response_test.go`、`sales_report_details_test.go`、`ticket_ratings_test.go`、`annual_report_test.go`、`report_expenses_test.go`、`product_report_details_test.go` | 必须强制独立PG18；导入夹具不代表真实旧全量迁移，四尺寸浏览器与外部现金凭证另验 |
| 供应商与 NOKVM | 批量导入是否原子/幂等，所有支持账期与客户配置是否按订单冻结并真实开通？超时、重试、凭据、未知结果和人工复核是否安全？生命周期目标是否确认、付清恢复是否有可信系统暂停来源？无账单到期扫描是否不建单/扣款，写前核实远端权益且不误归因已有暂停？自动续费是否复核实际期限、可信来源及付款时的客户意图？付款后是否回读状态，且到期暂停的即时/延迟恢复不绕过人工暂停或终态？ | `scripts/test-compiled-suppliers.mjs` 的 unit/integration 严格入口、`TestSupplierRenewal*` / `TestSupplierExpiry*` 状态与来源回归、供应商组件测试、schema170→171→172 来源升级及 request guards、schema174→175→176 保留升级与 `TestSupplierCatalog*` / `TestSupplierConfigurable*` / `TestSupplierCalendar*` / `TestSupplierVolume*`、真实 worker 续费 SIGKILL、[接入契约](supplier-module-development.md)、[供应商协议](../supplier-protocol.md)、[NOKVM](../nokvm-delivery.md) | 模拟供应商不证明真实商户、配额、网络或生产权限；既有真实续费恢复不证明新增到期暂停分支或显式生命周期写；默认魔方三类生命周期写能力仍关闭 |
| 实名认证 | 实例和凭据是否固定到申请？未知提交是否只查？租约与新申请/人工审核能否阻止旧结果覆盖？通过后下单/API 密钥资格是否一致？锁序用例是否保留真实竞争且不被观察连接耗尽业务池？ | [实名认证契约](../domain/certification.md)、`scripts/test-compiled-certification.mjs --integration`、`TestCertificationProviderSettingsClaimLockOrder`、`TestCertificationProviderLeaseRecheckedAfterJobLockWait`、下单/API 密钥资格回归 | 观察使用有界独立直连，业务池固定四连接；测试提供方不进入生产装配；浏览器、真实厂商及回调协议独立验收，SQL 加速租约不证明实际恢复耗时 |
| 统一通知 | 渠道是否独立于事件？仅绑定 Telegram 能否收到实际业务转换和提醒？扩大付款订阅是否须主动确认？跨日补偿是否限 72 小时、固定去重且不追溯新订阅？付款/续费后新旧提醒是否失效？UTC 会话与旧提醒来源升级是否安全，旧日期保守兼容是否仅限无精确来源记录？退订/解绑是否阻止旧任务，未知结果是否停止重发？Web Push 是否按浏览器加密、会话失效即停止、410 仅撤销对应版本，锁屏是否隐私，push-only 是否仍有授权后的详情？ | [通知契约](../notification-architecture.md)、PG18 严格 integration 门禁、`TestNotificationLifecycle*`、`TestLifecycleBusiness*`、`TestNotificationCatchup*`、`TestNotificationLegacyDeadline*`、`TestNotificationLegacyBilling*`、`TestNotificationCenterProcessRecovery`、schema172→173→174 升级、`TestNotificationWebPush*`、`TestWebPushAPI*`、本地 TLS 解密/验签与通知组件/SW测试 | SIGKILL 是生产循环的测试子进程，HTTP 为本地模拟器、租约及历史时间由 SQL 控制；不证明实际停机补偿时长，真实回调及客户接收另验 |
| 迁移与切换 | 旧数据、密文、队列及 checksum 是否保留？169 是否只安全重开未派发 pending 冲突，保留原资金/任务/审计并使用新审批代次？歧义历史是否阻断升级？172 是否保持旧暂停与资金事实且不推断系统来源？173 是否保留旧去重与已发历史、废弃来源不明的旧提醒？182 是否保留 ID、金额、安全与历史业务证据，且不为管理员伪造客户或重新归属旧商务记录？ | `TestCancellationRenewalUpgradeFrom168`、`TestCancellationRenewalUpgradeRejectsAmbiguousHistory`、取消续费严格 runner；`TestNotificationCenterUpgradeFrom160`、`TestNotificationLifecycleUpgradeFrom162`、`TestSupplierExpiryUpgrade171PreservesHistoryAndDoesNotInferSource`、`TestSupplierExpiryRequestDatabaseGuards`、`TestNotificationLegacyDeadlineUpgrade172PreservesFacts`；`TestAccountSeparationUpgrade181PreservesHistory`、`TestAccountSeparationPrincipalsAndIndependentUniqueness`、`scripts/test-captcha.mjs --integration`；候选启动门禁、`cmd/migrate`、[迁移入口](../migration/README.md) | schema182 独立测试库升级、历史保留和严格认证已通过；生产库升级/回退仍待实际执行。仓库 183 个 SQL 迁移文件、最大编号 182 不代表业务库已升级。先停旧 writer，迁移/API/worker/Web 成套发布，不可在新 schema 直接降旧 binary；合成旧夹具和正向升级不证明生产备份恢复、正式切换或旧二进制降级 |
| 前端与 API | 加载、空、错误、冲突、响应式和错误码是否可见且一致？目录是否只展示摘要并进入独立配置详情，配置/资料/多件购买与购物车编辑、报价及订单快照是否一致？业务文件下载是否保留同源认证、错误语义、失效响应检查及临时资源清理？ | `npm --prefix web run lint`、`typecheck`、`test:proxy`、组件测试、`scripts/product-purchase-components.test.mjs`、`TestProductPurchaseBrowserFixture`、`node --test scripts/download.test.mjs`；数据报表不再承担文件下载 | lint/typecheck 与 DOM/fetch 夹具不等于浏览器或真实 API 验收 |
| 站点主题 | 匿名接口是否只公开展示字段？保存后 SSR/客户端能否消费？亮色主色可读、页脚转义和后台隔离是否保持？ | [主题契约](../admin-configuration.md#官网主题)、`TestSiteContentVersionedCRUDAndPermissions`、`TestSiteThemeValidationPermissionsAndReplay`、`node --test scripts/site-theme.test.mjs` | API 写入和权限使用隔离 PG18；渲染夹具不代替实际浏览器视觉/Portal/刷新验证 |
| 内部调度 | 任务目录、API 筛选与页面名称是否一致？既有循环频率和顺序是否保留？审计失败会否重跑？日续费是否保留锁和全量完成标记？ | [调度契约](scheduler.md)、`scripts/test-scheduler.mjs --unit` / `--integration`、调度组件测试 | 必需 run/pass 零 skip；独立 PG18 和组件夹具不代表真实 worker 进程、浏览器或生产调度 |
| worker 与追踪 | 请求错误、审计与异步重试能否按来源串联？worker 缺失是否保留 API 启动语义？真实循环是否暴露卡住、未来任务与近期失败？ | [运行诊断契约](runtime-observability.md)、`scripts/test-runtime-observability.mjs --unit` / `--integration`、运行诊断组件与代理测试；`./scripts/test-worker-recovery.sh`、[容量验证](../capacity.md) | 强制独立 PG18、零 skip，真实闲置 worker 分钟轮询与 TERM 验证；时间加速与组件不证明真实告警耗时、浏览器、生产网络开销或支付容量 |

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
