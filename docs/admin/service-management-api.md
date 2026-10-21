# 管理员服务管理契约

本文冻结旧服务管理的恢复边界，定义单服务代续费、同客户批量代续费和第三批人工价格及期限 API。各批实现与验收分别记录；状态纠错、可信人工收款及服务转移仍为后续批次。前端不得用通用保存或客户身份替代这些动作。

## 旧行为与本项目取舍

旧源码由后台需求盘点的只读输入定位，不作为活动知识入口。主要证据为 `ClientsServicesController::hostRenew/postBatchRenew/postInfo/postTransfer`、`Renew::renew`、`Upgrade::deleteUpgradeInvoices` 及旧服务页面。

| 旧行为 | 恢复规则 |
| --- | --- |
| 续费 controller 建未付单，页面另外选择余额、人工收款或仅建单；批量 controller 的 type 注释不等于实际执行 | 报价、建单、余额付款独立，人工收款另列财务批次；后台记录真实管理员，不借客户登录 |
| 同客户批量多服务，逐项账期/金额后合并账单 | 一个批次关联多张单服务账单，逐项明确结果与恢复；本批沿用服务当前账期和合同价，不合并为多服务账单 |
| 零价续费可直接延期并清长期价；本次手工金额可能写回长期价 | 本次固定应付、长期续费合同价、免费补偿各自独立；零价仍显式付款结算，不因一次续费清零长期价 |
| 同账期把已有服务 amount 当最终应付 | 自动合同/优惠模式与管理员固定应付模式分开；固定应付不再重复叠加客户组或优惠码，恢复自动前预览 |
| 普通保存可改本地期限/状态，模块动作另走调用；保存也可能触发 hook 或资源下游同步 | 真实暂停/恢复/终止继续走现有生命周期。人工期限需区分客户权益和上游实际期限，状态纠错单独受限，不能复活终态 |
| 转移时删除续费，升级转移还会处置未完成单与余额 | 转移先处理未决业务，保留财务事实；不能靠删账单再改 owner 达成 |

## 第一批共同规则

路径前缀 `/api/v1`，响应封装、原管理员凭据、CSRF、错误及 `X-Request-ID` 沿用[后台 API](api.md)。三个入口均 `Cache-Control: private, no-store`。报价/建单要求 `services.view`、`invoices.view`、`services.renew`；余额付款要求 `services.view`、`invoices.view`、`services.renew.pay_balance`，不隐含授予建单权限。非超级管理员需显式分配新权限。

新报价、新建账单及新付款要求目标为范围内且启用的真实客户。服务为已复核的 active/suspended、可续费账期；拒绝退款权益保留、配置变更、取消互斥及未完成/待核验交付任务。历史回执仍校验当前权限、客户范围和原凭据，但不因客户后来停用、服务状态变化、旧版本或优惠已消耗而失效。权限、客户范围和原会话/API 密钥在锁等待后复核，凭据在提交前再检查自然过期。代建和代付均记录真实管理员 actor、客户、服务与原因。

本批每次只续当前账期的一个周期，允许提前续费；不要求开启自动续费，也不改变该开关、长期合同价或当前客户归属。金额为整数分，币种沿用现有 CNY 结算，不新增多币种能力。

报价不创建账单、不扣款、不修改权益；供应商报价只调用既有只读协议，并在释放准备事务后读取远端，随后重开事务核对版本和绑定。新建始终是未付款账单，包括零价；余额付款独立确认后才走现有结算、权益/供应商任务、流水、通知及佣金链路。

## 报价

`POST /admin/services/{id}/renewal-quotes`，成功200，不要求幂等键。

```json
{"expected_version":12}
```

版本取服务详情中的 `version`。响应示例：

```json
{
  "data": {
    "customer_id":123,
    "service_id":456,
    "service_version":12,
    "billing_cycle":"monthly",
    "base_cents":2500,
    "discount_cents":0,
    "amount_cents":2500,
    "current_due_at":"2026-11-20T08:00:00Z",
    "pending_invoice_id":0,
    "can_create":true,
    "expires_at":"2026-10-21T08:05:00Z",
    "quote_hash":"64位小写十六进制摘要"
  }
}
```

contract模式下，`base_cents` 是服务合同周期基础价，`discount_cents` 是按服务冻结续费优惠计算的本次减免，`amount_cents=base_cents-discount_cents`。fixed模式下，`base_cents=amount_cents` 为长期固定最终应付、`discount_cents=0`，保留的原合同估算通过管理状态的 `contract_*` 读取。不按现有客户分组重新追溯合同价。`current_due_at` 可以为null。

报价到数据库时钟的下一个整五分钟边界失效，**有效期最多五分钟**。摘要绑定服务/价格/优惠/供应商快照与该窗口；提交重新计算，不能改摘要或靠客户端时间延长。网络慢或跨边界时需重新报价。

已有未付续费单时返回 `can_create=false`、原 `pending_invoice_id` 和空 `quote_hash`；此时 `base_cents/discount_cents/amount_cents` 仍是按当前有效定价模式计算的估算，并非原账单金额。页面应读取并展示原账单，付款取原账单金额和版本，再让管理员明确选择；本接口不替换或删除它。其余业务围栏仍可返回错误，不能把有原账单当作允许付款的保证。

## 代建未付续费单

`POST /admin/services/{id}/renewals`，要求唯一 `Idempotency-Key` 请求头，去首尾空白后8–128字节。首次201，同键同请求的历史回执200。

```json
{
  "expected_version":12,
  "quote_hash":"从报价原样取得的64位小写十六进制摘要",
  "reason":"客户通过工单要求提前续费"
}
```

`reason` 去首尾空白后必填、最多240字符。只接受上述字段，不接收本次覆盖价、客户编号、账期或付款模式。返回 `invoice_id/invoice_no/service_id/amount_cents/replayed/customer_id/version/status`，其中 `version` 为新账单版本、`status="unpaid"`。

事务原子写入账单、续费行快照、管理员审计和统一 `mutation_operations` 回执，scope 为 `admin:service.renewal.create`。内部账单键派生自管理员/scope/原key，隔离客户提供的续费键。创建不扣款、不续期、不启动上游续费。

已有未付续费单返回409 `renewal_invoice_pending`；服务版本变化为409 `stale_service`；报价窗口或内容变化为409 `stale_renewal_quote`。重新读取服务/原账单后由管理员确认新意图。已成功请求的原key回执优先于报价过期、旧版本及已消耗优惠，不能因为响应丢失再生成一张单。

## 使用客户余额付款

`POST /admin/services/{id}/renewals/{invoice_id}/pay`，要求独立的唯一 `Idempotency-Key`。首次201，历史回执200。

```json
{"expected_version":1,"amount_cents":2500,"reason":"按客户工单使用其现有余额续费"}
```

`expected_version` 使用账单版本；`amount_cents` 必须显式提交、等于当前账单总额，0有效；`reason` 去首尾空白后必填、最多500字符。只支持该客户、该服务的单行 automatic 类型续费账单，包括现有正常客户续费单；不能代付充值、其他服务、多服务混合或人工通用账单。

返回现有付款字段 `invoice_id/invoice_no/amount_cents/balance_cents/status/replayed`，另加 `service_id/customer_id/renewal_status`。`status="paid"` 仅表示本地账单收款完成；本地服务 `renewal_status="completed"`，供应商为 `pending`，必须继续刷新任务、服务及上游回读状态，不能显示“上游已续费”。

付款同事务复用原客户余额结算路径，保留余额冻结、真实渠道未决支付、取消/退款/配置互斥、优惠次数、权益、供应商队列、通知和佣金检查。流水键另设命名空间，管理员审计和 `admin:service.renewal.pay` 统一回执一并提交。余额不足不自动充值、补账、改价或创建替代单，原未付单保留。

金额不符为409 `renewal_amount_changed`，账单版本变化为409 `stale_invoice`。业务条件不符也返回明确冲突；401重新登录，403无权限，404对象不存在或范围外，不回退客户普通身份。

## 未知结果与前端接线

创建和付款分别保存原服务/账单编号、请求体、原因及key。超时/断网时保留同次请求体与key恢复；同key异参返回409 `idempotency_conflict`。不得换key重新付款，或用余额变化推断本次请求成功。

回执代表当时已提交的事实，仍校验当前权限、范围和原凭据。创建回执可能仍为 `unpaid/version=1`，而当前账单已经付款；付款回执中的余额和供应商pending也可能已经变化。恢复后读取最新账单、服务和任务，各自展示，不把旧回执当当前详情。

前端将“报价”“仅建未付单”“使用客户余额付款”分为明确操作；付款展示客户、服务、账单号和精确金额并要求确认。按钮分别按权限显示，零价也确认。与当前自动续费开关分开；本批仅实现 API，页面尚未接线。

## 第二批：同客户批量代续费

批次最多20项，所有服务必须属于明确指定的同一客户。清单按提交顺序冻结；不能重复服务、混入其他客户，或通过批次覆盖价格、账期、服务归属和自动续费开关。报价和登记仍各自核对客户范围、真实原始凭据及动作权限。跨客户、客户不存在或范围外均整批404，不返回可供探测其他客户的逐项结果。

四个入口均沿用管理员认证、CSRF、统一错误及 `Cache-Control: private, no-store`。报价要求 `services.view`、`invoices.view`、`services.renew`；登记、查询和执行按冻结的 `action` 校验相同读取权限及 `services.renew` 或 `services.renew.pay_balance`。批次仅原登记管理员可访问，查询以actor、scope和batch_id共同定位，其他管理员即使有相同权限也不能接管。登记一个付款批次不授予建单权，登记建单批次也不授予余额付款权。执行每项重新检查当前权限、客户范围和本次原凭据，401/403或范围变化立即停止其后项目。

### 批量报价

`POST /admin/service-renewal-batch-quotes`，成功200，只读，不要求幂等键。

```json
{"customer_id":123,"items":[{"service_id":456,"expected_version":12},{"service_id":789,"expected_version":4}]}
```

`expected_version` 取每个服务自己的版本。返回按清单顺序排列的逐项结果，每项有 `service_id/status`；`status="quoted"` 时包含 `quote`，字段与单服务报价相同；`status="failed"` 时包含业务 `code/error`。某项业务冲突不伪装为报价0元，也不影响其他合法项目的报价；认证或范围错误仍按整批边界停止。

已有未付续费单的 `quote.can_create=false` 不表示原单金额已确认，不能自动替换或采用原单。管理员先读取原账单详情，明确核对其版本、金额和服务，再登记独立的 `pay_balance` 批次。报价不建单、不扣款，也不创建批次。

### 登记不可变清单

`POST /admin/service-renewal-batches`，要求唯一稳定 `Idempotency-Key`，首次201，同键同清单重放200。`action` 只能为 `create` 或 `pay_balance`，不能在一个批次混用。

创建清单：

```json
{"customer_id":123,"action":"create","reason":"按客户工单批量代建续费单","items":[{"service_id":456,"expected_version":12,"quote_hash":"从该服务报价原样取得的64位小写十六进制摘要"}]}
```

付款清单：

```json
{"customer_id":123,"action":"pay_balance","reason":"按客户工单使用余额付款","items":[{"service_id":456,"invoice_id":901,"expected_version":1,"amount_cents":2500}]}
```

`create.expected_version` 为服务版本，`pay_balance.expected_version` 为账单版本；付款 `amount_cents` 必须显式提交，0有效。真正执行创建时重新校验各项单服务报价哈希及有效窗口，付款时重新校验该服务的单行续费账单、版本及精确金额；登记成功不保证这些条件在执行时仍满足。`reason` 去首尾空白后必填、有效UTF-8且不含NUL，create最多240字符、pay_balance最多500字符。每批一个原因，不接受逐项改价、改期或自动选付款模式；服务不能重复，付款账单也不能重复。

登记只持久化意图并返回 `batch_id` 及进度，不创建续费账单、不扣余额、不延期、不派发上游写入。`batch_id` 是从原key派生的64位小写十六进制内部键。原key必须绑定同一有序清单、动作、客户和原因；同key异参返回409 `idempotency_conflict`。登记重放也没有执行副作用，不能拿它替代执行接口。

### 查询进度

`GET /admin/service-renewal-batches/{batch_id}`，成功200，只读。返回 `batch_id/customer_id/action/reason/items/succeeded/failed/unconfirmed/pending/replayed`；items保留冻结的输入，每项另有 `status`，成功时带 `result`，确定业务失败时带 `code/error`。四个计数字段覆盖本批全部项目：

| 项目状态 | 含义及恢复 |
| --- | --- |
| `pending` | 尚无确定结果，需显式选择后执行 |
| `unconfirmed` | 存在执行意图而结果尚不确定；保留原execute key恢复，不能据此认定失败或换批重扣 |
| `failed` | 该次执行已确定的业务失败；同execute key保持原结果，重新核对后用新execute key显式重试 |
| `succeeded` | 已有单项原回执，`result` 为原单服务建单或付款结果；任何后续执行都复用该事实 |

状态描述批次执行事实，不代表当前账单、余额或服务详情。成功创建的原回执可能仍为unpaid但当前账单已付款；供应商付款回执成功仍只表示本地paid、上游pending。查询后需分别读取最新账单、服务和任务，不靠计数或余额差推断外部成功。

### 显式执行或恢复

`POST /admin/service-renewal-batches/{batch_id}/execute`，要求与登记分开的稳定 `Idempotency-Key`，请求明确选择清单中的服务：

```json
{"service_ids":[456,789]}
```

`service_ids` 必须包含1–20个互不重复且属于本批清单的服务编号，不接受空选择或清单外编号。成功200并返回批次进度。执行按原manifest顺序处理所选子集，调用既有单项建单或余额付款路径；提交顺序不会被 `service_ids` 的排列改变。每项使用独立事务，已成功项目保留，不因后项失败整体回滚，也不重新创建或扣款。各次attempt共享批次派生的稳定单项key，成功回执不随execute key变化。

同execute key绑定同次选择，已确定失败项目不隐式重试；修复业务条件后须用新execute key显式选择重试。响应返回最新全批次进度，可能包含其他attempt后续成功的结果，不是旧attempt响应的原样快照。报价过期、价格/金额或版本变化不能改旧清单，应重新读取、报价并确认新批次；旧成功项不得再次纳入新的付款意图。执行期间401/403、客户范围变化或未知数据库/提交结果会停止后续项目；未知结果不写成确定失败，应先GET并使用原execute key恢复。

同批次执行通过独立 `pgx.ConnectConfig` 会话持有 `pg_try_advisory_lock`，不占用业务连接池，结束时关闭协调会话释放锁；同批次忙时409 `renewal_batch_busy`。进程同时最多4个批次协调会话，容量满非阻塞返回409 `renewal_batch_capacity`；不通过耗尽业务池阻塞已有付款。GET不锁钱包、账单或服务业务行，在读取结果后再次核对当前归属，执行中仍可查看已提交进度。没有后台自动扣款worker。批次、attempt及失败观察均复用统一 `mutation_operations`，scope分别为 `admin:service.renewal.batch`、`admin:service.renewal.batch.execute:<batch_id>`、`admin:service.renewal.batch.outcome:<batch_id>`，键为派生的64位十六进制。单项仍使用既有建单/付款scope，键为 `batch-renewal:` 加64位摘要，读取时同时核对原请求hash。无需新增迁移或旁路回执表，意图不保存Cookie、API密钥或供应商凭据。

### 前端与验收边界

页面先固定客户，再分别进行批量报价、选择创建或付款清单、登记和确认执行；查询、登记重放均不可触发执行。每项显示原账单、精确金额、业务错误、未确认及供应商pending，允许明确选择失败子集重试；按钮按动作独立授权。没有后台自动续跑，但关闭页面或断网不能证明在途请求已停止或未扣款，重新打开须先恢复进度。本批UI仍未接线。

批量测试覆盖跨客户及越权整批拒绝、20项上限和严格输入、报价只读、登记/GET无业务副作用、有序子集、同key异参、成功不重扣、失败只经新attempt重试、未知提交恢复、执行锁忙和释放、协调会话与业务池隔离、逐项撤权/自然过期/停止、业务围栏、供应商本地paid与远端pending及客户付款竞态。第二批数据库验收与第一批独立记录，具体结果见下方验收边界；批量与自动续费的新增组合场景、浏览器、真实上游及部署未单独验收。

## 第三批：人工价格与客户期限

所有入口沿用管理员认证、CSRF、客户范围、原凭据及 `Cache-Control: private, no-store`。金额为整数分，0须显式提交；未知字段拒绝。新动作权限不随 `services.manage` 自动授予。预览无建单、扣款、延期或上游写入，摘要绑定服务版本、定价/期限快照和数据库时钟的下一个整五分钟边界；提交须带原摘要及版本。长期定价、单次建单和期限调整三个写入口各自使用稳定 `Idempotency-Key`、统一回执与真实管理员审计；等待后及提交前复核权限、范围和原凭据，原key优先恢复历史事实，不因旧报价过期而再次执行。

### 当前管理状态

`GET /admin/services/{id}/management`，要求 `services.view`，返回200：`service_id/customer_id/service_version/status/billing_cycle/pricing_mode/fixed_amount_cents/contract_base_cents/contract_discount_cents/contract_amount_cents/amount_cents/next_due_at/term_mode/supplier_next_due_at/supplier_observed_at`。

`pricing_mode=contract` 时使用服务原合同基价及冻结续费优惠，`fixed` 时 `amount_cents` 为管理员长期固定的最终应付，不再折扣。`contract_*` 保留原合同估算；`fixed_amount_cents` 在contract模式为null。读取原回执后刷新该入口和账单/服务详情，不能把历史回执当当前状态。服务详情及列表保持原 `recurring_price_cents` 合同基价，新增 `renewal_pricing_mode/fixed_renewal_amount_cents`，`renewal_amount_cents` 显示当前有效应付；这组字段名与management投影不同。

`next_due_at` 是客户权益期限。没有人工调整时 `term_mode=follow_supplier`，供应商保持原跟随逻辑；调整后为 `managed`。上游期限及观测时间仅来自真实只读/执行结果，尚未观测为null，不从本地期限反推。非供应商的mode仅标识是否有人工调整来源，不表示存在上游。

### 长期固定续费价与恢复合同价

`POST /admin/services/{id}/renewal-pricing/preview`，要求 `services.view`、`services.pricing`：

```json
{"expected_version":12,"mode":"fixed","fixed_amount_cents":1900}
```

恢复原合同模式使用 `{"expected_version":12,"mode":"contract"}`，不传fixed金额。返回200，含 `customer_id/service_id/service_version/mode/fixed_amount_cents/contract_base_cents/contract_discount_cents/contract_amount_cents/amount_cents/billing_cycle/current_due_at/expires_at/quote_hash`。

`PUT /admin/services/{id}/renewal-pricing` 在同一输入上增加 `quote_hash`、必填 `reason`（去首尾空白后最多240字符），提供稳定key；成功及历史重放均200，返回报价字段及 `version/replayed`。固定价独立于原 `recurring_price_cents` 和优惠计数保存；默认续费、自动续费、管理员普通续费均使用固定最终应付，优惠使用次数不增加。恢复contract删除固定覆盖，继续使用原合同与当时剩余优惠次数，不按客户当前分组或商品现价重定价。固定模式期间禁止配置/商品/账期升级，须明确恢复contract后重新报价。

操作不修改已有账单或历史金额。未付关联账单、退款冻结、配置/取消冲突、未完成交付及待核验续费等围栏阻断改价；定价真实变化增加服务版本及自动续费代次，保持开关不变；同模式同金额的no-op仍留审计/回执，但版本及代次不增加。scope 为 `admin:service.pricing.set`。

### 本次固定应付续费

`POST /admin/services/{id}/fixed-renewal-quotes`，要求 `services.view`、`invoices.view`、`services.renew`、`services.renew.override`：

```json
{"expected_version":12,"amount_cents":1900}
```

返回200，字段沿用单服务续费报价，`base_cents=amount_cents`、`discount_cents=0`。摘要与普通续费报价隔离，不可互换。

`POST /admin/services/{id}/fixed-renewals` 在输入上增加原 `quote_hash` 及必填 `reason`（去首尾空白后最多240字符），提供稳定key，首次201、重放200，返回既有代建续费单字段。scope 为 `admin:service.renewal.fixed.create`。只建一张未付单，包括0元；不修改长期价格或优惠使用次数，不重复套用分组/优惠码。后续显式余额付款仍要求 `services.renew.pay_balance`，沿用原结算、退款、通知、佣金和供应商任务链路。供应商报价仍读取真实成本/原期限；本地收款不是远端续费完成。

### 客户期限纠错与免费补偿

`POST /admin/services/{id}/term-adjustment-quotes`，要求 `services.view` 及与kind对应的 `services.term.correct` 或 `services.term.compensate`：

```json
{"expected_version":12,"kind":"compensation","next_due_at":"2026-12-01T08:00:00Z","reason":"服务故障补偿"}
```

`correction` 允许纠正为早于或晚于原期限，`compensation` 必须严格晚于原期限；均须不同于原期限且为2000–9999年的RFC3339时间，最多微秒精度，不接受null。`reason` 去首尾空白后必填、最多500字符。预览含 `service_id/customer_id/service_version/kind/mode/current_due_at/supplier_next_due_at/supplier_observed_at/next_due_at/expires_at/quote_hash`，mode是当前模式，提交后进入managed。

`POST /admin/services/{id}/term-adjustments` 在原输入上增加 `quote_hash`，提供稳定key；首次与重放200，返回 `service_id/customer_id/service_version/kind/mode/before_due_at/next_due_at/supplier_next_due_at/supplier_observed_at/replayed`，scope为 `admin:service.term.adjust`。原因绑定预览，修改原因须重新预览。

仅启用客户已交付、未终止的active/suspended周期服务可调整；一次性、未复核、库存已释放、未付关联账单、退款冻结、配置/取消/交付未决及未确认供应商续费等阻断。调整不建单、不入账、不改状态、不自动恢复暂停、不修改实际上游期限或宣称真实上游成功；增加服务版本及自动续费代次，保留自动续费开关。免费补偿没有销售收入或收款流水。

managed模式下普通同步/回调只更新真实上游期限观测，不能覆盖客户承诺。真实确认一次供应商续费后，按客户合同账期从客户原期限与确认时刻的较晚者延长一次，并保存按原job唯一的不可变权益事实；重复确认不重复延期，不能用远端到期日直接替换客户期限。自动续费以客户期限为准，managed不沿用“上游尚未到期所以跳过”的跟随模式；可能在上游尚未过期时购买一个周期，仍受真实成本上限及既有支付/任务围栏约束。managed客户期限到期的暂停按客户权益执行，不能因上游更晚而擅自延长；已有远端暂停不冒认新系统暂停来源。

### 第三批错误与恢复

| HTTP / code | 含义与前端处理 |
| --- | --- |
| 400 `invalid_service_pricing` / `invalid_fixed_renewal` / `invalid_service_term` | 定价、金额、版本、原因、时间或摘要输入不合法；保留草稿并修正。通用JSON及key错误沿用后台公共协议 |
| 409 `stale_service` / `service_owner_changed` | 服务版本或客户已变化；先读取当前管理状态，再重新预览和确认 |
| 409 `stale_pricing_quote` / `stale_renewal_quote` / `stale_service_term_quote` | 对应预览已过期或快照变化；不能自动换摘要后提交，须重新确认 |
| 409 `invalid_service_term_change` / `service_term_unavailable` | 补偿未延长、期限未变化或服务不满足调整资格；展示明确原因 |
| 409 `service_term_operation_pending` / `service_action_pending` / `supplier_renewal_pending` / `renewal_invoice_pending` 等业务围栏 | 先处理原账单、交付、退款或取消；不得删除历史事实绕过围栏 |
| 409 `fixed_service_pricing` | 固定价阻止商品、配置或账期变更；管理员显式恢复contract后重新报价 |
| 409 `idempotency_conflict` | 同key已绑定其他请求；先核对原意图及历史事实，不能将换key当作未知结果恢复 |

401/403/404及断网/超时不证明原写入失败。三个写入口均保留原body/key，通过原请求重放恢复；确认后再读取management及关联账单。期限调整不会自动恢复远端暂停，供应商续费仍需检查任务是否真实确认。

### 迁移与验收

迁移188新增独立服务固定价，189新增独立客户期限管理/上游观测及供应商确认续费权益事实，不修改services整行形态，不改旧迁移checksum，不猜测或回填历史管理意图/远端期限。旧服务无记录时沿用原行为。发布须先停旧API、worker及其他writer，再迁移并启动配套程序；旧writer不得混跑或直接降级，以免忽略固定价或覆盖客户期限。

第三批 E1 已独立完成后台严格runner的180个入口，包含本批新增14项，全部run/pass且零跳过；相邻回归结果见下方验收边界。UI仍未接线，浏览器、真实供应商、远端CI与部署未验证，不能复用前两批或旧真实上游验收结论。

## 后续批次的固定边界

1. **状态纠错**：真实动作使用现有适配器；仅允许明确的本地纠错转换，终止态不复活，记录原因、证据及未决任务处置，不开放任意字符串保存。
2. **可信人工收款**：独立财务协议、付款来源/证据和防重复入账；不能模拟网关回调或简单设置paid冒充外部到账。
3. **服务转移**：源/目标客户范围、启用状态、服务owner代次及双方并发控制；先清未付单、退款、升级、取消及外部未决任务。历史财务留原客户，新单归新客户，默认关闭自动续费，保留明确的合同权益。旧订单购买字段、按服务聚合活动、密码/控制台链接需隔离或交接核验；库存跟随服务，不靠改owner自动失效凭据。

后续每批实施前补精确字段、版本、幂等恢复和对应数据库/外部验收；本段不表示这些接口已实现。

## 验收边界

第一批行为测试纳入已有 `scripts/test-admin-api.mjs --integration`，检查报价无副作用、权限/范围、严格输入、回执隔离、一次扣款、余额不足、审计/回执回滚、客户付款竞态、撤权和过期，以及供应商收款与远端续费的区分。客户/自动续费与原财务结算另做相邻回归。

数据库、浏览器、真实供应商和部署分别记录。测试执行状态以实际门禁证据为准；本文协议和源码存在不等于数据库、页面或生产已验收。

第一批 E1 已在任务自有 PostgreSQL 18.6、UTC 会话中实际执行：后台严格 runner 共156个入口（含新增14个单服务代续费入口），race模式全部通过，零跳过；相邻客户/自动续费、有限次优惠、财务、取消、退款与供应商回归共53项通过。相邻首轮的历史升级夹具遗漏了分表后的管理员，已按原身份还原 pre064 数据并单项复跑通过，保留创建人、密码哈希及身份不复制为客户的断言；旧迁移未修改。Go全仓测试编译、相关包vet、架构门禁、16项接口盘点/消费者测试及文档校验通过。

该验收使用本地供应商协议模拟器；第二批批量功能不复用其通过结论。新增页面接线、浏览器、真实上游及部署均未验收。服务人工管理整组继续为partial，其余批准批次不计已实现；第一批机器证据保存在被忽略的 `output/admin-service-renewals/`。

第二批 E1 已在任务自有 PostgreSQL 18.6、UTC 会话以race模式完成25个专项入口：新增批量10项、既有单服务代续费14项及RBAC路由覆盖1项，全部取得run/pass，零跳过。覆盖真实付款提交回包丢失后的原key恢复、部分余额按原清单顺序执行、失败子集显式重试、逐项凭据过期停止、并发执行互斥、GET不等待业务行锁，以及业务池仅1个连接时两个批次仍可执行和查询进度。首轮21项通过后，任务临时库WAL占满tmpfs导致数据库崩溃；重建并限制WAL后，受影响4项全部复跑通过。原失败日志与复跑日志均保留，这不是一次连续全绿运行。

全仓Go测试编译、相关包vet、架构门禁、16项接口盘点/消费者测试、生成器及契约/文档校验通过。第二批结束时后台严格runner登记166个入口，该批未全量重跑；第一批156项及相邻53项仍为历史证据。第二批机器证据位于被忽略的 `output/admin-renewal-batches/`，汇总为 `combined-summary.json`。UI未接线，浏览器、真实供应商、远端CI与部署未验证；模拟供应商结果不代表真实上游验收。

第三批 E1 在任务自有 PostgreSQL 18.6、UTC 会话、fsync及同步提交开启的环境中完成：后台严格runner全量180个入口，以race模式全部取得run/pass，零失败、零跳过；其中新增HTTP 11项与Store 3项。覆盖长期固定价/恢复原合同及剩余优惠、单次固定与0元续费、收款退款、只读预览/CAS、原key并发及真实COMMIT回包丢失恢复、撤权/自然过期、审计和回执回滚、期限补偿/纠错围栏、上游观测与客户期限隔离、managed自动续费及旧恢复来源失效。真实schema187升级验证旧services与财务快照保持、新表不推断回填；月底及闰年账期计算统一按上海日历截到月末。

相邻供应商、自动续费、到期暂停/恢复、同步/回调、配置商品/长账期及本地续费退款共45个额外入口，在同一UTC隔离库串行以race完成，全部run/pass且零失败、零跳过；与后台180项没有重叠。本轮未执行真实worker进程SIGKILL验收，worker方法经本地供应商协议模拟器验证。

全仓Go测试编译、相关包vet、supplier单元race、架构门禁、16项接口盘点/消费者测试及生成器通过。最终源码摘要、严格runner、先期失败与修复记录保留在被忽略的 `output/admin-service-management/`；初轮SQL参数类型及临时库锁容量问题修复后，全量180项在同一最终源码上连续通过。第三批不复用早期8/9或前两批的通过数量。UI、浏览器、真实供应商、远端CI及部署仍为未验证，状态纠错、可信人工收款及转移未实施。
