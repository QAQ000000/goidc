# G09 有界进程采样时窗增量

本增量仅增加进程观察辅助库，不实现容量controller，不运行容量验收；所有结果固定 `capacityVerified=false`、`candidateVerified=false`。不访问PG，不构建候选，不启动或重启常驻服务。

`scripts/g09-sampler-window.mjs` 导出异步 `createG09SamplerWindow`。调用方提供自己启动的直接子进程期望身份 `children`（沿用已有sampler注册字段）、`clockTicksPerSecond`、私有 `journalPath`、`stop`回调和完整 `profile`，返回一次性 `run()`。子进程必须在注册前由调用方独立核对候选摘要；库只调用已有 `captureG09OwnedProcess/createG09ProcessSampler`，不扫描进程、不读取命令行/环境、不自行发送信号。

profile字段：`startAtMs/endAtMs` 使用同一单调时钟，`maxSamples` 必须精确等于半开窗口内2秒slot数量；最大30分钟、1000样本、16直接子进程。`maxScheduleLagMs` 必须小于2000ms。资源停止阈值 `thresholds` 必须全部明确：`rssBytes/threads/fdCount/cpuPercentOneCore` 为正有限上限，`requireIO` 为布尔值。注册后profile独立拷贝并冻结，运行期间不能放宽预算。首slot为start，其余按start+N×2000ms计算；迟到超过固定预算立即失败，不补追。样本读取和持久化越过slot或终点也失败。

journal父目录必须真实绝对路径、当前UID所有、0700；文件独占创建0600，拒绝已有文件或symlink路径，初始文件及父目录fsync。每条profile/sample/summary记录完整写入后fsync；每次写入核对文件mode、owner、link数及路径inode。仅持久化已有sampler的脱敏投影，不输出注册中的cwd/executable等路径。第一条CPU差分保持null，不当0；缺失内核I/O保持 `ioAvailable=false/io=null`，需要I/O时立即资源失败。未提供Go heap/GC、宿主磁盘/内存、PG或外部网络观测。

采样/身份变化/调度漏采/时钟倒退或非有限值/持久化失败/资源越线均锁定失败，并且至多一次调用停止回调。回调只得到固定错误码，不接收任意PID或原始异常。调用方负责在回调内先停止新写，并对自己已验证owned进程执行有界退出；本库不能保证任意外部回调的完成时间。失败journal可能不可写，调用方还须保存返回FAIL和独立清理证据；不声称此时已有durable失败尾记录。调用方取得句柄后必须执行一次run，不能丢弃打开的journal。

成功必须等到冻结终点，核对 `planned=attempted=persisted`，missing为0；否则不报告complete。这里只证明采样slot与durable样本数量守恒，不证明600任务/业务/资金/候选/容量守恒。阈值是单PID上限，不替代宿主预算或完整安全停止条件。

## 验证

`node --test scripts/g09-sampler-window.test.mjs scripts/g09-process-sampler.test.mjs`：15 PASS、0 FAIL、0 SKIP。真实owned Node子进程覆盖2秒壁钟窗口、null CPU、I/O可用性、CPU/RSS/FD、真实cwd身份变化及进程退出；确定性时钟覆盖冻结预算、防追赶、倒退和NaN；真实文件覆盖0600与权限破坏、已有文件/不安全目录拒绝；资源越线调用一次stop。进程均由用例自行TERM并wait清理。日志见本增量独立evidence目录。

初轮12项有1个断言FAIL：浮点单调时间的两个slot相减得到1999.9999999999998而非精确2000。改为小于1e-6ms的断言容差，调度实现仍是固定2000ms。随后扩大至15项通过。没有用本专项替代主线强制全量或最终容量实测。

后续真实时钟用例曾14/15 FAIL；进一步增加结果诊断后确认是注册/初始化fsync占用预留时间，导致首slot已超过冻结延迟预算。保留失败日志（路径已脱敏）。测试在运行前将首slot准备余量设为3秒、调度延迟预算1秒、最后slot余量1秒后重新执行；库仍拒绝任何执行中放宽预算或补追。
