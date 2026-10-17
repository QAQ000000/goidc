# G09 固定容量 profile 与候选前门槛

本增量增加 `scripts/g09-capacity-contract.mjs` 和
`scripts/accept-g09-capacity.mjs`。代码把首轮五个阶段（2 分钟预热、10
分钟稳态、3 分钟峰值、最多 2 分钟 cohort 准备、最多 10 分钟排空）、读/业务
速率、40/30/10/10/10 事务组成、1000 客户/2000 服务/10000 账单、双 worker、
600 个冻结任务以及延迟、失败、排空和全局背景阈值固定下来。profile JSON 必须
逐字段匹配该合同，只允许把 `origin` 绑定到候选的独立 IPv4 loopback API
端口；不能在运行后调低目标或放宽阈值。

入口要求绝对 profile、candidate、clean source、预算和 SHA256 文件，并调用现有
`verifyRuntimeCandidate` 做候选完整清单/摘要/迁移/运行元数据校验。run root 必须是
新的 `/tmp` 或 `/dev/shm` 路径，且候选副本不得超过预算。输出只包含脱敏阶段和门槛摘要，
`candidateVerified=true` 仅代表静态 sealed candidate 前门槛通过，始终
`capacityVerified=false`。

本增量没有启动 API、Web、worker、Nginx、fixture 或数据库。无 `--execute` 时入口输出
`PREFLIGHT_ONLY`；传入 `--execute` 会返回 `g09_capacity_controller_not_implemented`
并退出失败，防止把尚未实现的 27 分钟 controller 当作容量 PASS。候选业务 seed、真实
HTTP 子请求、协议 sink、队列 drain、资金/库存不变量、进程/namespace 及生产 SLO 均未
覆盖，必须在后续完整 controller 中独立验收。
