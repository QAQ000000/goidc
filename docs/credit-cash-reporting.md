# 经营报表与现金口径

兼容业务消费报表、销售收入和仪表盘经营收入使用两类来源：未使用信用的已支付业务账单（按 paid_at、排除充值账单），以及信用还款凭证中的真实金额（按 received_at）。它包含余额消费，不能统称全部外部现金；信用消费本身不算收款，部分还款计实际到账，完全还款状态不重复计整张账单金额。新增年度现金报表按独立外部收退款事实统计，详见下文。全部客户来源执行当前管理员客户范围。

## 账单数与期间

收入报表每月的 `invoice_count` 按该月 `DISTINCT invoice_id` 统计；`total.invoice_count` 独立按完整查询期间去重，默认全年，指定 `month` 时只覆盖该月。同一账单同月多笔到账只算一张，跨月到账可分别出现在各月，但期间总数仍只算一张；期间金额仍是全部实际到账之和。月度与期间统计在同一 SQL 快照中完成。

每日运营报表的查询边界、日期序列和数据分桶统一按 `Asia/Shanghai`，起点包含、终点不包含。数据库会话为 UTC 或其他时区时，仍返回完整上海自然月，包含月末，排除下月首日。

## 排名、工单权限与空值

客户收入排名只包含当前管理员客户范围内、状态为 `active` 且确有余额流水的客户，按全历史净余额流水降序、客户 ID 升序取前十名。没有流水的客户不以零额补入；有流水但净额为零或负数的客户仍有资格进入排名。此排名不接受年份或月份改变统计期间，页面和导出都明确标为“全历史前十名”。余额事件中的收入、支出不代表公司的经营利润。

六类报表及 CSV 均要求 `reports.view`；收入、销售、产品和排名执行当前客户范围。每日运营的商业指标也执行客户范围，工单指标另要求 `tickets.view` 与当前部门范围：无工单权限时仍返回其他每日指标，`ticket_metrics_available=false`，每日 `new_tickets/ticket_replies` 为 `null`，不能用零表示无权查看。有权限而无事件才返回零。

独立工单统计接口无 `tickets.view` 时返回 `403 permission_denied`，不返回伪装为空表的成功响应。有权限时按上海创建日统计，`ticket_count` 保留合并来源；`open_count` 仅计其中未合并且当前 `open/pending` 的工单。工单部门范围独立于客户销售范围，详见[RBAC](admin-rbac.md)。

## 产品新购净额

普通非信用新购的 `new_order_amount_cents` 使用折后账单金额。`order_items.total_cents` 继续保存购买时毛额，订单保存优惠快照；报表按完整订单的商品行毛额比例分摊账单净额，再按产品汇总，不修改历史交易数据或按当前优惠规则重算。

分摊沿用[返佣](affiliate-core.md)的累计比例口径：按 `order_items.id` 排序，取相邻累计毛额对应的净额向下取整之差作为行净额，计算使用 `numeric` 和 `div` 整数商防止乘法溢出及除法舍入。分币归属稳定，各行合计等于可归属净额；可归属净额为账单金额与订单行毛额合计的较小值。零毛额行计零，整单全免仍保留订单数量；同一产品多行时订单数仍按订单去重。续费继续使用账单项目净额。

产品收入只将单一发票项目、自动服务/续费账单且服务客户匹配的信用到账归属产品；手工、混合、无服务关联订单不估算分摊，其实际到账仍计入总收入和销售收入。产品报告因此可能低于总现金收入；未做新的产品分摊账本。

余额流水排名依旧反映余额充值/流水，不将信用消费当入账；本轮只收紧有流水的入榜条件，不改变余额指标定义。客户等级的购买指标不转换为现金指标。还款记录属于真实外部到账，后续信用退款须独立处理，不使用整账单 paid/refunded 状态抹除已收到的现金；本轮未引入还款现金撤销流程。

## CSV 导出

在下列现有 GET 接口上指定 `format=csv`，省略或指定 `format=json` 保持 JSON；其他格式返回 `400 invalid_report_format`。CSV 复用同一请求中已经授权的报表结果，保持当前已查询期间、资源范围、排序和结果上限，不能绕过权限重新查询全库。

| 类型 | API（省略 `/api/v1` 前缀） | 当前导出范围 |
| --- | --- | --- |
| 收入 | `/admin/reports/income` | 查询期间的月度行及期间合计，账单总数独立去重；页面按所选年查询 |
| 年度现金 | `/admin/reports/annual-cash` | 所选年与上年各12个月、年度合计和差额；真实/模拟/待核验收款分列，全局费用不可见时写“无权限” |
| 每日运营 | `/admin/reports/new-clients` | 所选上海自然月全部日期；无权查看的工单字段写“无权限” |
| 客户排名 | `/admin/reports/customer-ranking` | 当前客户范围的全历史前十名，不受年月筛选影响 |
| 产品收入 | `/admin/reports/product-income` | 所选年月的产品新购净额、续费金额及现有订单/账单数量 |
| 分类产品收入 | `/admin/reports/product-income/groups` | 完整筛选内全部分类下的产品；CSV不受界面分类页码限制 |
| 销售汇总兼容接口 | `/admin/reports/sales` | 当前查询期间的销售归属汇总 |
| 销售业绩明细 | `/admin/reports/sales/details` | 当前筛选及账单分页内的展开子项；净额、预计与历史有效确认分列，预计规则和历史确认口径见[恢复契约](report-restoration-contract.md#金额提成与归属) |
| 工单统计 | `/admin/reports/tickets` | 所选上海自然月的创建量和当前待处理量，保留工单权限及部门范围 |
| 工单处理人 | `/admin/reports/tickets/handlers` | 当前期间、处理人、排序和分页，公开管理员消息评分按消息创建时间归期 |

CSV 使用 UTF-8 BOM 与中文列名，所有金额列明确标注“分”，保持整数分而不经浮点转换；名称等文本转义电子表格公式前缀。空报表保留表头，收入报表另保留零额合计。响应为 `text/csv` 附件，使用 `private, no-store`；导出失败返回错误，不伪装为空 CSV。

前端只加载当前标签，加载失败与重试只影响当前报表；其他报表接口失败不阻塞当前内容。导出使用已查询条件，筛选草稿变更后须先查询；未加载、加载失败或正在导出时按钮禁用。失败保留可重试错误，类别或期间切换后旧导出不得触发下载，详见[前端契约](admin-frontend-design.md#71-报表独立加载与导出)。

## 本轮已补代码与边界

| 项目 | 当前事实 |
| --- | --- |
| 产品净额 | 完整订单按行 ID 累计整数商分摊，防止优惠后收入高估；bigint 边界保持分币守恒 |
| 期间账单数 | 月内与全年各自去重；跨月还款不累加为全年多张账单 |
| 每日运营 | 上海日期序列与分桶统一；无工单权限为 `null`，有权无事件为零 |
| 排名与导出 | 有余额流水的活跃客户才入全历史Top10；CSV与JSON复用权限和筛选范围 |
| 销售明细 | 当前可见范围、人员/高级筛选、账单分页展开、对比与补零趋势；当前预计和历史确认分列，原确认快照不重算 |
| 工单处理人 | 当前处理人/工单创建期间、消息原作者/消息创建期间；关闭不含resolved，保留合并历史与当前部门范围 |
| 客户消息评分 | 原生公开消息身份、一次评分与幂等审计；旧star保留来源，缺失评分时间为null，不用当前角色倒推历史身份 |
| 年度现金与费用 | 真实/模拟/待核验收款分列，12月和上年比较；只追加费用及撤销、来源凭据唯一、全局范围和finance.manage边界 |
| 产品分类与数量 | 复用独立分类，迁移160和旧gid映射；分组分页、全筛选汇总、件数/项目数与订单数分别展示 |

年度 `received_cents` 为真实网关到账、信用还款与已核验迟到到账之和；`unverified_anomaly_cents`、`simulated_received_cents/simulated_refund_cents` 单列，不计真实现金净额。退款按凭证发生月进入 `refund_cents`；人工费用只计有效登记，撤销重述原发生月。`business_consumption_cents` 独立展示，充值与余额消费不能双计收款。全部客户范围之外的岗位无权看全局费用，`manual_expense_cents/outgoing_cents/net_cents=null`；新 `finance.manage` 权限不能由 `reports.view` 隐式获得。

产品金额是已成交口径，独立确认退款列在年度现金支出，件数保留购买事实；不是扣退后的净销售额。销售历史确认表示所选账单的历史快照，不能称期间新增确认，跨月应完整期间查询去重。详细来源、当前规则预计阶梯、旧评分与分类映射见[恢复契约](report-restoration-contract.md)。

上述为代码恢复事实。隔离PG18最终 `-race` 回归32个顶层测试通过、0跳过；浏览器另用隔离Next+Chrome+受控Node模拟API完成43条显式断言，4个恢复页×6宽度共24组均无页面水平溢出，年度/销售图无NaN，320宽费用弹窗和评分可操作。费用未知结果刷新保留原摘要/body/key并成功清空pending；评分恢复原星级、重试保留原body/key。证据见 `output/playwright/report-acceptance/summary.json` 和 `SUMMARY.md`。浏览器未连接真实Go API，不能与独立PG结果拼接称Go浏览器全链路；真实旧全量迁移/对账与外部凭证核验仍未验收，台账继续部分完成。

## 回归入口

现金与信用范围对应 `internal/httpapi/credit_cash_reports_test.go`；产品净额、期间账单去重和 UTC 会话月界对应 `internal/httpapi/reports_regression_test.go`；排名入榜条件、工单指标空值和撤权对应 `internal/httpapi/reports_permissions_test.go`；合并来源对应 `internal/httpapi/reports_extra_test.go`；六类CSV字段/整数分/公式转义和权限范围对应 `internal/httpapi/report_export_test.go`。

销售金额、信用跨月、历史确认与范围对应 `internal/httpapi/sales_report_details_test.go`；工单身份、一次评分/并发/审计和处理人统计对应 `internal/httpapi/ticket_ratings_test.go`；年度现金/退款发生月与费用权限/只追加/幂等对应 `internal/httpapi/annual_report_test.go`、`internal/httpapi/report_expenses_test.go`；分类分页/完整汇总/件数对应 `internal/httpapi/product_report_details_test.go`。旧分类与评分投影对应 `cmd/legacy-import/product_categories_test.go`、`cmd/legacy-import/tickets_test.go`；导入全包夹具通过不能替代真实旧全量迁移。

运行数据库回归必须设置 `REQUIRE_INTEGRATION=1` 和指向独立 PostgreSQL 18 `_test` 数据库的 `ZJMFGO_TEST_DATABASE_URL`；无 DSN 的跳过结果不算数据库验收。本轮报表综合及32项竞态回归、销售/分类数量专项与导入全包通过；模拟API浏览器证据单列于[恢复契约](report-restoration-contract.md#本轮已执行边界)，不能由这些结果推定真实全量旧数据或生产链路通过。
