# 旧接口兼容性与路由审计白皮书

审计基线为 `docs/generated/legacy-inventory.json`，参考源码目录为
`/www/wwwroot/zjmf-manger-decoded-副本`。审计只读旧目录，没有修改旧 PHP
文件。每一条清单记录以 `file:line:method` 和旧表达式指纹固定，结果保存在
`docs/audit/route-reviews.json`，由 `scripts/route-coverage.mjs` 生成索引。

## 口径

清单统计的是 Route 调用声明，不是唯一 URL，也不是功能数量。它包含重复声明、
注释中的声明以及当前源码已经漂移的行；不包含 ThinkPHP 动态
`Route::controller` 及被 include 的额外路由。旧 `admin` 分组归一化为
`/:legacy_admin`，因为实际后台前缀来自部署配置。`/v1` 和 `/api/v1` 保持为
不同命名空间。

状态含义如下：

* `matched` 只表示通过真实请求/响应测试证明的协议兼容，本轮没有把路径相似的
  接口标成 matched。
* `covered` 表示新 Go API 或 Next 页面承接了已确认的业务能力，但字段、状态码、
  权限或 URL 可能改变。
* `frontend_replaced` 表示旧模板页面由 Next 页面和 API 承载。
* `superseded`/`deprecated` 表示旧任意运行时执行、安装器、调试或不安全副作用
  入口由新部署/能力模型取代。
* `missing_candidate` 表示当前没有足够证据证明等价能力，记录了差异和后续工作，
  不代表已经实现。

## 结果

生成时间和完整逐项证据见 `output/audit/route-coverage.json`（运行 `node scripts/route-coverage.mjs` 生成，不跟踪）；封存基线见 [历史审计](archive/README.md)。下表为封存基线：

| 项目 | 数量 |
| --- | ---: |
| 旧 Route 调用声明 | 1,352 |
| Go 路由注册（封存基线） | 348 |
| 已审查 | 1,352 |
| 未审查 | 0 |
| `matched` | 0 |
| `covered` | 189 |
| `frontend_replaced` | 40 |
| `superseded` | 52 |
| `deprecated` | 46 |
| `missing_candidate` | 1,025 |
| 旧源码行/声明漂移 | 96 |

按领域的明细直接从生成索引的 `summary.modules` 读取，避免白皮书和台账出现
第二份会漂移的数字。`missing_candidate` 数量较高是刻意保守的结论：旧系统的
DCIM 资产、信用额/多币、合同、CRM、OAuth、站内信、客户关怀、供应商下游分销
以及动态模块等能力没有被同名的新页面或列表接口掩盖。

## 已确认的兼容边界

供应商主机同步保留 `POST /api/host/sync` 和 `POST /api/v1/host/sync`，并有
签名成功/失败测试；新协议额外校验 `host_id`、交付快照和终态。支付、短信、SMTP、
通知模板、内容管理、商品配置、服务动作、工单基础操作、客户认证和推广结算均
有新 API，但采用实例 ID、版本、幂等键、整数分金额、审计和权限模型，不应把旧
字段直接透传成兼容协议。

服务电源动作使用 `power_on`、`power_off`、`reboot`、`force_off`、
`force_reboot`，由适配器映射到上游命令；客户 VNC 仍是缺口，后台 VNC 权限不
能推断客户端点。监控当前只对 NOKVM 且资源有效的服务开放，也没有宣称是通用
供应商图表兼容。

## 缺口优先级

优先补齐会阻断业务闭环的客户资料/手机号模式、充值与信用额、DCIM 资产及批量
服务动作、供应商下游分销、客户 VNC、站内收件箱、实名第三方核验和多语言模板。
合同签署、CRM/客户关怀、营销批次、多币、应用市场和 PHP 动态插件应在确认实际
消费者后单独立项。每个 `missing_candidate` 都带 `follow_up`，实现时必须新增
真实 API、权限边界和集成测试，不能为了减少缺口数添加空别名。

## 验证

```text
node scripts/route-coverage.mjs --strict
node --test scripts/route-coverage.test.mjs
./scripts/go.sh test ./... -count=1       # 独立 PG18 测试库，exit 0
./scripts/go.sh vet ./...                 # exit 0
./scripts/go.sh test -race ./internal/httpapi -run 'TestSupplierHostSyncCallback|TestAuthenticationOwnershipAndCSRF|TestAPIKeysAuthenticateAndRevoke' -count=1  # exit 0
```

全量 Go 测试和 vet 使用临时 PG18 数据库完成，测试结束后数据库已删除；专项
race 测试无竞态。该白皮书描述的是审查完成度和已证能力边界，不是“已替换旧站
全部功能”的上线声明。
