# 注册客户字段专项测试与夹具

基线efebb1d，叠加后端9cab76d，仅新增registration_fields_restoration_test.go及本记录。使用/tmp/zjmfgo-refund-local.env，现testServer PG18 `_test`库及随机schema，不碰业务库，不发邮件短信。

覆盖公开schema六属性白名单/内部隐藏停用隔离；六类型含false/0、必填/真实日期/regex/select/未知key拒绝；版本变化、禁用GET/POST；无字段旧注册兼容、审计及profile插入故障原子回滚、并发同邮箱唯一账号；字段目录锁与定义变化下旧注册409。相关后台字段与profile旧语义回归并跑，不改变内部required管理语义。

```sh
source /tmp/zjmfgo-refund-local.env
scripts/go.sh test -race ./internal/httpapi -run '^(TestRegistrationFields|TestAdminCustomerCustomField|TestCustomerProfile)' -count=1 -v -timeout 5m
```

opt-in TestRegistrationFieldsBrowserFixture沿用adminBrowserFixture，READY_FILE/FINISH_FILE/ORIGIN与可设3300秒的BROWSER_TIMEOUT_SECONDS，默认skip。root registration-fields-super；manager registration-fields-manager具有customers.view/manage、settings.view/manage；reader registration-fields-reader仅customers.view/settings.view。六公开required字段+内部required+隐藏+停用，0600私有ready.fixture.json只含IDs。隔离schema注册开关开启、验证码及邮箱验证关闭，无真实渠道。表单合法示例：text=ABC，textarea任意非空，number=0，date=2024-02-29，select=A，boolean=false。

短时ready/stop烟测PASS18.202s，9字段清单/0600检查通过，退出采样schema查询0、临时清单清理；未挂长fixture。浏览器四宽及全量未执行，由主代理负责，不将此fixture烟测算浏览器业务通过。

实际上述专项-race退出0，包耗时76.346s，匹配全部PASS（fixture默认SKIP）。故障注入两条预期ERROR均已对应rollback断言，不是未处理失败。日志/tmp/registration-fields-race-20261016.log。注册重复响应不当作幂等重放：并发同邮箱一个201一个409，无匿名明文密码持久化测试账本。

补充容量回归：100字段完整返回，101字段GET/POST503 registration_fields_unavailable且无用户；3个合法select字段的选项集合超过256KiB时同样整体拒绝而不截断；注册关闭时优先403。新增容量+原空schema旧请求/回滚并发专项-race退出0，包24.056s。未挑主分支其他提交。

启动fixture的准确命令（路径必须尚不存在）：

```sh
source /tmp/zjmfgo-refund-local.env
ZJMFGO_BROWSER_READY_FILE=/tmp/registration-fields-accept.ready \
ZJMFGO_BROWSER_FINISH_FILE=/tmp/registration-fields-accept.finish \
ZJMFGO_BROWSER_ORIGIN=http://127.0.0.1:3093 \
ZJMFGO_BROWSER_TIMEOUT_SECONDS=3300 \
scripts/go.sh test ./internal/httpapi -run '^TestRegistrationFieldsBrowserFixture$' -count=1 -v -timeout 56m
```

ready主清单为api_origin/username/schema；ready+'.fixture.json'私有清单为manager_id、reader_id、field_ids。field_ids键为text/textarea/number/date/select/boolean/internal/hidden/disabled。结束显式创建FINISH_FILE，helper清理schema与文件。密码不写清单/日志；测试账号沿现helper固定测试凭据，不属于生产身份。
