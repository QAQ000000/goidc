# 支付异常统一浏览器验收夹具（2026-10-16）

`TestPaymentExceptionBrowserFixture` 将退款与可信关单两组初始状态放在同一个隔离 PG18 schema、同一个真实 API 原点。只有设置 ready 和 finish 文件路径时才启动；正常测试默认跳过。数据库仍要求 `_test` 后缀，使用 `testServer` 的迁移重放、schema 检查和退出清理机制。支付网关是本机 `httptest`，不接触真实商户和生产配置。

## 启动与结束

在仓库根目录执行（路径必须尚不存在）：

```bash
source /tmp/zjmfgo-refund-local.env
export ZJMFGO_BROWSER_READY_FILE=/tmp/payment-exception-3097.ready.json
export ZJMFGO_BROWSER_FINISH_FILE=/tmp/payment-exception-3097.finish
export ZJMFGO_BROWSER_ORIGIN=http://localhost:3097
scripts/go.sh test ./internal/httpapi \
  -run '^TestPaymentExceptionBrowserFixture$' -count=1 -timeout=20m
```

ready 文件出现后，读取其中 `api_origin`，用它作为前端 `API_INTERNAL_URL`，再以 3097 端口启动前端。必须在前端启动/构建前指定这个值，因为 Next.js rewrite 使用该原点。默认验收期限为 15 分钟，可通过 `ZJMFGO_BROWSER_TIMEOUT_SECONDS` 调整，测试自身 timeout 也需相应延长。

验收结束：

```bash
touch /tmp/payment-exception-3097.finish
```

Go test 正常退出后删除 ready、伴随 manifest、finish 文件，并 DROP 本次独立 schema。不要直接终止测试进程来代替 finish；强制杀进程可能绕过 Go 清理。

## 交接文件和状态

ready 与 `${ZJMFGO_BROWSER_READY_FILE}.fixture.json` 权限均为 `0600`。伴随文件采用排他创建，拒绝覆盖现有文件，内含临时测试账户及以下数据，不要将文件提交 Git 或输出全部内容。

- `accounts`：客户、closer、reader、无查看权限员工、超销售范围员工，以及独立退款申请/审核/只读员工。
- `payment_close.invoices`：6 笔 `closable` 普通 mock 账单，分别可用于基本关单、丢失成功回执后的幂等恢复、409 版本冲突及其他尺寸；另有 `closed_retry`、`unknown_frozen`、`late_anomaly_frozen`、`epay_unsupported` 各 1 笔。
- `closed_topup_cancel` 和 `closed_topup_retry` 各 1 笔，均为 accepted 充值，`topup_can_retry=true`，供客户取消与重新发起支付。
- `external_refund`：保留原退款 fixture 的 7 笔空/待复核/已确认/已驳回退款数据及 1 笔待补账充值。
- 每笔关单数据带 `invoice_id/payment_id/expected_version/admin_path/customer_path/close_path/constructed_by`，初始状态来自实际 API 或明确标识的 Store 状态机调用。

关单员工只有 `invoices.view` 与 `payments.close`；reader 只有 `invoices.view`，客户使用实际客户会话。没有新增绕过权限的调试 API。

普通 mock 的关闭通过 HTTP 和编译进系统的模拟适配器完成；unknown 用 Store 真实预约和未知结果记录构造；迟到异常通过 Store 原通知处理链路构造。两笔 closed 充值沿用后端测试的显式可信结果构造方法：创建真实 accepted 充值和 Epay 意图后，调用 `BeginPaymentClose/CompletePaymentClose` 种入既有证据。该步骤仅准备历史状态，**没有给运行时 Epay 添加关单能力**，并断言它仍 `close_supported=false`。浏览器只能验证投影、取消和重新支付，不得将其当成真实 Epay 关单证明。

原 `TestExternalRefundReversalBrowserFixture` 入口继续可用，复用相同退款 setup；统一夹具应优先用于同一前端同时验收两种业务。

## 本批验证

- 正常无 opt-in 参数的两个 fixture 入口：PASS（默认跳过，不创建环境）。
- 隔离 PG18 实际启动统一 fixture：PASS，84.679 秒（包含等待 HTTP 自检的时间）。
- 通过真实 TCP HTTP 登录，验证 12 笔关单状态、7 笔退款状态、只读/无权限/销售范围拒绝、关单、幂等重放、409 版本、unknown/late 冻结、Epay 不支持、closed 普通账单重付、充值取消与重付：PASS。
- ready/manifest `0600`、finish 正常退出、独立 schema 及三个交接文件全部清理：PASS。
- `scripts/go.sh vet ./internal/httpapi`、`git diff --check`：PASS。

本批只准备并自检夹具，没有浏览器四尺寸验收，没有发送真实付款、退款、短信或资源命令。后续浏览器证据单独记录。
