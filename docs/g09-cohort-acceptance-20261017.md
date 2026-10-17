# G09 600 任务来源、双 worker 与独立对账验收

状态：**有限来源前置验收 PASS，G09 容量和总体接管目标仍开放。** 固定源码 `2f321067b472e49be9765477a628b29585b6c06c`，隔离 PostgreSQL 18.0；真实 Go Handler、loopback SMTP/SMS/魔方协议接收器与两个实际 `cmd/worker`。没有启动封存候选或执行 27 分钟容量 profile。

## 本批结果

第四轮 `run4` 强制 opt-in race 退出 0，无 SKIP；用例 243.64 秒、HTTP 包 244.677 秒。独立票据 pilot 16.44 秒通过；两个 schema 均先用只读 Prepare 解析实际共用的 10 条附加断言，之后才准备身份与来源。

150 个客户通过真实 API 各采购两次、建一张票：270 个 simulation create、30 个 supplier create（10 账户各 3）、150 个邮件和 150 个短信任务。正式来源串行耗时 **24.672 秒**，未超过固定 120 秒。每次写请求发送前保存原 payload/key，未知结果不换键重买；身份、部门与商品准备单列，不冒充注册或采购吞吐。

| 独立只读边界 | simulation | supplier | 邮件 | 短信 | 结果 |
| --- | --- | --- | --- | --- | --- |
| sealed_pending | 270 pending/attempt0 | 30 pending/attempt0 | 150 pending/attempt0 | 150 pending/attempt0 | PENDING，不变量通过 |
| first_drained | 270 succeeded/attempt1 | 30 succeeded/attempt1 | 150 sent/attempt1 | 150 sent/attempt1 | PASS |
| final | 270 succeeded/attempt1 | 30 succeeded/attempt1 | 150 sent/attempt1 | 150 sent/attempt1 | PASS |

双 worker 首次排空 **77.514 秒**。四类真实分钟调度任务在首次排空之后各有至少两次 succeeded、affected_count=0，最早/最晚 started_at 至少相差 55 秒；未修改时间、租约或生产调度。首次成功到正常停机约 103 秒，worker 全程约 180.484 秒，低于包含稳定性和停机在内的固定 600 秒预算。

实际测试逐来源检查订单/账单/服务归属、付款/账本、库存、任务与审计；150 客户各余额 8000 分、完整账本 3 条，11 个商品库存 0。邮件/短信恰好 300 次协议接纳，正文/收件身份 hash 与持久 journal、SMS 加密响应证据匹配；供应商逐资源 create/pay 事实及协议 hash 匹配。排空后资金、权益和通知稳定投影未变。公开文件可直接重算任务边界和摘要；私有正文、原 mutation、完整账本及协议 journal 已随测试临时目录清理，行级与协议判断由固定源码测试完成，不能据公开投影重建全部原始私有事实。

共 77 次独立 verifier/PGObserver 观察（每次完成后至少再间隔 2 秒，三个额外边界单列），全部可用的库级 deadlocks 为 0、StatsReset 未变。供应商正常周期 sync 最终 30 个可复用行，独立归类，不混入 600 分母；没有完整 attempt history，不声称 periodic exactly-once 或全局永久零队列。

wrong-owner 负例只改 manifest 的 invoice owner，实际只读 purchase SQL 拒绝并永久锁定 FAIL，不修改数据库。两个 owned worker 均 TERM/Wait 正常退出，PID 消失、账户租约释放；schema 集合恢复，常驻 API/worker/Web 身份未变，前后 API/Web 均 200。

worker 脱敏日志保留 **2 条 SQLSTATE 23514**，hash 均对应已知 `renewal_refund_hold` 保护；领取事务回滚后正常轮询完成任务。154/155 保护未改。此轮不满足“零 worker ERROR 日志”的额外声明，未设置或事后增加这样的门槛。

## 失败与修复

| 轮次 / 固定源码 | 实际结果 | 定位与处理 |
| --- | --- | --- |
| run1 / 8962873 | FAIL，140.58 秒 | 附加 SQL 布尔断言失败，旧诊断不足；保留原结果，不追认正常 TERM 证据 |
| run2 / 5a2db1a | FAIL，143.03 秒 | purchase JOIN 只按 service，可能误取周期 sync；将冻结 create ID 同时放入 JOIN，保留全部断言 |
| run3 / f7dd58a | FAIL，141.68 秒 | supplier 参数首处 text、后处 bigint 比较导致解析失败；改为 bigint 后转 text，并提前 Prepare 同一实际 SQL |
| run4 / 2f32106 | PASS，243.64 秒 | 三边界逐 600 核对、真实分钟周期及负例、清理均通过 |

前三轮 FAIL 原始证据均保留，不覆盖、不放宽预算，也未改生产状态机。

## 组合回归与证据

同一固定 `2f32106` 顺序独占 PG 执行：

- Node 72/72，通过、失败/取消/跳过/待办均 0。
- `internal/g09fixture` 全部 21 个顶层 race 测试通过，包 3.720 秒；PGObserver opt-in 明确启用。
- HTTP 4 条来源小链加 3 组 guard，共 7 个顶层 race 测试通过，包 17.196 秒；source API、双 worker、supplier、易支付各 opt-in 明确启用，无 SKIP。
- `scripts/go.sh build ./...`、`scripts/go.sh vet ./...`、`git diff --check` 退出 0；schema、常驻身份、健康及源码未变核对通过。

Node 当前使用 spec reporter 的 `ℹ` 汇总格式；派生结果从未修改的原日志重读 72/72，支持 `#` 与 `ℹ`，未以计数不可用冒充 0。该批只改测试与文档，无 UI、迁移或生产逻辑变更，不重复前端/浏览器及已在 `c6d2314` 通过的强制产品全量；后加 fixture 专项不能追认为旧全量包含的文件。

原始 [run4 结果](evidence/g09-cohort-20261017/run4/result.json)、[三边界逐任务投影](evidence/g09-cohort-20261017/run4/boundaries.json)、[日志及摘要索引](evidence/g09-cohort-20261017/run4/index.json)、[组合回归](evidence/g09-cohort-20261017/combined/result.json)及[独立复核](g09-cohort-independent-review-20261017.md)共同限定本批证明范围。

## 剩余方向

下一批先接同一封存候选、普通用户 namespace、宿主 TCP SQL fixture 与同 namespace 协议进程，验证迁移、健康和有界退出；再接独立 seed 客户端及完整 1000 客户/2000 服务/10000 账单、混合请求、进程/数据库资源联合门槛和 27 分钟固定 profile。保持 [既定接入边界](g09-fixture-contract-20261017.md)，不放宽 observer 身份或 measurement executor 的金额、路径和数量规则。

未部署、未推送、未重启常驻服务、未写业务库或调用真实外部资金/资源写接口。此轮不证明真实商户收退款、真实云主机、终端收件、公网 HTTPS、RTO/RPO 或生产切流；105 页仍 45 完成 / 60 部分。
