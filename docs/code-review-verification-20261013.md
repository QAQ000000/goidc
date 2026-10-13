# 10 项代码审查发现的复核结果

复核日期：2026-10-13。目标基线：`codex/affiliate-sales-batch-20261013`，提交 `661399e`。进入任务时工作区为较早的 `main`（`d5fb6fa`），部分被审查文件尚不存在；已切换至包含全部被审查文件的现有业务分支。原有 `docs/generated/route-coverage.json` 未提交修改保留。

确认并修复 1 项购物车状态问题；其余 8 项未在该基线复现，另 1 项属于前后端一致的现有业务规则。

| 编号 | 复核结论 | 代码与行为证据 |
| --- | --- | --- |
| 1 | 现有业务规则，不修改 | `internal/store/admin_order_create.go:110` 与 `web/src/components/admin-order-create.tsx:159` 均明确禁止优惠码与任何内部价格覆盖并用。首次、续费覆盖金额各自已有独立范围校验。单独续费覆盖加优惠码返回 409 是当前合同，并非 OR 运算错误；允许该组合属于业务规则变更。补充 API 回归覆盖此组合。 |
| 2 | 未复现 | `internal/httpapi/sales_settlements.go:104` 已有 `NOT EXISTS(credit_invoice_payments ...)`，拒绝未还清、余额非零或还款凭证总额不匹配的信用账单。结算时间取还款到账时间；集成测试验证未还款拒绝、还清后只能在到账月份结算。 |
| 3 | 未复现 | `internal/store/affiliate.go:488` 查询的是是否存在未还清信用记录。现金发票无此记录，`creditOutstanding=false`，继续入队。现金佣金快照及并发幂等测试通过。 |
| 4 | 部分成立，已修复 | 准入失败原来只返回 `admission_code/admission_error`，仍有 `active=true`，会允许勾选及编辑。现同时设置 `active=false`；保留购物车列表供用户查看和处理，其他合格商品继续可用。前端不再将单纯准入失败提示为“产品已变更”。既有前端结账按钮和后端订单创建已经拒绝不合格商品，未发现绕过准入下单。新增测试先复现旧代码失败，再验证修复及手机验证后恢复可用。 |
| 5 | 未复现 | `internal/store/checkout_lines.go:96` 在首次覆盖前保存 `DefaultRecurring`。集成测试验证首次 700 分、默认续费 1234 分，数据库服务续费价同为 1234 分。 |
| 6 | 未复现 | `SendSMS` 计算配置有效性后仍无条件解析提供商，并检查 `resolveErr`，失败返回非空错误。`cmd/worker/main.go:149` 将错误交给 `CompleteSMSJob`，并记录投递失败。补充未知提供商测试，验证返回固定错误且不发出请求；未测试真实短信服务投递。 |
| 7 | 未复现，建议的反向修改会引入错误 | `remaining_cents<>0` 位于 `NOT EXISTS` 内部，用于排除尚有欠款的账单。改为 `=0` 将排除已全额偿还的账单，并可能允许未还清记录通过该余额条件。信用结算和到账月份测试通过。 |
| 8 | 未复现 | `FindAllStringSubmatch` 的 `m` 是字符串数组，校验使用 `m[1]`；`ReplaceAllStringFunc` 回调的 `m` 是完整匹配字符串，`m[2:len(m)-2]` 正确移除两侧 `{{` 与 `}}`。正常、未知、畸形占位符测试通过。 |
| 9 | 未复现 | 还款事务先更新信用记录为 `repaid` 并登记还款，再调用佣金队列。测试验证未偿还及部分偿还为 0 条佣金，全额偿还后为 1 条，重放仍为 1 条。 |
| 10 | 未复现，19 是受支持类型 | `internal/store/options.go:245` 的数量类型列表明确包含 19，`docs/product-configuration.md` 亦记录 14–19。新增真实 API 测试验证全局类型 19 创建、绑定、数量乘算及结账：标准周期价 1234 分，加 3×25 分配置及 99 分初装费，账单总额 1408 分，无 panic。 |

## 验证

使用新建的独立 PostgreSQL 18 集群及 `zjmfgo_review_test` 数据库，关闭 TCP 监听，只通过专用 Unix socket 连接。测试每次创建并清理随机 schema，不使用业务数据库。

针对性测试命令（`ZJMFGO_TEST_DATABASE_URL` 指向该隔离库）：

```bash
REQUIRE_INTEGRATION=1 go test ./internal/httpapi ./internal/store ./internal/notification \
  -run '^(TestCartAdmissionAvailabilityAndRecovery|TestOrderAdmissionCheckoutAndReplay|TestAdminOrderAdmission|TestAdminOrderCreatePriceOverridesReviewAndDeferredInvoice|TestAdminOrderCreateSensitiveQuoteSnapshotAndCoupon|TestAffiliatePaymentCommissionSnapshotAndConcurrentIdempotency|TestAffiliateRulesCreditCashMonthAndReplay|TestSalesSettlementLifecycle|TestSalesSettlementConcurrentRefund|TestContractPlaceholderValidation|TestGlobalConfiguration.*|TestSMS.*|TestIdcsmart.*)$' \
  -count=1 -v
```

24 个顶层测试全部通过。另通过 `go vet ./...`、`git diff --check`、前端 `npm run typecheck` 及 `npx eslint src/components/cart-view.tsx`。本次未运行全量集成测试、浏览器验收或真实短信投递，未部署补丁；前端验证为静态检查。测试集群在验收后停止。
