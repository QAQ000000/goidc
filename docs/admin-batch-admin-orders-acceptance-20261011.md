# 后台新建订单与订单操作验收（2026-10-11）

对照旧规范第8页产品订单、第99页新建订单，并补齐订单详情。迁移122/123已在隔离PG18验证后应用到开发实例；页面使用真实Go API，没有静态订单或模拟财务入账。

## 已交付

- `/admin/orders/new`：真实客户/商品搜索分页、多商品、配置选项/数量/购买资料、首次与续费价格覆盖、优惠码校验、付款偏好、订单确认、生成账单和发送邮件选择。报价绑定客户范围、商品版本、配置与购买字段定义、促销、渠道和续费权益；任何修改使报价失效。每单服务总数不超过100。
- 优惠码字段旁新增目录选择和“新增”入口：目录通过 `promos.view` 调用真实分页/搜索 API，可筛选启停状态并标出未开始、过期、停用和达到次数上限的记录；升级专用码不会出现在订单目录。创建弹窗要求 `promos.manage`，复用现有创建校验，支持折扣类型/值、使用上限、客户范围、单客户限制、账期、循环续费和备注。创建后自动选入订单；客户与商品齐全时自动重新报价，失败保留订单表单。报价仍由 Go 后端判断全部促销约束。
- 创建复用共享结算校验，不修改客户购物车。首次覆盖包括配置/初装费，不改变续费默认价；付款偏好不扣款，也不强制客户最终支付方式。订单确认只决定审核，未付款不会开通。
- 不生成账单时保留待支付订单和库存预留；详情以版本和幂等键生成待支付账单。发送邮件选择实际进入outbox或返回未验证收件地址/模板/渠道不可用等原因；未勾选不发送创建邮件。
- 订单列表补齐待核验页签、客户/上海日期/付款状态高级筛选、真实总数分页和URL恢复、IP、延期出账状态、新建入口、全选及批量取消/审核/删除逐项结果。
- 备注、取消、审核、删除、延期出账携带`expected_version`和`Idempotency-Key`；改变载荷重复使用key返回409。批量key绑定完整选择/版本/决策，每个订单独立事务，冲突逐项反馈。
- 删除使用审计软删除；订单从客户/后台普通列表及详情隐藏，账单、服务和财务引用保留。仅待支付或已取消/已退款且没有活跃资源的订单允许删除；未付款订单取消并返库存。在线支付仍待确认、运行中/待人工/等待核验任务、未释放资源阻止删除。
- 每笔管理员写事务重新检查并持有`orders.manage`授权和客户范围锁，幂等重放也不能绕过撤权。账单→订单→服务的锁顺序与付款保持一致。
- 创建草稿按用户隔离保存在sessionStorage，购买资料从不保存；重载后显式恢复，取消可丢弃草稿，409保留输入；详情备注按编辑开始版本提交，跨标签更新不静默覆盖草稿。

## API 契约

全部路径以`/api/v1`为前缀。列表读取`orders.view`；以下创建/写操作使用`orders.manage`和真实客户范围。

| API | 用途 |
|---|---|
| `GET /admin/orders/create-options` | 渠道/邮件可用状态；`kind=customers/products`返回`items/total/limit/offset`，客户只返回范围内活跃账号，商品不返回内部metadata |
| `GET /admin/promo-codes/page` | `promos.view`；搜索、启停筛选、`items/total/limit/offset`；`order_eligible=true`排除升级专用优惠；旧数组接口保留兼容 |
| `POST /admin/promo-codes` | `promos.manage`；创建优惠码，复用原有字段校验和审计；订单页创建成功会自动选中并触发报价 |
| `POST /admin/orders/quote` | 真实价格及版本快照报价，不预留库存、不创建订单 |
| `POST /admin/orders` | 提交`quote_hash`和幂等键，创建订单、服务、可选账单与通知任务 |
| `POST /admin/orders/{id}/invoice` | 以版本和key生成延期账单 |
| `POST /admin/orders/{id}/notes` | 内部备注，版本、key、审计 |
| `POST /admin/orders/{id}/cancel`、`POST /admin/orders/{id}/review` | 单个取消/审核 |
| `DELETE /admin/orders/{id}` | 版本、原因和key软删除 |
| `POST /admin/orders/bulk-cancel`、`POST /admin/orders/bulk-review`、`POST /admin/orders/bulk-delete` | `order_ids` + `expected_versions`，逐项处理 |

创建输入为`customer_id/items/payment_provider/channel_instance_id/confirmed/generate_invoice/send_email/notes/promo_code`。每个item包含`product_id/expected_revision/billing_cycle/quantity/option_ids/option_quantities/purchase_fields/first_price_override_cents/renewal_price_override_cents`；金额为整数分，空覆盖保留默认价格。

## 验证与证据

| 验证 | 结果 / 边界 |
|---|---|
| 隔离PG18全量Go回归 | 权限竞争修复前全量通过，httpapi 551.755s；修复后`scripts/go.sh test ./... -timeout 20m`再次全部通过，httpapi 527.403s |
| 优惠码分页 API | 本次独立PG18 `_test` 库覆盖搜索/备注匹配、启停过滤、分页元数据、排除升级专用码、非法筛选及缺少`promos.view`拒绝；专项通过，测试库已删除 |
| 本次全量Go回归 / vet | `REQUIRE_INTEGRATION=1 scripts/go.sh test ./... -timeout 20m` 使用独立PG18 `_test` 数据库通过，httpapi 650.057s；测试库自动删除。`scripts/go.sh vet ./...` 无诊断 |
| 创建/延期账单/共享结算回归 | 真实配置、库存、促销、客户范围、报价变更、幂等、审计和邮件入队通过 |
| 订单变更专项与竞态 | 并发备注/取消版本竞争、审计失败全部回滚、批量载荷冲突、库存单次释放通过 |
| 外部结果不确定保护 | 3个场景通过；`waiting_verification`阻止取消/删除，订单/账单/服务/库存/任务/receipt/审计完全不变 |
| HTTP旧权限之后撤权 | 7个真实并发场景通过；备注/取消/删除/审核/创建/报价/延期账单返回403，无业务写入 |
| Go race | 授权/变更/创建专项通过，38.155s；非全量race |
| Go vet | 全量通过 |
| Next typecheck / production build | 最终布局代码通过 |
| Next lint | 0错误、9条仓库既有warning；本轮组件无新增warning |
| 四尺寸 | 创建、列表、详情均完成1440/1024/390/320截图，document.scrollWidth等于视口宽度；表格内部滚动。人工抽查创建和列表1440/320及详情320，无遮挡 |

浏览器实际创建验收订单24，首次123分、续费456分、先不出账、不发送邮件；创建后详情保持`limit=50`。随后生成账单30且仍待付款；双标签更新备注，旧版本保存409并保留草稿。取消确认先拒绝，再确认；最后带原因软删除，返回保留页容量。数据库核实删除标记、订单/账单均cancelled、商品库存恢复4、保留1条服务、1条删除审计，没有付款或供应商开通写入。

最后一轮浏览器真实报价、修改数量使报价失效/提交禁用、beforeunload确认、草稿恢复后数量/价格保留但必须重新报价、取消确认拒绝/接受、返回列表、客户/日期/付款筛选URL恢复、批量取消确认拒绝均通过。运行期检查使用本地临时管理员会话，未修改账号密码；验收后撤销临时会话。

截图位于`output/playwright/admin-batch-order-create-20261011/`：`order-create-{width}.png`、`order-list-{width}.png`、`order-detail-{width}.png`、`notes-conflict-320.png`。截图不进入Git；SHA256写入浏览器证据JSON。

最终稳定阶段JS异常、控制台错误和API失败均为0。历史测试过程中包含预期的备注409、重启期间一次site 500；本次恢复会话尝试127.0.0.1登录被来源策略403、旧密码登录401，随后通过临时受限时长会话完成验收；这些不计为正常稳定页面通过，不能声称整个过程没有失败响应。批量成功/部分失败和权限拒绝由隔离HTTP回归验证，浏览器只验证确认拒绝，未对旧订单批量写入。邮件真实投递、支付/供应商链路本批未测试。

## 剩余对齐

- 第8页仍部分完成：客户资料备注投影与订单提成只读预估已接入真实 API；提成不是结算写接口，实际结算和真实邮件投递仍待专项验收。
- 第99页仍部分完成：目录选择/新增弹窗已通过真实PG18 API回归及 1440/1024/390/320 浏览器验收；四尺寸截图位于 `output/playwright/admin-ticket-create-20261011/promo-directory-{1440,1024,390,320}.png` 和 `promo-create-final-{1440,1024,390,320}.png`。创建优惠码后自动选入订单并重新报价，优惠从 ¥12.34 变为 ¥11.11；复杂多级商品逐项改选和真实订单邮件投递仍待专项验收。
- 浏览器验收期间发现 Chrome 新正则语法拒绝优惠码输入的字符类，已改为等价的显式分组表达式；修复后四尺寸 `pattern` 校验均有效、无横向溢出，最终控制台无错误。短屏 320×640 下两个弹窗使用 90dvh 高度上限并在弹窗内部滚动，滚动后提交按钮可见；截图为 `output/playwright/admin-ticket-create-20261011/promo-create-320x640-bottom.png`。
- 收尾强制全量回归：`REQUIRE_INTEGRATION=1 scripts/go.sh test ./... -count=1 -timeout 20m` 全部通过，PG18 `internal/httpapi` 用时 591.444 秒；`scripts/go.sh vet ./...`、最终 Next typecheck 与独立测试副本生产 build 通过。lint 0 错误、8 条既有 warning；Next CSS 优化器另对生成的 `animate-out` 规则报 1 条 warning，构建仍成功。临时 PG18 数据库已删除。
- 全部105页深度还原、正式切换和回滚仍未完成。

最终补记：权限竞争修复后强制隔离PG18全量Go回归全部通过（httpapi527.403s、命令退出0）；全量vet与最终前端typecheck/build通过。之前10分钟测试超时、一次测试字段名断言错误已修正并重新通过；收尾曾有测试进程被环境终止（143），最终重新运行退出0，不以被终止的日志作为通过证据。迁移123、API/worker/web active、readyz正常。

## 断点续作：客户备注与订单提成 API

- `/api/v1/admin/orders/page` 和 `/api/v1/admin/orders/{id}` 将 `customer_note` 投影为客户资料备注；订单自身 `notes` 仍作为独立管理员备注返回，不再混用。
- 新增只读 `POST /api/v1/admin/orders/commission`，请求 `{ "order_ids": [...] }`，每次最多100条且拒绝重复/非正 ID。响应含订单已付基数、销售归属、提成组采购基点、本月营业额阶梯基点、预估提成分及状态；客户范围外/不存在订单统一返回404。
- 接口路由要求 `orders.view`，同时处理器要求 `sales.view`；未付款、未出账、销售停用、无归属或无有效提成组均不产生预估金额。营业额及提成按调用人可访问客户范围统计，并基于当前规则实时计算，不创建结算记录、不触发付款。
- 订单列表显示“提成 / 销售”、当前页预估合计与非结算状态；无 `sales.view` 权限时只显示客户销售归属。
- 管理端在 `/admin` 布局加载共享权限上下文，页面可以在 `<Workspace>` 外安全读取当前管理员权限；此前提成栏因 hook 越过 Workspace 而隐藏的问题已修复。
- 隔离 PG18 专项覆盖提成基数/阶梯、未付款、无销售归属、双权限、客户范围、输入边界和空请求。订单操作批次的全量 Go 回归与 `go vet` 已通过；本次对最终前端源码重建隔离生产构建，Next.js TypeScript 检查和构建通过。

## 最终浏览器复验（2026-10-11）

Playwright 使用隔离 PostgreSQL 18 测试库中的三笔订单，对最终生产构建复验。订单列表与提成 API 均返回 HTTP 200，稳定页面控制台错误为0；页面显示客户资料备注、已付款订单 ¥3.00/¥1.20 预估以及未付款订单 ¥0.00。

| 视口宽度 | `document.scrollWidth` | 表格容器（可视 / 内容） | 结果 |
|---:|---:|---:|---|
| 1440 | 1440 | 1198 / 1613 | 页面无横向溢出，订单表在自身容器滚动 |
| 1024 | 1024 | 918 / 1613 | 页面无横向溢出，订单表在自身容器滚动 |
| 390 | 390 | 364 / 1613 | 页面无横向溢出，订单表在自身容器滚动 |
| 320 | 320 | 294 / 1613 | 页面无横向溢出，订单表在自身容器滚动 |

320px 下表格最大横向滚动距离为1319px，销售提成表头可以滚入视口；最右侧操作列也可达。这里使用共享 `Table` 的滚动容器，无需额外包一层横向滚动区。四尺寸截图及移动端提成列截图位于 `output/playwright/admin-order-commission-20261011/`：`orders-{1440,1024,390,320}-final.png`、`orders-320-commission.png`。截图只含隔离测试订单。
