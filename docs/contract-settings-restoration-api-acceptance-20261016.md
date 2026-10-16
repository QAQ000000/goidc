# 合同设置恢复HTTP/PG专项

事实范围：仅新增 `internal/httpapi/contract_settings_restoration_test.go`，基线cbde223，叠加后端699caf6进行验证。不改后端、前端、共用helper，无外发、worker或资源动作；仅source `/tmp/zjmfgo-refund-local.env`，沿用testServer的PG18 `_test`库名保护与随机schema清理。

执行：

```sh
source /tmp/zjmfgo-refund-local.env
scripts/go.sh test -race ./internal/httpapi -run '^TestContractSettingsRestoration' -count=1 -v -timeout 3m
```

PASS 5.812s：35模板名称/正文搜索、all/enabled/disabled、跨页总数与无重复遗漏、空/越界；新模板单记录GET与settings.view权限；settings和template双编辑者一个成功一个409、同key并发唯一审计、异参409、撤权后重放403；审计故障500不改变version/审计数，移除故障后同键可成功；模板改动/停用不改变历史正文，总开关关闭拒绝新申请及客户提交、历史仍可读。两条预期ERROR来自隔离schema审计故障注入，不是未处理失败。settings旧幂等哈希只针对enabled业务值；异参测试改变enabled，不将仅expected_version变化误认作业务异参。

opt-in `TestContractSettingsRestorationBrowserFixture` 复用adminBrowserFixture，显式READY_FILE/FINISH_FILE，无环境默认skip。35模板（18启用/17停用）支持跨页；manager员工有settings.view/manage，reader仅settings.view。私有ready.fixture.json为0600，只含员工ID和模板ID，不提交凭据。主helper用户contract-settings-accept-super；员工用户名contract-settings-manager、contract-settings-reader，测试密码沿用公共测试helper但不写入本记录。显式Origin http://127.0.0.1:3093。

短时ready/stop烟测PASS 13.205s，35条及0600检查通过，结束后采样schema查询计数0，ready/finish/私有清单删除。没有挂长fixture。未执行四宽1440/1024/390/320浏览器交互、前端检查或全量；完整批次检查由主代理负责。分页一致性静态实现为同SQL statement，运行测试证明稳定数据集的items/total及真实翻页，不声称已通过持续并发写入压力验证。
