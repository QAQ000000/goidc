# 本地未消费续费退款与精确冲正验收

日期：2026-10-17。产品实现基线 `5179259`；独立增量复核已集成 `7f4a23c`，并发回调修复为 `c6d2314`。当前状态：专项、恢复、浏览器及最终强制隔离PG18全量已通过；历史失败逐轮保留，G03整体仍未关闭。迁移155仅应用到隔离 PG18，不部署或写入业务运行库。

## 本批范围

契约见[本地续费退款设计](local-renewal-refund-contract-20261017.md)。只对具有不可变生效事实、原期限尚未到期、新续期尚未开始且无后续依赖的本地整期续费进行全额冲正。manual、simulation、nokvm 使用同一精确逆操作；supplier 保持独立路径，不推测上游权益。

真实结算在同一事务记录实际 before_due / period_start / after_due 和优惠次数。外部退款登记只记录已经核实的外部出款，持久冻结服务；独立员工确认才精确恢复 before_due / before_used，关闭自动续费并增加 generation。原支付 succeeded、paid_at、订单、服务及库存保留，客户余额不变。驳回只释放冻结，期限、次数和自动续费保持原样。既有余额、零额和未还信用续费退款复用同一逆操作，各自保留资金账本语义。

新增续期上下文 API 和 `external_refunds.renewal_reverse` 权限；登记绑定原 invoice/effect/service 版本，确认和驳回均验证独立员工、会话、权限及范围。effect、reversal 和 hold 在数据库层保护身份、快照与状态；关联写入通过服务锁串行，反向锁使用 NOWAIT 稳定返回可重试冲突。

## 自动化验证

证据目录：[本批日志](evidence/local-renewal-refund-20261017/)。测试仅连接 `127.0.0.1:55439` 的 `_test` 数据库，逐例创建随机 schema。恢复目标为独立 `_test` 数据库；没有连接业务运行库。

| 检查 | 实测结论 |
| --- | --- |
| Go build / vet | 最终产品代码通过，命令退出0；空日志分别为 `go-build.log` / `go-vet.log` |
| 154→155升级 | PASS 4.075秒；真实154 schema中的合成历史续费保持原业务事实，不回填 effect；旧退款保守拒绝，升级后真实付款形成 effect；见 `upgrade.log` |
| PG dump / restore | PASS 12.136秒；恢复 pending hold、完整快照、重复迁移、直接SQL拒绝、独立确认、请求重放与晚回调；见 `restore.log`。archive SHA-256：`e4e4c5e71910f36db6199fe8fe7667bea0d73eef7207dcec1b302c7c58633b26` |
| store专项 race | PASS 180.236秒；本地续期、独立SQL审查、升级和恢复专项；含月底/折扣/多周期、LIFO、截止与依赖、generation、关联与锁序、审计失败回滚。见 `store-race.log` |
| 首次 effect 竞争 race | 主线 `7f4a23c` PASS 4.092秒；未提交任务不能被首次真实结算越过，见 `first-effect-race.log` |
| HTTP专项 race | PASS 150.565秒；真实签名收款、登记/独立确认/驳回、并发、权限及8组实际数据库锁等待后撤权/范围撤销；见 `http-race.log`。默认 SKIP 的浏览器 opt-in 不计此项通过 |
| 前端 | typecheck、lint、独立 webpack build 退出0；lint为0错误、7个既有warning。见 `web-typecheck.log` / `web-lint.log` / `web-build.log`；预览只代理隔离API28089 |
| 独立最终复核 | [固定实现增量审查](local-renewal-refund-independent-review-20261017.md)未发现新增阻断；真实PG两个定向用例通过，静态UI结论与浏览器结论分列 |
| 强制隔离PG18全量 | 两个重叠轮次均失败；已修夹具轮达到原45分钟上限。唯一串行轮退出1（HTTP2069.738秒、store272.985秒）：仅供应商并发续费 `auto_renew_disabled` 断言仍要求旧`pgx.ErrNoRows`，实际已正确拒绝为`auto_renewal_race`。`56d07ac`仅修测试并增强零账单/零审计/开关保持关闭断言，专项PG/race通过；快照`f6e8cb4`的下一唯一全量也退出1（HTTP1659.871秒、store229.621秒，未超时）：供应商断言通过，但`TestEpayPendingGuardsAndServiceSettlement/upgrade`并发回调出现一次500。见`serial-fixed-full-suite.log`及`serial-fixed-full-suite-result.json`；该轮失败保留；修复及最终通过结果见下方追加记录 |

最终追加：`c6d2314` 将回调原有 invoice 行锁前移到第一次 payment 更新前，解决普通取消锁竞争被154关联保护误判为退款冻结的问题；154/155及真实hold保护未改，见[复现及修复证据](evidence/epay-upgrade-callback-20261017/README.md)。上述表格中的旧失败是历史记录，最终结果如下：

| 最终固定源码 `c6d2314` | 实测结论 |
| --- | --- |
| 强制隔离PG18全量 | `scripts/test-all.sh` 退出0；HTTP1653.256秒、store237.273秒，其余包通过或无测试。原45分钟包时限未改；见 `epay-upgrade-callback-20261017/integrated-full/` |
| 五项组合race | SMTP三种可选字段、确定性取消/回调竞争、source API→Node、两个真实worker通知/模拟开通小链、真实退款hold拒绝均实际PASS，无SKIP；HTTP15.543秒、store2.637秒，见 `integrated-race/` |
| Go build / vet / diff | 三命令退出0，见 `integrated-static/` |
| 环境与清理 | 三轮源码固定，schema集合恢复；常驻API/worker/Web身份未变，前后API/Web均200；各 `result.json` 独立记录 |

完整回归的默认opt-in入口不计专项执行，五项实际运行以race日志为准。上述全量只证明固定产品快照，不追认后来新增G09工具；本批无前端新增修改，沿用此前对应源码的浏览器及前端检查证据。

恢复归档摘要以 `restore.log` 内实际记录为准；代表性隔离夹具不等同于正式业务快照或真实资金到账。

较早全量在修正历史夹具前启动，最终自然退出1：旧JobIdentity、153升级快照及HTTP锁等待撤权夹具失败，HTTP包总计2700.038秒后达到45分钟上限。超时发生时当前查单审计回滚用例只运行1秒，不据此认定该例卡死。夹具修正后的相关专项与race已通过，但不能替代最终全量。两轮重叠使同集群逐例迁移争用，已修夹具的轮次也达到原包时限；串行轮需看实际终态，不能自动提高超时掩盖结果。

## 真实 API 浏览器验证

使用 Playwright CLI、独立 API28089 / Web3089、两套员工会话和真实隔离 PG18；不是页面静态假数据。截图、日志和公开数据库投影在 `output/playwright/local-renewal-refund-20261017/`；[SHA-256 索引](generated/local-renewal-refund-browser-acceptance-20261017.json)登记176个文件、75张截图（包括早期尝试，不把全部截图数量当作通过项数），不收录私有 cookie、fixture manifest 或认证状态文件。公开数据库投影另保存在本批日志目录。

| 场景 | 实测结果与证据 |
| --- | --- |
| 四宽布局 | 1440 / 1024 / 390 / 320；资格、创建、审核、未知恢复、已冲正、只读、缺权限、409、驳回释放和分页截图均保留，文档横向溢出为false。创建与审核四宽有弹窗边界几何日志 |
| 320×568短屏 | 最终 `create-stable-top/bottom-320-568.png`，弹窗 x16 / y16 / w288 / h536，上下可滚动，底部两个按钮可见；主代理实际查看顶部/底部。早期 `short-*` 在resize过渡中拍摄，几何越界，不作为稳定布局通过证据 |
| 必填与取消 | 空表单显示校验，无提交；已填写草稿取消需确认，再次打开为空；`create-validation.log`、取消及新草稿快照 |
| 未知响应及刷新 | 拦截 invoice2 POST：先由真实API成功登记再丢浏览器响应。页面冻结新登记，刷新后原请求SHA仍为 `c0396f09decd66f11f37d1e881aa73e09d25173b1f6b919fcc4c4c5c0a189f43`；以原键/原body核实仅产生退款#1。见 `unknown-original-request-before/after.log`、`unknown-replay-confirmed.log` |
| 独立确认 | 申请人无自审入口；另一员工确认#1，hold applied、账单refunded。服务1精确恢复11月17日原due、优惠used0、auto=false、generation1；原收款、paid_at和余额不变。见 `review-applied.log`、`db-after-confirm.json` |
| 权限拒绝 | 无专用权限员工不能登记，浏览器认证真实POST返回403 permission_denied；只读员工无操作按钮，仍可读持久状态。见 `no-reverse-api-403.log`、`readonly-widths.log` |
| 真实版本冲突 | invoice3草稿绑定service2 v4，隔离库仅把version改为5，真实POST返回409。页面保留body/key/v4，读取最新事实显示v5，明确结束被拒绝原请求后才新建草稿；未自动换键。见 `conflict-result.log`、`conflict-latest-facts.log`、`conflict-ended.log`、`new-version-draft.log` |
| 独立驳回 | 第二申请人与第二审核人处理#2，released且invoice3仍paid。原续后due、used1、auto=true、generation0不变；余额、原支付、订单和库存保留。见 `rejected.log`、`db-after-reject.json` |
| 加载和错误恢复 | 页面加载骨架实际可见，见 `loading-verified.log`；注入资格GET503后显示错误并禁止登记，移除故障点击重新加载恢复真实资格，见 `error-verified.log` / `error-recovered.log`。故障注入不证明API原本失败 |
| 分页与URL | 真实API额外登记并由独立员工驳回11次，总12条，每条独立审计。第二页URL为 `?refund_offset=10`，刷新后仍第2页，再返回真实账单列表；见 `pagination-seed.json`、`page2-reloaded.log`、`page2-widths.log`、`back-to-list.log` |

错误边界：验收主动制造了丢响应、403、409和503，不能宣称零API错误。早期CLI对加载骨架使用文字定位、限一次路由被身份重建取消，以及不支持的network命令属于工具验收失败尝试，保留原日志；最终分别使用ARIA加载状态、持续故障路由和requests命令复验。最终控制台与请求列表另行记录：requester保留1条预期409资源错误，reviewer最后一次导航后0错误；之前503资源错误另有原始日志。没有观察到JavaScript运行异常，但最后一次导航后的空控制台不代表整个验收从未出现过预期资源错误。

数据库最终投影保留成功收款和paid_at，余额97834分未变；服务1冲正一次，服务2全部驳回不冲正；订单保留、库存18、两服务inventory_released=false。最初基线未记录订单和库存逐字段快照，因此后两项只断言最终保留事实，不虚称有完整前后逐字段比较。

浏览器验收后fixture以finish文件正常退出，opt-in测试PASS（2399.50秒）、退出0；专属schema已不存在、28089无监听。预览Web3089已TERM关闭，隔离 `.next` 释放435400704字节；常驻API608393、worker608413、Web1647696保留，API/Web健康检查200。证据为 `browser-fixture-cleanup.json` / `browser-web-cleanup.json`，未停止共享测试PG。

## 未关闭范围

已消费续期、部分退款、供应商权益、升级、佣金、多方混合资金、已还信用、自动原路退款和真实付款人到账仍待补齐或独立验收。旧历史缺effect不回填、不猜测。本批不证明NOKVM真实母机、魔方上游退款或生产切流通过。G03继续开放，105页仍45完成 / 60部分，总体目标保持进行中。
