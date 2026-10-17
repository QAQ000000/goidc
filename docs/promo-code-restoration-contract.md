# 优惠码页面恢复契约（旧规范 #36）

状态：已实现并通过独立验收，见[最终记录](archive/README.md#f-11554f6d96b2)。以下保留开发前事实基线 `b61949d`、最小关闭契约与原验收方案；“当前能力/未执行”描述均属于当时快照，实际结果以最终记录为准。

## 范围与依据

旧规范 `/www/wwwroot/zjmf-manger-decoded/docs/frontend/admin-full-ui-interaction-specification.md:765` 的 #36 优惠码（旧路由 `#/promo-code`）要求搜索、分页、新增及表格管理。九列表格为 ID、优惠码、类型、价值、循环优惠、已使用/最大次数、开始时间、失效时间、操作，并呈现激活、过期、全部优惠码。

Go 页面入口为 `web/src/app/admin/promo-codes/page.tsx`，当前实现在 `web/src/components/data-pages.tsx` 的 `PromoCodesView`。最小范围只关闭该页面的日期、状态、搜索分页与保存取消闭环，不扩展其他域，不重写折扣计算，不增加删除或修改已发优惠经济条款。

## 当前能力与缺口（静态核对）

- `internal/store/promos.go` 已在使用优惠时检查启停、未开始、过期、耗尽和适用条件；页面只按 active 布尔过滤，不能说明优惠实际有效。
- `internal/httpapi/promos.go` 已有 `GET /api/v1/admin/promo-codes/page`，返回 items/total/limit/offset，支持 q 搜索代码或备注、active 筛选及 order_eligible。页面仍读旧数组接口，无搜索分页。
- POST 已接收 starts_at/expires_at，校验 RFC3339 与完整输入起止关系；页面无日期输入和日期列。
- PATCH 已接收时间，但 COALESCE 使清空无效，且缺合并后起止校验、版本条件与幂等重放。当前直接后写覆盖。
- 页面现有内联新增和即时启停，无编辑草稿与取消。POST/PATCH 未读取幂等键；code 唯一约束只能防重复行，不能替代重放契约。
- `internal/httpapi/admin_permissions.go` 已将 GET 映射到 promos.view，将 POST/PATCH 映射到 promos.manage；前端已有 PermissionGate。静态存在不代表运行权限验收通过。

## 最小关闭契约

列表复用分页端点，旧数组接口保留兼容。保留 active=all/active/inactive 的既有布尔语义及 order_eligible 参数，避免破坏订单选择器；新增独立 status 参数和服务器计算状态，不把 active=true 改解释为有效。有效条件为启用、已开始、未失效且次数未耗尽；失效边界为 expires_at <= now，未开始为 starts_at > now，另明确停用/耗尽。建议状态优先级为过期、停用、未开始、耗尽、有效，全部/有效/过期标签与其他状态筛选共用该定义。服务器时间或状态字段避免浏览器时钟差造成分歧。

列表呈现旧规范九列；循环优惠说明每件服务续费次数或不限，最大次数0显示不限，空开始/失效时间显示不限，保留升级专用含义。搜索、状态、页大小变化回第一页，翻页展示总数，加载/失败/空数据与重试明确，越界页可恢复。

新增/编辑使用草稿表单，保存/取消明确；取消不发写请求。提交中禁用重复提交及关闭，失败保留草稿，成功刷新服务器数据并显示反馈。日期控件明确本地时区，提交RFC3339，显示和往返需一致。编辑只允许现API范围：启停、开始/失效、次数上限及备注；优惠码、类型、价值和适用条件维持不可编辑。

PATCH 必须区分字段未提交与显式清空：未提交保留，空值清除日期。先加载当前记录，合并全部提交字段，再校验合并后 expires_at > starts_at（两者均非空时）；单字段更新也必须校验。加入 version/expected_version，过期版本返回409并保留草稿，提供重新加载而不自动覆盖。新migration补版本及必要日期约束，不改历史migration。

POST/PATCH采用工程既有幂等机制，同次用户操作失败重试保持同键；同键同payload返回原结果且不重复审计，同键不同payload返回409。唯一代码、版本冲突、幂等冲突与非法日期需可区分提示。不得仅用随机新键和busy按钮声称实现幂等。

只读用户（promos.view）可读取搜索分页及日期状态，不显示写控件；管理用户仍需正确页面读取权限和promos.manage写权限。无权限、只读直接写API、会话中撤权均由服务端拒绝，不能只依赖隐藏按钮。

## 实施定位与复用

- `web/src/components/data-pages.tsx`：PromoCodesView切分页、日期、状态和草稿交互，可按需要拆独立组件。
- `web/src/lib/api.ts`：PromoCode已有日期字段；补分页、状态和版本类型。
- `web/src/components/admin-pagination.tsx` 与现有 AdminStatusTabs、PermissionGate、Loading、Failure：复用分页、权限、状态与失败模式。
- `internal/httpapi/promos.go`：分页扩展status但保持active兼容，POST/PATCH补幂等、版本、clear及merged校验。
- `internal/httpapi/server.go`、`internal/httpapi/admin_permissions.go`：复用既有路由与权限，不增权限域。
- `internal/store/promos.go`、`db/migrations/020_promo_codes.sql`：复用时间列、索引与消费校验语义，不重写优惠计算；新增migration处理版本和约束。
- `internal/httpapi/promo_codes_page_test.go`：扩展现搜索分页及权限测试；相关回归选择 recurring_promos_test.go、upgrade_promotions_test.go，不扩大到全量。

## 验收方案（全部未执行）

API/PG需在独立测试schema验证：过去/未来/空时间、恰等于失效时间、耗尽与停用区别；搜索跨页、页大小、零结果及active旧参数兼容；日期清空、只改一个日期造成合并后非法；双编辑者版本409；幂等重放/冲突、唯一审计；无权限/只读/管理/撤权。

浏览器四尺寸为1440、1024、390、320像素宽，分别核对可读表格/横向滚动、搜索分页、全部/有效/过期及状态、日期时区往返、新增保存/取消、编辑清空日期、409保留草稿后重新加载、失败重试不重复新增，以及只读无写入口。补loading/error/empty、键盘焦点与确认/取消可达性证据。每项记录PASS/FAIL/N/A/未验证及证据，不能用lint/typecheck/build替代浏览器行为验收。

本分析仅静态阅读；以上API/PG、四尺寸浏览器、并发及幂等场景均未执行。开发时先运行定向测试；完成批次须按核心目标执行 scripts/test-all.sh 强制隔离PG18全量、相关race、Go build/vet及前端typecheck/lint/隔离build，并附四尺寸运行证据。

## 独立记录：scheduler跨事务网络风险

此项属于供应商续费可靠性，不纳入优惠码页面改动。`internal/store/auto_renew.go` 的 RunDailyAutoRenewals 开启事务、持transaction advisory lock，在提交前调用ProcessAutoRenewals；后者通过CreateAutoRenewalInvoice进入 `internal/store/renewals.go` 的上游QuoteRenewal。因而外层调度事务及锁确实跨越网络请求，不能声称所有外部调用都在事务外。

该外层事务承担调度串行化，并非等同远端请求持有同一业务行锁。风险是长期占连接、idle-in-transaction及advisory锁、慢网络/多候选/小连接池时推迟其他worker接管。连接断开自动释放锁，账单/支付稳定键仍承担恢复防重。既有进程验收证明功能和防重，不证明慢网络下连接池健康。后续独立可靠性批次可研究专用连接session advisory lock或带fencing的租约；不能简单提前提交释放锁而破坏并发语义，本优惠码批次不修复。
