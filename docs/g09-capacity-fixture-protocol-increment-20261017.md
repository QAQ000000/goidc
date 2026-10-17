# G09 离线容量 fixture 协议

提交 `1aac142` 新增容量 fixture 状态机，独立于候选启动 fixture。它接受固定 450 个来源记录并通过既有 cohort 契约封存，映射为 300 个开通任务及 150 张工单产生的 300 个通知任务。来源注册跨批次拒绝重复业务实体；drain projection 按每个队列实体核对来源、ID、时间、状态、尝试次数及任务类型。

请求拒绝重复/未知字段、尾随 JSON、未知动作、错误序号和重放。状态转换必须经过 collecting、sealed、draining；不完整的合法投影返回 `PENDING`，缺失、伪造或非法投影冻结为失败。cleanup 只检查 fixture 状态，不删 schema/文件、不停进程。

主线验证：

```text
ZJMFGO_G09_CAPACITY_FIXTURE_PROTOCOL=1 scripts/go.sh test -race ./internal/g09fixture -count=1
scripts/go.sh build ./...
scripts/go.sh vet ./...
```

专项 race PASS，build/vet PASS。协议不访问 PostgreSQL，不驱动业务 API/worker/适配器；`CapacityVerified` 只描述传入投影满足 fixture 契约，不能作为候选容量、真实通知投递或生产 drain 证据。下一步仍须让受控 controller 从封存候选读取真实 source、切换 owned worker，并由只读 PG18 observer 生成该 projection。
