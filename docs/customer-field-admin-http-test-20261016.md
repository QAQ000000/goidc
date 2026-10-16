# 客户字段后台 HTTP 专项与浏览器夹具（2026-10-16）

本记录依赖后端 `89f864138b1f118874588b3269fcea6fec6a008b`，基线 `0615efc`。仅使用本地隔离 PostgreSQL 18 `_test` 数据库，每个测试创建随机 schema；未操作生产或外部服务、未部署。

## 执行及结果

加载本地测试环境（不记录连接凭据）后执行：

```sh
scripts/go.sh test -race ./internal/httpapi -run '^(TestCustomerFieldAdmin|TestAdminCustomerCustomField|TestRegistrationFields)' -count=1 -v
```

初次运行包耗时59.433秒：原有字段CRUD/并发2项、注册字段4项、新增查询/竞争版本/同快照3项通过；另外2项因测试把既有`replayed:true`元数据计作业务回执变化而失败。随后仅修正测试比较方式，保留对`id/version/deleted`以及数据库快照的严格比较，重跑这2项：

```sh
scripts/go.sh test -race ./internal/httpapi -run '^(TestCustomerFieldAdminImmutableReplayHistoryAndRevocation|TestCustomerFieldAdminConcurrentUpdateAndDeleteReceipts)$' -count=1 -v
```

结果PASS，5.410秒。查询测试再增加重复参数、非法UTF8/NUL、带空格类型及大小写匹配后，单独race重跑PASS，3.841秒。因此最终新增5个顶层用例及上述既有6个顶层回归均有通过记录；未执行本项目全量测试。

## 证据覆盖

- 配置列表及详情：匿名401、仅`customers.view`员工403、manage员工200；既有只读definitions接口仍允许view。新详情合法不存在404、非法ID400，成功`no-store`。
- 配置DTO完整白名单：`id,field_key,label,field_type,description,validation_pattern,choices,sort_order,admin_only,required,show_on_register,active,version,created_at,updated_at`。列表及详情均核对完整集合、正ID/version、数组choices。
- q去空白、中文100码点成功/101拒绝、字面百分号/下划线/反斜杠、大小写匹配；未知及重复参数、无效类型/active、非法UTF8/NUL拒绝；非法分页回落及相同sort_order按ID排序。
- 不可重命名409 `field_key_immutable`：定义全行、目录版本、审计数及profile JSON快照零变化。停用后删除保留false/0历史profile。
- 真实POST/PATCH/DELETE提交后，目标删除及GET404不阻断原键原载荷200恢复；业务回执固定且目录/审计/profile快照零变化。三类同键异参409，陈旧版本409；撤角色权限403、撤会话401不能借回执绕过。
- 同键并发PATCH与DELETE返回相同业务回执，创建/修改/删除合计恰好3条审计；不同键竞争相同版本恰好一200、一409。选项重复/空白/缺失、非下拉选项、非法Go正则、名称及描述长度边界均验证。
- 同快照并发测试：25次真实HTTP采样，观察total=1共13次、total=21共12次；并发写入/删除20字段成功提交42次。每次items长度均等于total；若无至少一个真实提交周期则测试失败。此为有变更的运行采样，隔离级别另由源码保证。
- 注册既有回归：六类型公开白名单、内部required隔离、false/0持久化、审计/profile故障回滚、注册并发/旧契约、定义修改串行化及目录失败关闭。

## 浏览器夹具

```sh
ZJMFGO_BROWSER_READY_FILE=/tmp/customer-field-admin-ready-20261016.json \
ZJMFGO_BROWSER_FINISH_FILE=/tmp/customer-field-admin-finish-20261016 \
ZJMFGO_BROWSER_ORIGIN=http://127.0.0.1:3095 \
ZJMFGO_BROWSER_TIMEOUT_SECONDS=3300 \
scripts/go.sh test ./internal/httpapi -run '^TestCustomerFieldAdminBrowserFixture$' -count=1 -v -timeout 56m
```

READY含随机API监听地址`api_origin`、用户名和schema；前端代理使用api_origin，公开Origin使用3095。创建FINISH结束测试。

提供62个真实API创建字段：六类型公开字段、仅管理员/不注册显示/停用的required冲突标志3项、53个分页字段。账户包括`field-admin-super`、`field-admin-manager`（manage+view）、`field-admin-viewer`（view-only）及`field-history-customer`（历史false/0资料）。账户使用既有测试helper，不在记录中输出密码。

私有`READY.fixture.json`权限0600，字段为`manager_id,viewer_id,customer_id,field_ids,field_count`。独立smoke PASS，34.811秒；确认62字段manifest、0600和实际API `/healthz` 200。finish后ready/manifest/finish全删除。专项日志及smoke记录的9个精确schema逐一只读核对，剩余0；未删除或干预其他任务schema。

此记录不代表浏览器页面交互、URL/草稿/故障恢复、四宽度或工程全量验收已完成；这些由主任务继续执行。
