# G09 目标容量与积压排空：只读差距及最小执行批次

状态：计划，未执行容量验收。初稿只读源码基线 `5179259`；后续[精确实施契约](g09-fixture-contract-20261017.md)修正了周期背景、cohort准备和API来源口径，本文已同步。开环负载库已实现并有单测，完整fixture/controller及候选容量实测仍未完成，未触碰常驻服务。本计划不承诺生产容量。容量目标须在执行前形成固定 profile；下面数值不能事后随结果下调并称达标。

## 已有工具与实际缺口

| 可复用材料 | 能复用的能力 | 不能借用的结论 / 必要适配 |
| --- | --- | --- |
| `build-release-runtime-candidate.mjs` / `verify-release-runtime.mjs` / `accept-release-runtime.mjs` | 固定提交/产物摘要、独立候选、运行依赖与实际进程身份、普通用户、私有命名空间、loopback及可变路径审计、owned process清理 | 构建与启动证明不是吞吐证明；本阶段不执行构建。后续用相同干净候选启动负载环境，不能复用已删除历史候选路径。 |
| `release-runtime-acceptance-lib.mjs`、`release-runtime-builder-lib.mjs` | `validateTestDSN`、独占端口/目录、PID/路径审计、资源预算、证据写入及摘要辅助函数 | 新容量控制器必须沿用拒绝非loopback、非PG18、非`_test`和私有输出规则；不能直接调用旧fixture后任意扩充业务而保留旧安全断言。 |
| `accept-release-dual-web.mjs` / `release-dual-web-scenario.mjs` | Nginx→Web→API链路、统一Origin/会话、持续读探针、有界在途排空和清理方法 | 现有观察器顺序请求、轮次间100ms暂停、上限4000条，属于切换可用性探针，不是开环负载器。控制器15分钟、fixture活跃10分钟，不够下述25分钟批次；新入口需独立deadline，不可直接套旧窗口。HTTP在途排空不等于业务任务积压排空。 |
| `internal/store/release_runtime_fixture_test.go` | 随机schema、迁移集合校验、前后财务摘要、白名单子进程与清理协议 | 原fixture明确禁用通知、禁止其他到期业务，且只容许最小业务变更；不能原封不动拿来做混合通知/采购负载。 |
| `supplier_worker_acceptance_test.go`、`supplier_worker_sigkill_test.go`、`notification_worker_sigkill_test.go` | 真实cmd/worker子进程、loopback供应商/SMS、请求计数、账户租约、未知写冻结、恢复和独立员工流程 | 既有强杀测试SQL加速五分钟租约/退避，不能作为真实排空耗时。可复用fixture实现和断言模式，需新opt-in容量测试而非修改原测试含义；SMTP接收器需补齐。 |
| `scripts/test-worker-recovery.sh`、`scripts/test-all.sh` | 强制实际执行/PASS检查、REQUIRE_INTEGRATION、无缓存、明确超时 | 不执行容量场景。仓库未找到专用load/bench/stress脚本或已签容量报告；不能声称现有负载入口已可运行。 |

worker生产约束：`cmd/worker/main.go:135` 每2秒进入主循环，同进程串行处理最多10个SMS、10个email、20个provision job；网络耗时会占用这条串行链，循环时限不是严格2秒。自动对账为10秒独立循环，到期/账单/暂停调度为分钟循环，自动续费是独立分钟循环并带每日锁。`internal/store/store.go:32` 每个Store连接池固定MaxConns=10，不能把配置DSN里的更大值当有效池容量。供应商账户租约会串行限制同账户动作，因此必须分开记录账户数，不能仅增加worker数就推定扩容。

## 最小完整批次

新增一个独立opt-in容量fixture/controller和开环负载器是执行前必要工作，本阶段不改实现。入口建议 `scripts/accept-g09-capacity.mjs --profile <absolute-json> --candidate <absolute-path> --manifest-sha256 <sha> --execute`，无execute只校验/输出计划。入口名称只是计划，不是现有命令。拒绝缺少固定profile、候选摘要、隔离预算或未匹配实际执行PASS的报告。

采用同一固定候选的API、两个真实worker、独立Web与独立Nginx；普通UID与私有namespace沿用release runner隔离。端口必须动态分配并记录，不能借用常驻8088/3088或已有验收监听。一个独占随机schema贯穿单轮；新一轮使用新schema。fixture白名单环境、不读取项目.env；禁止外部网路，支付/供应商/SMTP/SMS仅loopback计数协议夹具。私有Cookie/密钥/manifest单独0600，不进公开报告。容量测试用普通构建运行；race回归另列，避免将race性能当生产性能。仅httptest Handler的试跑必须标为source API，不满足最终候选容量门槛。

首轮数据候选：1000客户、2000服务、10000账单，至少覆盖最近/中段/末段分页；10个供应商测试账户与多账户独立服务，热点占20%、其余分散。种子SQL只负责历史基数、明确业务可用状态；测量窗口内新账单、付款、回调、续费与任务须经真实API/业务事务建立，不能直插succeeded/effect/ledger伪装经营吞吐。预先记录客户余额/信用、库存、账单与不可变事实基线，设置有限足够库存和余额，耗尽不允许静默补款或改用其他池。种子耗时不计测量吞吐。

| 阶段 | 时长与负载（首轮候选） | 目的 |
| --- | --- | --- |
| 预热 | 2分钟，10 read/s、0.2业务事务/s | 预热连接/JIT/缓存，验证签名/会话/路由，确认负载器未丢调度；单列，不混入稳态分位数。 |
| 稳态 | 10分钟，30 read/s、1业务事务/s | 同时运行两个真实worker及所有正常timer。业务事务比例40%支付结算/重复回调、30%本地续费、20%本地或供应商模拟采购、10%工单及通知。每个事务实际发出的HTTP请求数另计，不能把1事务称为1HTTP。 |
| 峰值+积压 | 3分钟，60 read/s、2业务事务/s；首60秒仅停止owned workers，API继续 | 产生实际已接纳业务积压，随后恢复两个owned workers，观察恢复吞吐与API尾延迟。不要SIGSTOP在持锁事务里；worker须已TERM退出再开始计暂停窗口。 |
| 独立cohort准备 | 最长2分钟，10 read/s，停止正常业务流量、TERM并wait两worker后创建600任务来源 | 300笔采购、150张独立部门工单真实入队；准备不计峰值吞吐，超时即失败。 |
| 停止新写与排空 | 最长10分钟，保留10 read/s、停止新业务流量，恢复两个worker | 有限600任务全部成功，正常supplier周期背景保持有界；保留1例指定人工核验冻结写任务，单列且不得自动重发。最大阶段时长合计27分钟。 |

read组成：匿名商品/目录30%、客户账单与服务详情40%、员工账单/任务分页20%、Web HTML与带摘要核验的静态资源10%。预建有效会话；登录/密码哈希/限流单独少量冒烟，不用共享单IP高频登录扭曲业务容量。HTML、API、静态资源分别汇总，不用大量缓存静态请求掩盖动态API尾延迟。下载/大文件、真实外网、正式HTTPS与切流容量不在该首轮范围，报告明确未验证。

“峰值暂停”自然生成的积压可能太少，另加固定有界 cohort：300个独立provision任务（270 simulation、30 supplier）、150封邮件、150条SMS。采购和工单均经真实API；150个独立部门的唯一收件员工各有已验证邮件/短信地址与双渠道偏好，一张工单恰好生成两条queued delivery。先在新schema执行pilot核准数量。不能用直接SQL任务插入、测试发送接口或queued状态代替成功终态证明。cohort准备在独立120秒窗口，超时即FAIL；若另选store入队替代，只能明确标worker-only，不能纳入API端到端口径。

通知和到期候选有限，minute timer正常运行；supplier周期sync没有独立开关，成功活跃服务会每5分钟重新入队，必须保留并单列背景。不能通过删除/终止服务或修改生产调度制造全局零队列。若测自动续费，seed及arm须在worker首次启动前完成，不清每日锁。已知unknown写用独立供应商账户，避免该账户租约阻塞普通cohort。恢复/排空阶段不SQL修改available_at/locked_at/lease时间，不直接调用Recover函数代替实际worker。

## 目标、统计和停止条件

首轮候选门槛：稳态/峰值按类别分别报告，read p95≤500ms/p99≤1s、写请求p95≤1s/p99≤2s；非预期失败≤0.1%，开环调度漏发≤1%，实际接纳业务事务≥计划的99%。这些是待执行的profile阈值，不是已达指标；若达不到，应报告真实瓶颈/最大已验证阶段，不能把低于发出速率的成功吞吐称为目标达标。压力发生器自身CPU/调度延误、并发上限、超时与未发出数都入报告；禁止无界并发和静默丢弃，不能用闭环并发掩盖coordinated omission。

p95/p99按请求类别、阶段计算，保存数量/最小/最大/直方图边界或受控逐请求记录。普通错误、超时、断连、调度miss分别计；重试另外标记原attempt，不把重试成功覆盖原失败。业务冲突409不自动统算成功：预期负例单列，经营负载意外409仍是未接纳。负例401/403、未知窗口由单独确定性场景产生，不参与正常延迟SLO的分母，却必须纳入总请求清单。

每2秒采样各队列pending/running/sending/retry-wait、最老可执行任务年龄、延迟任务数量/最老created年龄、waiting_manual/waiting_verification/dead_letter及派发/账户租约。ready backlog与future retry不能混淆，不能把任务移入dead_letter或unknown当“成功排空”。以冻结cohort ID为分母追踪created→claimed→终态及每次attempt，无任务失踪。候选排空门槛：停止新写后10分钟内600个预期成功cohort全部成功；最后两个完整minute窗口每次采样全局ready+running/sending≤100、future≤100、最老ready≤15分钟；无新增非指定死信/人工冻结/failed/expired，指定unknown原样保留。跨两个真实minute tick终态与资金副作用不变，不要求周期背景永久归零。排空起点同时记录最后来源commit及worker恢复时刻，包含真实等待。periodic同job ID可复用且attempt重置，不能仅用ID集合证明只执行一次。

同时采集API/worker/Web/代理逐PID CPU、RSS、FD、线程、IO、Go GC/heap（若无现有安全指标入口则标未采，不虚构pprof端点），PG连接/active/idle-in-transaction、锁等待与死锁、事务/回滚、IO/WAL及宿主CPU/RAM/磁盘余量。建议每2秒采样、每10秒汇总；查询监控也计自身开销。短采样看不出瞬时峰值需明确。只取允许字段，不输出query正文/DSN/凭据。当前无统一Prometheus/pprof端点，首轮可复用/proc与PG统计，不能宣称已具备完整GC/连接池等待可观测性。

立即停止新写的硬条件：任意资金/权益不变量破坏、出隔离范围的网络请求、进程指向常驻目录/非目标schema、PG deadlock、负载发生器失控、磁盘/内存越过执行前budget安全线。停止后保留证据并有界终止owned进程，不为追求排空率自动解除unknown或抹掉失败业务。

## 必须全量核对的资金与资源不变量

- 按客户验证余额起点+全部ledger delta=终点；每张成功账单恰好一次结算、账本参考和幂等键一致；信用used/remaining与已接纳消费一致，不能以最终总额抵消客户间错账。
- 重复/乱序/迟到回调只对应一次合法经营生效；成功payment、invoice paid_at、订单及原始记录保留。外部退款不充值客户余额；unknown外部写保持冻结，不为补吞吐自动重发。
- 本地续费effect与真实延期逐张对应，期限/优惠次数符合实际期数；退款若纳入固定小cohort，恢复before_due/before_used、auto关闭/generation增加一次，hold与refund终态一致。首轮性能事务不必混入复杂退款，但开跑前后需跑有限真实API哨兵验证保护。
- 订单数量、单位服务数量、库存扣减/释放逐项一致；pending hold不得新增采购/配置/取消语义写入；供应商create/pay调用计数逐job核验，不能用请求总数代替“无重复采购”。
- outbox任务/attempt/送达证据与fixture请求ID对齐：通知是至少一次尝试，已注入丢响应允许且记录重复投递，不能称exactly-once。普通无故障cohort应恰好一次成功，所有重试保留可解释来源。
- 审计按actor、invoice/job/refund ID关联，业务失败事务无半成品副作用；排空后未完成集合可逐ID解释，不只记录count=0。跨真实timer/owned-worker重启终态不再变化。

## 隔离证据与交付格式

每轮独立 `run_id`，不可覆盖旧FAIL。公开目录建议 `docs/evidence/g09-capacity-<date>/<run_id>/`；写 `profile.json`（计划速率/组成/时长/阈值/数据规模/账户数/worker数/最大并发/超时/budget）、`manifest.json`（提交/dirty=false/迁移checksum/候选摘要/版本/配置存在性）、`requests.jsonl`（阶段、类别、计划时刻、实际开始结束、HTTP状态/业务码、trace ID，脱敏）、`samples.jsonl`（资源/DB/队列）、`invariants-before.json` / `invariants-after.json`（逐实体或可复核受控索引）、`fixture-counters.json`、`summary.json`与`cleanup.json`。

summary必须含planned/offered/started/completed/accepted/succeeded/failed/timed_out/dropped、分阶段类别吞吐/分位数、积压最大值/最老年龄/实际排空曲线、预期冻结集合、所有门槛PASS/FAIL/未验证和停止原因。index逐文件字节数+SHA256，私有会话不索引发布。记录实际开始结束时间与退出码，不以“进程仍在跑”算通过；清理核对owned PID、监听、随机schema及临时目录不存在，原常驻进程身份/健康未变，共享PG不停止。

先做小规模脚本校验再执行上表固定profile；脚本校验不能当稳态通过。首轮若暴露瓶颈，保留FAIL并在独立变更修复后以新run重测。即使整轮PASS，也只证明该硬件、数据量、候选、loopback延迟和组合下的隔离已验证容量；正式生产SLO仍需目标硬件/数据分布/真实渠道时延、HTTPS与完整观测/RTO/RPO另行验收。不得沿用其他项目的吞吐数字或把450次切换探针换算成生产RPS。
