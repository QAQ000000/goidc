# G09 候选运行前置验收

固定源码 `78b4d1e15010d9281b16b61e5bf1add680a4dd46`，封存清单 SHA-256 为 `859230a9347ef5bff6b0da39711b0f255b2dab275fb694e90851b2b2895fb07f`。候选包含 3,060 个文件、157 个迁移，artifact 合计 251,537,218 字节。完整输入、运行输出、负例、回归日志和逐文件摘要见[证据索引](evidence/g09-runtime-bootstrap-20261017/index.json)。

## 结果

首轮 `runtime-run1` 保留 FAIL。夹具创建隔离 schema 并正常清理；真实 namespace 未进入内层，原因是 sealed 候选不包含空 `.next/cache` 目录，而只读挂载后 bwrap 无法创建绑定目标。独立无 PG 探针复现 `Can't mkdir ... Read-only file system`。修复只在独立 run copy 创建空挂载点，不改 sealed 候选；修复后的合成探针进入 `migrate-first`，连接不存在的模拟 PG 端点后预期失败。

第二轮 `runtime-run2` PASS：

- 专用 PostgreSQL 18 `_test` 库上创建随机 schema；候选真实执行两次完整迁移，两次都核对 157 条 migration 版本/checksum 与相同 SHA-256。独立只读 observer 记录两次快照。
- 普通 `www` UID、capabilities 全剥离、独立 PID/network/mount namespace 启动 API、Next Web 和两个真实 worker 进程。严格采样验证进程身份并采两次样；API ready 与页面/资源请求通过。
- `/`、`/login`、`/admin-login` 都返回 200，21 个构建清单中的真实静态资源逐一返回 200 并匹配封存摘要。
- 对 api、web、worker_0、worker_1 逐一验证同一实际 PID 的成功 `execve`、manifest executable/argv 和二进制摘要。共 55 个 syscall trace 文件通过文件/网络边界审计；trace 原文保留在本机私有 run 目录，报告只给逐文件摘要。
- 四个 owned 进程正常 TERM/Wait 退出，监听端口释放。fixture 通过已确认的 `finish` 删除原 OID 对应 schema；done 文件 UID/0600/无链接检查通过。源码 HEAD/proof、candidate manifest 摘要、原 schema 集合、三个常驻服务的 PID starttime/executable 及 API/Web 健康最终复核均通过。

## 范围

这是候选运行启动前置，不是容量验收：报告明确 `capacityVerified=false`。两个 worker 只证明实际启动、存活采样和有界退出，没有业务任务 ready 或处理证明，故 `workerReadinessVerified=false`。没有历史数据 seed、目标规模负载、27 分钟 profile、完整后台队列、执行中强杀/崩溃恢复、公网 HTTPS、真实第三方交易、生产切流或回滚验收。G09、G04及核心替代总体目标保持开放，105页状态不变。

未部署、未推送、未重启常驻服务、未写业务 schema、未调用真实资金/供应商写接口。隔离 schema 已删除，常驻 API/worker/Web 身份和健康前后不变。复核通过的控制器改动在分支 `codex/g09-controller-bootstrap-20261017`，本工作区提交 `91b6e11`；本说明在候选执行后新增，候选本身仍绑定上述固定源码提交。
