# 线上充值待补账 HTTP 与浏览器夹具验证（2026-10-16）

验证依赖后端 `02ee9f825e2f6f75cedbbae17b4ed480f84e9c8b`。使用已授权的本地隔离 PostgreSQL 测试库；每个用例创建随机 schema 并清理。没有访问生产库、外部网关或修改部署配置。

## 定向 HTTP 验证

加载本地测试环境后执行：

```sh
scripts/go.sh test -race ./internal/httpapi -run '^(TestTopupLiabilityHTTP|TestOnlineTopupHTTP|TestEpayPaymentLifecycleAndReplay|TestEpayCallbackRollbackAndInvalidFields|TestEpayPendingGuardsAndServiceSettlement)' -count=1 -v
```

结果：10 个顶层用例全部 PASS，包耗时 21.020 秒，无 race 报告。新增 2 个用例覆盖：真实签名回调在余额流水写入失败后保留 pending_liability；补账与幂等重放、不同键/参数冲突、回调重放、余额/唯一流水/唯一审计、原失败事实及 paid_at 不变、停用客户及充值设置不阻断既有责任、现金收入报表不重计、客户详情不泄露内部说明/操作者/幂等信息；读写权限、匿名/客户/API key 写入拒绝、销售范围及角色/会话/范围撤销后的重放拒绝、陈旧版本和输入拒绝。

## 可复用浏览器夹具

```sh
ZJMFGO_BROWSER_READY_FILE=/tmp/topup-liability-ready-20261016.json \
ZJMFGO_BROWSER_FINISH_FILE=/tmp/topup-liability-finish-20261016 \
ZJMFGO_BROWSER_ORIGIN=http://127.0.0.1:3094 \
ZJMFGO_BROWSER_TIMEOUT_SECONDS=3300 \
scripts/go.sh test ./internal/httpapi -run '^TestTopupLiabilityBrowserFixture$' -count=1 -v -timeout 56m
```

夹具沿用 `adminBrowserFixture`：`READY.api_origin` 是随机监听端口，供前端代理使用；`BROWSER_ORIGIN` 是公开前端 Origin。结束时创建 FINISH 文件。

提供 `liability-super`、`liability-finance`、`liability-reader`、`liability-limited` 员工，`upgrade-customer` 与 `liability-other-customer` 客户。密码沿用现有测试 helper。私有 `READY.fixture.json`（0600）记录 `customer_id`、`other_customer_id`、`finance_id`、`reader_id`、`limited_id`、`pending_invoice_ids`（4 张）、`resolved_invoice_id`。财务员工可补账，只读员工只有查看权限，受限员工没有目标客户销售范围。

夹具 smoke 使用独立临时 ready/finish：PASS，30.170 秒；确认 4 张待补账、1 张已补账、manifest 权限 0600、实际 API `/healthz` 返回 200。创建 finish 后测试退出，ready/manifest/finish 全部删除；SQL 确认对应 schema 剩余数量 0。

本记录不代表实际浏览器交互、四种宽度验收或全量测试已执行；这些由主任务继续完成。
