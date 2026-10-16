# 客户信用门户专项与本地夹具

基线0eefd6e，叠加后端90299a3验证，只新增customer_credit_portal_test.go和本记录。不改资金引擎；只source `/tmp/zjmfgo-refund-local.env`，随机PG18 `_test` schema，不外发、不运行worker、不碰生产。

新测试覆盖4API匿名401/非客户403、A/B归属与跨账单404、越权/未知筛选400、本人非信用账单credit:null；显式字段白名单及no-store，账户异常缺行503而不补零；真实manual账单信用付款→部分还→全还，余额不变、额度债务守恒；未还退款冲销不造还款、部分/全还拒绝退款；开关关闭冻结不隐藏历史；上海付款/到账自然日边界、q的%/_字面匹配、分页真实总数/跨页/越界及空客户。

```sh
source /tmp/zjmfgo-refund-local.env
scripts/go.sh test -race ./internal/httpapi -run '^(TestCustomerCreditPortal|TestCreditLifecycle|TestCreditManagement)' -count=1 -v -timeout 4m
```

浏览器fixture默认无环境skip，启动路径必须不存在：

```sh
source /tmp/zjmfgo-refund-local.env
ZJMFGO_BROWSER_READY_FILE=/tmp/customer-credit-accept.ready \
ZJMFGO_BROWSER_FINISH_FILE=/tmp/customer-credit-accept.finish \
ZJMFGO_BROWSER_ORIGIN=http://127.0.0.1:3093 \
ZJMFGO_BROWSER_TIMEOUT_SECONDS=3300 \
scripts/go.sh test ./internal/httpapi -run '^TestCustomerCreditPortalBrowserFixture$' -count=1 -v -timeout 56m
```

root credit-portal-super；manager credit-portal-manager（credit.view/manage、invoices.view/manage/refund、customers.view、settings.view/manage）；reader credit-portal-reader（credit.view、invoices.view、customers.view、settings.view）；客户credit-portal-a、credit-portal-b、credit-portal-empty。沿helper测试凭据，清单不写密码/密钥。私有ready.fixture.json0600：manager_id、reader_id、customer_a_id、customer_b_id、customer_empty_id、invoice_ids。invoice_ids包含outstanding/partial/repaid/refunded/unpaid/other；消费额每张3000分、partial已还1000、repaid已还3000；A余额1234分、额度200000分。other属于B，empty无记录。

所有状态由真实manual账单/信用付款/后台还款/退款形成，没有伪造资金行。短时ready/stop烟测PASS23.307s，6账单ID及0600检查通过，采样schema结束计数0，临时清单清理。四宽1440/1024/390/320浏览器与全量尚未执行，由主代理另验，不把fixture烟测算页面通过。

上述专项-race实际退出0，包9.099s，所有匹配测试PASS（opt-in fixture默认SKIP）。日志/tmp/customer-credit-portal-race-20261016.log。预期缺账户诊断ERROR及旧审计故障注入ERROR分别有503/回滚断言。非快照跨请求刷新不声称原子；分页测试为稳定数据集，无持续并发写压力承诺。

补充专项：重复query拒绝、100Unicode字符接受/101拒绝、撤销会话后4API401、display_enabled=false仍保留债务/还款历史且不改变payment_enabled、credit_settings缺行503。独立-race两项PASS6.832s。site_settings缺行另发现全局maintenanceAllowed读取失败经s.fail映射404（在客户API前截获）；未把404写成PASS，已报主代理独立判断修复范围，本测试提交不修改生产middleware。
