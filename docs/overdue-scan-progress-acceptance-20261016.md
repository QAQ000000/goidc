# 逾期暂停候选扫描推进验收（2026-10-16）

## 问题与范围

`EnqueueOverdueServiceSuspensions` 先按账单/服务顺序选取至多 100 个候选，再记录不支持 suspend 的交付模式审计。已审计的 supplier 服务仍符合候选查询，所以前 100 项可以永久占据扫描窗口，阻塞后面的可暂停服务。

本次仅修复候选推进与对应审计去重，不为 supplier 开通 suspend。只创建既有的暂停任务，保留 24 小时宽限、导入审核、未付状态、忙碌任务和幂等键约束；真实交付动作仍由原 worker 处理。

候选查询在 LIMIT 前排除“当前交付模式仍不支持 suspend，且相同服务/账单/交付模式已记录 unsupported 审计”的组合。手动动作接口和调度扫描共用内存能力规则表，不增加每轮全表 DISTINCT 查询。当前仅 supplier 受限，manual/simulation/nokvm 行为沿用原规则；未来为某模式增加 suspend 支持时，旧审计自动停止排除该模式。模式切换也按当前模式重新评估，旧审计继续保留。

写入审计时按服务/账单/交付模式去重，与候选排除口径一致。批次仍先按账单 ID 锁定全部候选账单，再按服务 ID 锁服务；在服务锁内重新判断能力、读取审计并复核忙碌状态。并发工作者不重复记录证据或创建多份暂停任务。

## 回归覆盖

- `TestOverdueSuspendProgressPastUnsupportedBatch`：100 个 supplier 位于 manual/simulation 前；首批仅审计，次批必须为后两项建任务；重复扫描不重复建任务或审计，supplier 保持 active 且没有任务。
- `TestOverdueSuspendProgressReconsidersChangedModes`：supplier 已有审计后改为 manual/simulation，同一账单必须重新入选，旧证据保留。
- `TestOverdueSuspendProgressEvidenceMatchesCurrentMode`：历史其他模式审计不能屏蔽当前 supplier 的新证据；记录后下一批能推进。
- `TestOverdueSuspendProgressConcurrentInvoices`：同服务多个账单、两个服务与账单交叉关联、四个并发扫描器、有限批次持续推进；每个不支持的服务/账单只记一条证据，可暂停服务只建一个任务。

## 验证记录

环境：新建隔离测试库及专用测试角色，`REQUIRE_INTEGRATION=1`；每个 HTTP 集成用例创建随机 schema，执行迁移并在结束时删除。只读核实 `server_version_num=180000` 且库名以 `_test` 结尾。连接仅从工作树外的权限 600 私有 env 加载，未写入提交。

先保留原生产实现，运行：

```sh
scripts/go.sh test ./internal/httpapi -run '^TestOverdueSuspendProgressPastUnsupportedBatch$' -count=1 -v -timeout=5m
```

真实失败（3.664s）：`supported services behind 100 unsupported services did not progress: queued=0 err=<nil>`，证明原实现第二轮仍停留在前 100 项。

修复后运行：

```sh
scripts/go.sh test -race ./internal/httpapi -run 'TestOverdueSuspend|TestOverdueScheduler|TestBillingNotificationsAndOverdueSuspension' -count=1 -v -timeout=10m
scripts/go.sh vet ./internal/store ./internal/httpapi
git diff --check
```

专项 race 通过：10 个顶层用例及 supplier/nokvm 两个子用例，`httpapi 21.471s`，没有跳过；覆盖上述四项新回归、原有 supplier 手动动作边界、支持模式实际模拟暂停、重复调度、付款后撤销和付款/调度锁顺序。vet 与 diff 检查通过。无数据库时的预编译与跳过结果未计入此验收。

## 验证边界

只允许隔离测试库与每用例随机 schema。未访问业务数据库、真实供应商或真实 SMTP/SMS；未部署或重启业务服务。专项只证明数据库调度和任务入队，不证明第三方实际暂停成功。主代理负责合并后的全量回归与最终验收。
