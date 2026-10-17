# G09 容量 controller 增量

本增量新增 `scripts/g09-capacity-controller.mjs`，把固定 profile、资源投影、allocator contract 和已有 `runG09Load` 组合为一个有界 controller。

`createG09CapacityController` 只接受内存资源投影：1000 个 customer、2000 个 service、10000 个 invoice、10 个正常 supplier account 和独立 unknown supplier account。投影键是封闭集合，controller 不接收数据库连接、查询函数或生产配置，也不会访问 PostgreSQL、常驻 API 或 worker。

`parseG09CapacityProjection` 是 Go `g09projection` plain JSON 与 controller 之间的唯一边界。它只接受五个顶层键及 allocator 所需的最小字段，校验固定数量、正整数 ID、唯一性、客户归属、非负余额和 unknown 账户隔离；凭据、数据库/连接字段、额外字段以及 `capacityVerified=true` 或 `businessSuccessGenerated=true` 都会拒绝。`createG09CapacityControllerFromProjection` 先调用该适配器，再把冻结的内存投影交给 allocator。

`preflight()` 是默认路径，返回 `PREFLIGHT_ONLY`，并固定 `candidateVerified=false`、`capacityVerified=false`、`businessSuccessGenerated=false`。`run()` 必须每次显式传入 `execute:true`、`/tmp` 或 `/dev/shm` 下的临时 run root、loopback profile、performer、生命周期适配器和 observer；缺任何一项都在启动前拒绝。

显式执行时，controller 调用 `start -> observer.start -> runG09Load`。负载完成或发生异常后，先中止后续工作，再调用有界 `drain`，最后调用 `stop`，并收集 observer 快照和关闭结果。失败关闭返回 `FAILED_CLOSED`，不会把 runner 的失败、空样本或业务响应改写为 capacity/business success。HTTP 子请求、worker 队列、资金不变量和最终容量 SLO 仍需独立 observer/验收证明。

验证：

```sh
node --test scripts/g09-capacity-controller.test.mjs
node --check scripts/g09-capacity-controller.mjs
git diff --check
```
