# 定时任务执行日志的最小审计方案

现有 provision_jobs、email_outbox、sms_outbox 是可更新的业务任务状态表，不能证明调度器每次执行、每次失败或重试的历史。当前 /admin/logs/jobs 只展示授权范围内的服务任务终态记录，界面明确标注来源及限制，不伪造用户名、IP 或 cron 日志。

后续最小方案是独立 scheduler_runs 表：id、固定枚举 task_key、started_at、finished_at、status（running/succeeded/failed/interrupted）、affected_count、has_error；不存任意 payload、原始异常、令牌或客户详情。调度器执行前创建记录，结束时写入固定状态及计数；进程异常保留 running，恢复时标记 interrupted，不能把未确认的外部动作自动重新执行。使用数据库生成时间和唯一 run_id，写入失败应明确报告，不宣称已审计。只向 super_admin 或独立系统审计权限开放全局调度历史。

接入需要核对每个 worker/调度入口与日志保留策略；不得用业务队列表回填虚构历史。本次未新增调度历史迁移或宣称此方案已经实施。
