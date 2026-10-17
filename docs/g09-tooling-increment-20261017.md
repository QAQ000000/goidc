# G09 容量验收基础工具增量

状态：工具增量已实现，完整容量验收未执行，G09不关闭。未构建/启动新候选、未部署、未修改常驻服务或业务库。真实用户收件、真实商户付款/出款不在这些loopback测试证明范围。

执行口径见[容量计划](g09-capacity-drain-plan-20261017.md)及[精确契约](g09-fixture-contract-20261017.md)：最多27分钟阶段窗口，600个有限成功任务与正常供应商周期背景分开统计。禁止为制造零队列删除服务、修改调度或提前数据库租约。

## 已实现

- `scripts/g09-load-lib.mjs`：固定开环slot、有界并发/操作数/超时，不追赶或自动重试；显式登记漏发、失败、未完成与原计划/实际派发时间。请求和inflight排空均用绝对deadline，timer延后不能把迟到200计成功。业务事务单位和实际HTTP请求分别统计，延迟包含记录器及调度开销；分阶段窗口包含该阶段拖尾。
- `scripts/g09-scenario-lib.mjs`：执行明确分配的余额付款、本地续费、本地/供应商采购与创建工单链。每条链通过真实 `/me` 验证客户身份；付款前读取账单，核验归属/金额/未付状态/版本，提交 `expected_version`。购物车采用实际版本保护；其后端不支持幂等回放，未知结果尤其不能换键重买。
- 客户预算来自冻结的唯一身份列表，每客最多50条写事务、每笔最高1000分，预算预占不返还；原mutation在派发前由同步私有记录器保存原body/key，普通入口禁止重复派发同key。任一已尝试mutation的链失败即冻结资源，无普通解冻入口；`stop()`拒绝新链并abort在途请求。
- HTTP目标只允许显式IPv4 loopback origin，禁止重定向；只支持白名单路径及数值分页参数，有界请求/响应/请求总数。公开记录只含meta/hash，不复制cookie、原body/key、远端错误和自由查询串。私有记录器必须同步持久化，controller仍须实际落实0600文件、fsync及私有目录。
- `internal/g09fixture`：真实SMTP及generic HTTP JSON短信接收器，成功响应前持久化脱敏协议记录；固定接受预算、SMTP连接预算、持久化失败锁定；精确控制消息/序号校验、私有JSON独占创建和原子替换。生产二进制未导入本包。详见[包说明](../internal/g09fixture/README.md)。

## 验证边界

Node用例覆盖实际loopback重定向拒绝、日志阻塞超时复现、排空截止后的微任务、未知链冻结、独立客户预算、付款前金额及身份核验、版本请求、HTTP409、原请求保护和证据去敏。Go小包race只覆盖协议接收器/控制文件，不连接PG。执行日志与退出码另存[工具证据](evidence/g09-tooling-20261017/)。已有强制全量使用启动时的固定快照，后续工具修补专项单列，不能追认包含新增文件。

量化分位数为直方图保守上界；`null`表示没有样本，必须先用 `Number.isFinite`检查，不能利用JavaScript的`null <= limit`当通过。阶段SLO使用`phaseCategories`，同时检查计划数、接纳比例、failed/dropped/timeout/unfinished；负载库inflight drain不代表worker业务排空。

## 继续实现

外部付款签名/重复回调事务、Web/静态资源请求分配、完整账号/账单/供应商商品准备、600任务来源及协议逐实体关联、两个真实worker、SQL资金/权益全量核对、周期背景/资源采样，以及普通用户namespace内同一封存候选API/Web/Nginx/worker控制器仍待完成。当前scenario返回 `deliveryVerified=false` 和 `accountingVerified=false`；HTTP链成功不能据此宣称开通、投递或对账成功。

后续先以opt-in真实Go HTTP→Node小链验证请求契约，再固定干净候选执行小规模试跑和预先冻结的27分钟profile。source API试跑也不能代替最终候选容量验收。
