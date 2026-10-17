# G09 容量验收基础工具增量

状态：工具增量已实现，完整容量验收未执行，G09不关闭。未构建/启动新候选、未部署、未修改常驻服务或业务库。真实用户收件、真实商户付款/出款不在这些loopback测试证明范围。

执行口径见[容量计划](g09-capacity-drain-plan-20261017.md)及[精确契约](g09-fixture-contract-20261017.md)：最多27分钟阶段窗口，600个有限成功任务与正常供应商周期背景分开统计。禁止为制造零队列删除服务、修改调度或提前数据库租约。

## 已实现

- `scripts/g09-load-lib.mjs`：固定开环slot、有界并发/操作数/超时，不追赶或自动重试；显式登记漏发、失败、未完成与原计划/实际派发时间。请求和inflight排空均用绝对deadline，timer延后不能把迟到200计成功。业务事务单位和实际HTTP请求分别统计，延迟包含记录器及调度开销；分阶段窗口包含该阶段拖尾。
- `scripts/g09-scenario-lib.mjs`：执行明确分配的余额付款、本地续费、本地/供应商采购与创建工单链。每条链通过真实 `/me` 验证客户身份；付款前读取账单，核验归属/金额/未付状态/版本，提交 `expected_version`。购物车采用实际版本保护；其后端不支持幂等回放，未知结果尤其不能换键重买。
- 客户预算来自冻结的唯一身份列表，每客最多50条写事务、每笔最高1000分，预算预占不返还；原mutation在派发前由同步私有记录器保存原body/key，普通入口禁止重复派发同key。任一已尝试mutation的链失败即冻结资源，无普通解冻入口；`stop()`拒绝新链并abort在途请求。
- HTTP目标只允许显式IPv4 loopback origin，禁止重定向；只支持白名单路径及数值分页参数，有界请求/响应/请求总数。公开记录只含meta/hash，不复制cookie、原body/key、远端错误和自由查询串。私有记录器必须同步持久化，controller仍须实际落实0600文件、fsync及私有目录。
- `internal/g09fixture`：真实SMTP及generic HTTP JSON短信接收器，成功响应前持久化脱敏协议记录；固定接受预算、SMTP连接预算、持久化失败锁定；精确控制消息/序号校验、私有JSON独占创建和原子替换。生产二进制未导入本包。详见[包说明](../internal/g09fixture/README.md)。
- `scripts/g09-workload-report.mjs`：绑定负载开始时冻结的完整预算及phase计划；校验接纳/真实派发/完成/失败守恒、阶段和总观察时窗、绝对排空预算及固定直方图，按真实拖尾窗口重算吞吐。缺样本、篡改桶边界、缩短子统计窗口或拿更宽松profile实测后套更严格profile都不能通过。只评估work-unit结果，始终返回`capacityVerified=false`，实际HTTP子请求及资金/worker/候选身份另验。
- `scripts/g09-process-sampler.mjs`：只采controller明确登记的直接子进程，不扫描全机、不读取命令参数/环境、不发送信号。登记及每次采样前后核对PID/start ticks、父进程、UID、可执行文件路径、cwd和net/mount namespace；目标变化、退出、计数倒退、并发采样或预算耗尽后拒绝继续。输出CPU ticks/相邻采样单核百分比、RSS、线程、FD数量及可用的I/O计数，不发布路径。候选文件摘要和只读封存仍须由controller独立验证；本工具不证明candidate身份。

## 验证边界

Node用例覆盖实际loopback重定向拒绝、日志阻塞超时复现、排空截止后的微任务、未知链冻结、独立客户预算、付款前金额及身份核验、版本请求、HTTP409、原请求保护和证据去敏。Go小包race只覆盖协议接收器/控制文件，不连接PG。执行日志与退出码另存[工具证据](evidence/g09-tooling-20261017/)。已有强制全量使用启动时的固定快照，后续工具修补专项单列，不能追认包含新增文件。

后续增量的小包回归直接调用生产注册表中的SMTP/generic HTTP JSON provider连接loopback接收器，确认协议与证据hash匹配；它仍不运行worker、不claim真实outbox。Go→Node opt-in source API入口在`2c73f7d`实现，现已于`f5d69d2`独占PG18实测通过（HTTP包2.255秒、17次真实HTTP、4条业务链及SQL不变量），没有SKIP；schema清理、常驻PID身份和API/Web健康核对通过，见`source-api-pg-smoke/`。该轮前置全量退出1已单列保留，不能拿此专项替代全量。额外报告器及provider回归证据在`report-guard-increment/`子目录，与前一轮33项Node记录分开保存。

量化分位数为直方图保守上界；`null`表示没有样本，必须先用 `Number.isFinite`检查，不能利用JavaScript的`null <= limit`当通过。阶段SLO使用`phaseCategories`，同时检查计划数、接纳比例、failed/dropped/timeout/unfinished；负载库inflight drain不代表worker业务排空。

进程采样增量已通过49项合并Node回归，其中6项覆盖真实owned子进程和身份/退出/预算/并发拒绝，见`process-sampler-increment/`。当前宿主内核没有`/proc/<pid>/io`，实际记录`ioAvailable=false`、`io=null`及原因，不把未采集当零；其他读取或权限错误仍使采样失败。首个CPU样本无差分，同样返回null；CPU百分比基于`getconf CLK_TCK`和真实相邻观察窗口，可因多线程超过100%。controller仍须按2秒调度、持久化样本并执行预先冻结的资源停止门槛；这些还未接入，本轮不输出容量PASS。

## 继续实现

外部付款签名/重复回调事务、Web/静态资源请求分配、完整账号/账单/供应商商品准备、600任务来源及协议逐实体关联、完整负载的两个真实worker编排、SQL资金/权益全量核对、周期背景/PG采样与进程采样调度，以及普通用户namespace内同一封存候选API/Web/Nginx/worker控制器仍待完成。当前scenario返回 `deliveryVerified=false` 和 `accountingVerified=false`；HTTP链成功不能据此宣称开通、投递或对账成功。

后续先以opt-in真实Go HTTP→Node小链验证请求契约，再固定干净候选执行小规模试跑和预先冻结的27分钟profile。source API试跑也不能代替最终候选容量验收。

真实`cmd/worker`双进程小闭环已在`46b759b`实现。首次PG试跑退出1（47.779秒）：SMTP可选用户名省略后解析为`<nil>`，造成邮件发送失败、有限来源未全部成功；原FAIL保留于`source-worker-pg-smoke/`。`0792ffb`修正渠道缺省/null字符串并拒绝必填null，保持原SMTP安全校验；相同worker测试复验PASS（5.632秒），见`source-worker-fixed-pg-smoke/`：真实HTTP创建一个模拟开通任务和一组工单邮件/SMS，两个实际worker完成`succeeded/sent`，逐source/event/delivery/outbox的SQL事实、唯一余额/库存副作用及durable SMTP/SMS协议hash一致。TERM/Wait、schema回收和常驻PID/API/Web健康核对通过。不是600任务、正常minute tick稳定性、封存candidate或容量验收，G09保持开放。
