# 优惠码恢复HTTP/PG定向验收

状态：测试草案已编写，后端契约交付后定向执行；不代表页面浏览器验收完成。本测试只写 `promo_restoration_test.go`，不改后端或前端。

使用 `/tmp/zjmfgo-refund-local.env` 的显式测试DSN，沿用testServer的 `_test`库名检查、随机schema及清理，不接触生产库和真实上游。

```sh
source /tmp/zjmfgo-refund-local.env
go test ./internal/httpapi -run '^(TestPromoRestoration.*|TestPromoCodeLifecycleAndCheckoutDiscount|TestPromoCodesPageSearchPaginationOrderFilterAndPermissions)$' -count=1 -v
```

新增业务断言覆盖状态优先级/失效边界、旧active参数、搜索分页/零结果；日期omitted/clear(null与空串)/合并非法；两编辑者版本竞争；并发同键创建、异参冲突、唯一审计；无权限/只读/管理及会话中撤权。相关既有消费测试验证折扣计算和checkout使用次数语义。只断言公开行为和持久化结果，不复制实现。

与后端协商：version初始1；POST201含完整记录、同键重放201；PATCH200含完整记录；日期合并错误PATCH400，版本和幂等冲突409。无键兼容不承诺重放。四尺寸1440/1024/390/320浏览器验收未执行；完整批次仍须scripts/test-all.sh、Go build/vet及前端typecheck/lint/隔离build，当前开发中仅执行本域定向测试。

2026-10-16实际执行：测试tree叠加后端32bc726及修正a60f9f3，以上命令加-race通过，5个测试全部PASS，包耗时16.247s。初次有效PG运行发现one_time优惠已使用但列表未标耗尽，与既有消费语义不符；后端修正后保留该回归断言通过。日期clear/omitted/merged、真实分页及兼容筛选、版本并发、同key创建/PATCH并发、异参409、唯一审计和权限撤回均已通过。仅执行此定向范围，不代表全量或浏览器验收完成。

独立浏览器fixture为 TestPromoRestorationBrowserFixture，默认skip，复用adminBrowserFixture但不改共用helper。显式传ZJMFGO_BROWSER_READY_FILE、ZJMFGO_BROWSER_FINISH_FILE及ZJMFGO_BROWSER_ORIGIN=http://127.0.0.1:3093；ready附带.fixture.json为0600私有凭据/ID清单，包含35条优惠（五状态各7条）、可编辑/清空日期对象及promos.view只读员工。root用户名promo-accept-super。只提供本地API，无worker/供应商/外发。

2026-10-16显式短时ready/finish烟测PASS 15.277s，健康检查/35条清单/0600权限通过，finish后随机schema查询计数0，ready/finish/私有清单均删除。默认无环境测试SKIP通过。未挂长fixture，也未执行浏览器交互或全量测试。
