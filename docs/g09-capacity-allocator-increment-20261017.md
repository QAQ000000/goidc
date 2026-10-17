# G09 容量资源分配与预算租约

本增量新增 `scripts/g09-capacity-allocator.mjs`。`createG09CapacityAllocator` 接受已由调用方准备的 profile 和实体投影，并要求客户、服务、账单、正常供应商账户的数量分别精确为 1000、2000、10000、10；实体 ID 唯一，服务/账单必须属于已登记客户，余额与每客服务基数不能越界。供应商 unknown 专户必须独立于 10 个正常账户。

每个 flow 只能成功保留一次 lease。客户通过显式 `customerId` 或稳定的 flow ID 哈希分配；可选 `lane=hot`/`normal` 将分配限制在固定的前 20%/后 80% 客户池。allocator 固定检查每客最多 50 次写、每次最多 1000 分、测量可用余额最多 98000 分、最多 50 个服务；这些限制可以收紧，不能放宽。invoice 付款和 service 续费检查客户归属及重复占用，供应商采购按稳定账户选择并检查账户租约上限。

`unknown_supplier_write` 只能指向独立 unknown 账户，数量由 profile 的 `unknownSupplierWrites` 限制；未知 kind、未知字段、跨客户 invoice/service、重复 flow、耗尽配额均失败，不会静默改派到其他客户、服务或供应商账户。

这是**allocator contract**：模块只返回内存中的 allocation lease 和脱敏 snapshot，不写 PostgreSQL、不启动 API/worker、不调用外部服务，不生成 `paid`、`succeeded`、通知发送或其他静态业务成功。snapshot 永远包含 `capacityVerified=false` 与 `businessSuccessGenerated=false`。固定 profile、真实业务请求、worker 排空、资金/资源不变量和 G09 容量 PASS 仍须由后续受控 controller/observer 单独证明。

验证：

```text
node --test scripts/g09-capacity-allocator.test.mjs
```
