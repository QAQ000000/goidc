# 商品升级恢复专项测试与浏览器夹具

基线38b66e7，独立测试文件product_upgrade_restoration_test.go；不改经济引擎。后端2327ac2叠加验证。仅显式source `/tmp/zjmfgo-refund-local.env`，随机PG18 `_test` schema，无真实供应商/资源动作，不跑全量。

新增断言：目标下架/库存耗尽/报价过期提交409且服务不切换；目录价格更新不改变已报价金额；两个后台编辑竞争一个200一个409；相同header key原revision重放200、异参409及撤权后重放403。现有ProductChange及ConfigurationUpgrade回归承担付款前后、人工/模拟交付、库存释放、退款/终止与历史订单快照等经济链路，避免重写引擎测试。

浏览器opt-in TestProductUpgradeRestorationBrowserFixture，adminBrowserFixture口径READY_FILE/FINISH_FILE/ORIGIN，默认无环境skip。root upgrade-restoration-super；customer upgrade-customer；manager upgrade-restoration-manager（products.view/manage、services.view/manage、invoices.view），reader upgrade-restoration-reader（products.view、services.view、invoices.view）。0600 ready.fixture.json仅ID清单，不写密钥或凭据。两种本地商品为manual源/目标，4个已支付已人工开通服务；目标初始库存1，可进行真实预留/取消/付款流程。供应商不支持示例只有本地mode记录，无账号/URL，不会外发。

fixture内受控context的本地claim/process循环仅处理独立schema手工任务，使付款后进入人工确认进度；确认仍需浏览器调用正常delivery API，未运行生产worker。原有源/目标配置项允许客户配置升级，后台可改目标路径。Origin烟测为http://127.0.0.1:3093。

短时ready/finish烟测PASS11.763s，4服务清单/0600通过；结束采样schema计数0，ready/finish/私有清单清除。此为fixture启动收尾验证，不是浏览器操作通过；四宽1440/1024/390/320待主代理验收。

专项命令：

```sh
source /tmp/zjmfgo-refund-local.env
scripts/go.sh test -race ./internal/httpapi -run '^(TestProductUpgradeRestoration|TestProductChange|TestConfigurationUpgrade|TestUpgradePolicy)' -count=1 -v -timeout 5m
```

实际专项-race执行退出0，包耗时34.759s，全部匹配测试PASS（含新增四类报价边界、策略编辑与既有手工/模拟交付、快照、库存、取消退款、权限审计回归）。日志在/tmp/product-upgrade-restoration-race-20261016.log。报价目录改价测试故意不提升revision以验证经济快照；正常后台改价会提升revision，既有ProductChangePermissionsStaleAndAuditRollback覆盖该冲突，不声称后台改价可无条件提交旧报价。

最终补测：叠加test-only e1959f1（策略原子审计/凭据撤销），上述相关全部-race退出0，墙钟89.02s、包70.807s；scripts/go.sh build ./...退出0/10.61s，scripts/go.sh vet ./...退出0/10.19s。独立日志/tmp/product-upgrade-final-20261016/{race,build,vet}.{log,time}。未重复运行全量，未操作主代理活跃fixture。

正式改价边界：客户只生成报价但尚未提交时，后台正常改价提升商品revision；提交旧报价将409，须重新报价。已经提交生成升级账单后，账单/变更存储既定金额和新周期价，后续目录改价不重算该账单，付款及交付沿用已提交快照；服务商品只在交付成功后切换。新增price测试直接SQL只改价不升revision，用来单独证明冻结金额，不证明正常后台改价前后都能提交旧报价。正常revision变化导致未提交报价拒绝已有相关回归，已提交后正式改价场景本次主要为源码核对，不能冒称新专项逐项端到端验证。
