# 2026-10-13 旧供应商退款、删除与同步逻辑复核

## 结论

本次以两个旧 PHP 目录的现场源码为依据：`/www/wwwroot/zjmf-manger-decoded`（当前维护版）和 `/www/wwwroot/zjmf-manger-decoded-副本`（原始副本）。初次复核只读审查 PHP，在隔离 PG18 schema 中复现 Go 状态问题；没有提交真实上游请求、修改业务数据库或部署。用户随后指定采用主项目方案，工作区实现见末尾追加记录。

用户指定的业务流程继续采用：在远端申请退款，等待远端实际删除，本地通过定时查询或签名回调同步终止状态。退款申请被受理不能作为资源已删除的证据，本地账单退款仍是独立财务操作。

旧系统存在三类不同入口：普通服务的取消申请、本地财务退款，以及资源池代理向远端提交的退款。不能把它们统称为“删除接口”。远端是否必须先退款、是否审批后删除，不能仅凭本地 PHP 转发层判断。

## 源码事实

以下相对路径以当前维护版目录为根；副本差异另行注明。行号为本次读取位置。

| 流程 | 源码位置 | 实际行为 |
| --- | --- | --- |
| 客户取消申请 | `app/home/controller/HostController.php:2731`，`postCancel` | 校验归属、Active/Suspended、商品允许取消、Immediate/Endofbilling、原因及重复申请，然后写 `cancel_requests`。返回 200 表示提交成功，不直接删除或退款。API 模式下资源不存在或已经 Deleted 也可能返回 200 |
| 定时处理立即取消 | `app/admin/command/Cron.php:182`，`cancellations` | 扫描 Immediate 未完成申请，调用 `Host::terminate`；成功标记申请完成，失败标记失败。此通用入口没有先退款的前提 |
| 向供应商请求取消 | `app/common/logic/Host.php:568`，`terminate` | 魔方供应商分支 POST `/host/cancel`，携带上游资源 ID、Immediate 和原因。响应 success/200 就写本地 Deleted、删除时间并清理资源字段；原状态 Pending/Active/Suspended 时返还库存 |
| 普通服务本地退款 | `app/admin/controller/ClientsServicesController.php:1452`，`refund` | 按天、全额或自定义金额；退款至余额会写流水、增加客户余额，或仅标记退款。该方法没有调用上游删除，也没有调用 `Host::terminate` |
| 账单财务退款 | `app/admin/controller/InvoiceController.php:1151`，`refund` | 独立处理退款流水/余额等财务记录，不能把该入口的成功当作供应商资源删除 |
| 资源池代理远端退款 | `app/admin/controller/AgentController.php:938`，`postRefund` | 选择启用的资源池 API，转发到 `resource/resourcerefundfromagent`；参数包括 invoiceid、amount、subtotal、refund_reason。此方法直接返回远端结果，没有在本地写 Deleted |
| 资源池退款详情 | `app/admin/controller/AgentController.php:658`，`getRefundDetail` | GET `resource/refundDetail`，并转换代理侧退款金额及本地账单展示金额。上述两个资源池入口在副本中也存在 |
| 主动读取资源状态 | `app/common/logic/Host.php:677`，`sync` | 优先读取 `/host/header` 的 `host_data`，通过 `mergeUpstreamLifecycleState` 合并状态；方法本身不返还库存、不退款 |
| 上游推送状态 | `app/api/controller/HostController.php:173`，`syncInfo` | 校验签名，接受 Pending/Active/Cancelled/Fraud/Deleted/Suspended，直接更新白名单状态；该路径没有调用主动同步的终态保护方法，也不返还库存或退款 |

普通供应商与资源池代理使用不同接口和账户选择方式；不能直接把 `resource/resourcerefundfromagent` 加到现有普通魔方供应商适配器并假定协议可用。本次未请求远端退款接口，不验证远端审批和退款金额。

### 旧版哪些行为不能照搬

1. **取消成功不等于删除成功。** `postCancel` 返回 200 可能只代表写入申请，但 `terminate` 的 200 分支会立即把本地标为 Deleted。异步供应商应读取资源实际状态后再终止，不能仅判断请求返回码。本次真实测试的 202 也只代表受理。
2. **旧库存回收集中在主动 terminate。** 主动 `sync` 和状态回调没有对应库存返还；只复制旧同步代码会漏掉远端删除后的库存释放。Go 应保留当前单次库存释放机制。
3. **旧终态保护只覆盖部分入口。** 当前维护版主动同步有保护，但回调直接更新状态，仍可能被迟到状态覆盖。
4. **退款与删除分开。** 资源池退款转发代码不包含本地删除；普通取消处理也没有强制退款前置规则。用户测试供应商的实际政策优先，不能把通用旧代码当作远端政策。

## 两个目录的同步差异

当前维护版 `app/common/logic/Host.php:51` 的 `mergeUpstreamLifecycleState`：

- 已有 Cancelled/Fraud/Deleted 终态不接受状态覆盖。
- 非 Pending 状态不回退到 Pending。
- 本地 Suspended 不直接被远端 Active 覆盖；Active 可以更新 Pending/Active。
- 保留暂停原因及类型，Active 清除暂停原因。

原始副本 `app/common/logic/Host.php:607` 的 `sync` 主要在 `:644` 处理 Pending → Active，没有上述统一合并方法，不能依赖它主动同步 Deleted。

维护版也并非完整的全量资源轮询：`app/admin/command/Cron.php:291` 的 `hostInfo` 主要重试向下游推送状态，`:325` 附近只查询 Pending 超过 300 秒的 resource 产品开通状态。`app/admin/controller/BatchSyncController.php:9` 的执行入口直接 `exit("za")`，不能算可用的批量同步任务。不能据此断言所有 Active 产品会自动同步删除。

## 当前 Go 对照与隔离复现

已有 `internal/store/supplier_delivery.go:18` 定时同步，针对 provisioning/active/suspended 供应商服务复用同步任务，完成五分钟后重新调度。工作区已增加 `Deleted/deleted → terminated` 映射，并复用幂等库存返还；这个映射尚未部署到常驻 worker。

本次发现一个必须修复的缺口：`internal/store/supplier_delivery.go:344` 的 `applySupplierRemote` 根据远端状态更新服务，没有保护本地终态。`internal/store/supplier_callback.go:109` 的签名回调也会进入此方法。周期任务排除 terminated，并不能阻止迟到回调或已有任务返回旧状态。

通过临时 Go overlay 测试 `TestLegacyLifecycleProbeDelayedActive`，使用 `supplierSetup` 的 PG18 随机隔离 schema，依次提交匹配资源、有效签名的 Deleted 和 Active 回调，得到：

```text
remote=Deleted local=terminated stock=1 inventory_released=true
remote=Active local=active stock=1 inventory_released=true
late Active callback revived a terminated service; stock was already released
--- FAIL: TestLegacyLifecycleProbeDelayedActive (1.32s)
```

这是有意构造的缺陷复现失败，不是前序供应商回归测试失败。没有碰业务数据库或真实供应商；fixture 清理随机 schema。去敏日志及复现源码保存在 `output/legacy-supplier-review-20261013/`，不进入 Git；临时 overlay 文件已清理。

还需区分本地暂停与上游暂停，才能决定远端 Active 是否恢复服务；不能机械复制旧版“任何 Suspended 都不恢复”的规则。旧版 Fraud 也需要明确的本地状态/人工核验策略，目前 Go 没有相应映射。未知状态和读取失败不得视为删除。

## 后续实现约束

1. 在统一状态落库入口保护 terminated 终态，覆盖轮询、回调、已有任务和人工核验，保持资源身份及库存一致性。
2. 补回归：Deleted → Active/Pending/Suspended 不复活、库存不重复释放、不重复通知；并检查并发同步与删除回调交错。
3. 明确暂停来源与恢复策略；Fraud 采用显式策略，不悄悄转为 provisioning 或 terminated。
4. 完成上述修复和隔离回归后再部署 Deleted 映射，复验真实只读同步。保留远端退款/删除、本地同步的边界，不恢复本地主动销毁入口。

以上为初次复核时的事实和待办；随后实现进度如下。

## 追加：采用主项目状态合并方案

用户明确要求采用已经验证的主项目方案，Go 工作区现已移植维护版 `mergeUpstreamLifecycleState` 的适用状态规则：

- queued/provisioning 对应旧 Pending；active/suspended 不回退到 provisioning。
- cancelled/terminated 对应本地终态，不被迟到 Active/Pending/Suspended 等状态恢复。
- 本地 suspended 不因上游 Active 自动恢复，暂停原因保持；接受 Active 时清除暂停原因。
- 接受 Suspended 时携带 `suspendreason_type` 前缀，避免重复添加；回调及 `/host/header` 都读取类型字段。
- `applySupplierRemote` 在服务行锁内合并最新状态，轮询、回调、人工核验统一使用。已有资源不可换绑。
- 已忽略的 Pending 响应不会为已激活、暂停或终止服务追加开通查询任务；库存继续幂等返还。

Go 没有 Fraud 状态，本批保持显式拒绝和人工核验，不将其默认为删除；未新增数据库状态。旧主项目仅主动同步调用合并方法，Go 回调也使用同一规则，修复本次复现的回退入口。远端退款/删除、本地同步的流程保持不变。

新增/扩展隔离回归：

| 检查 | 覆盖 |
| --- | --- |
| `TestSupplierMaintainedLifecycleRules` | 签名回调和周期查询分别验证 Pending → Active、Active/Suspended 不回退 Pending、Suspended 不被 Active 恢复、Active 清暂停原因、暂停原因类型前缀及未知 Fraud 拒绝 |
| `TestSupplierPeriodicSyncAfterRemoteDeletionReleasesOnce` | Deleted/deleted 删除后重复回调及迟到 Active/Pending/Suspended/Cancelled 不复活，库存不重复释放、账单仍 paid、周期调度停止、本地 terminate 禁用 |
| `TestSupplierInflightActiveSyncCannotReviveDeleted` | 真实 HTTP fixture 阻塞 Active 查询响应，先完成 Deleted 签名回调，再释放查询；服务仍终止、库存只返还一次、任务结案并清账户租约 |

验证结果：强制隔离 PG18 全部 `TestSupplier*` 加 race 通过（32.789 秒）；supplier/store 单元测试、`scripts/go.sh vet ./...` 和 API/worker 构建通过。证据在 `output/legacy-supplier-review-20261013/maintained-lifecycle-*.log`。

全量 `scripts/go.sh test ./...` 注入隔离测试 DSN 和 `REQUIRE_INTEGRATION=1` 后，HTTP 包触及默认 10 分钟总时限，停止在 `TestAdminResourcesPageContractAndPermission` 的 schema 清理阶段；该用例当时仅运行约一秒，日志未报告断言失败。其他 Go 包通过，legacy-import 为 70.538 秒。根据 `go test -list '^Test'` 的实际执行顺序，超时前完成 390 个 HTTP 测试；从中断用例起补跑剩余 85 个，设置 15 分钟时限，全部通过（125.680 秒）。因此 HTTP 回归以分段方式完成，原始全量命令的超时结果仍保留，不能记作单次全量命令 PASS。证据分别为 `maintained-lifecycle-all-test.log`、`httpapi-test-order.txt`、`maintained-lifecycle-remaining-test.log`。

本批没有改前端或数据库 schema，没有重新操作真实上游，也没有部署常驻 API/worker。

用户随后要求提交部署，2026-10-13 16:34 已发布代码 `c81c040`；运行库为满足当前二进制要求同步应用此前提交的 126–135 迁移。服务 14 保持终止及库存已释放，没有再次请求真实退款/删除或下单。发布前数据库恢复、迁移数据保留及发布后只读接口检查均通过，详见 [部署记录](deployment-20261013-supplier-sms.md)。以上“没有部署”保留为实现与回归阶段的历史边界。
