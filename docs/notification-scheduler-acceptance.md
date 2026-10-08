# 通知中心与调度器 098 收口验收

本批在 codex/parallel-core-20261006 分支完成通知中心三个缺口的复核和修正。已有 098_notification_scheduling_outbox.sql、worker 和管理员重试接口继续使用，补充新增 098_outbox_retry_limits.sql，不改已提交迁移或旧 PHP 目录。

- 未支付订单、逾期账单的 email/SMS 模板已经入库。每分钟扫描未付账单，只向已验证联系方式和启用的渠道入队；同一账单/渠道/类型/日期最多一次。扫描排除当天已经提醒或没有可用渠道的记录，避免前一批占满扫描窗口。等待账单锁后重新读取金额、到期时间、审核状态。
- 修正 095 的旧唯一索引：支付成功仍每账单/渠道一次，催缴通过 098 日期索引实现跨日期重复提醒。原有支付成功事件保持幂等。
- 逾期暂停保留 24 小时宽限和批次事务，只创建 suspend 任务；批次先锁全部账单，再按服务 ID 锁服务。执行事务在服务锁之前锁定原账单，复核未支付及宽限；已支付或取消的任务记 cancelled 并审计。模拟、NOKVM 和供应商派发均接入复核，外部调用仍在原 dispatch 事务提交后执行。实际暂停成功继续使用 097 service_suspended 通知。
- email_outbox 和 sms_outbox 共用失败/恢复逻辑：1、2、4、8、16、32 分钟退避，后续封顶 32 分钟；默认最多 5 次，max_attempts 可配置 1–20。崩溃租约恢复也退避。达到上限转 dead_letter，保留有效加密正文；验证码失效或通知过期优先转 expired 并清理正文。
- 人工重试沿用 POST /api/v1/admin/email-outbox/{id}/retry 与 POST /api/v1/admin/sms-outbox/{id}/retry，需要 notifications.manage 并写审计；禁止已发送、投递中、过期/验证码已消费的消息重试。重试清零次数及死信时间。领取时间 locked_at 与 attempts 一起校验，旧 worker 无法完成人工重试后新领取的任务。

新增 internal/httpapi/notification_completion_test.go：有限批次持续排空、并发幂等、跨日期催缴、097 实际暂停通知、付款后撤销、付款/调度锁顺序、邮件/SMS完整失败→退避→死信→权限检查→人工重试→成功链路、旧租约迟到结果拦截、过期死信清理、上限领取拦截及通知失败原子回滚。

数据库验证使用动态创建的 PostgreSQL 18.0 独立库 zjmfgo_notifications_20261009005900_005852_test，REQUIRE_INTEGRATION=1，HTTP 每个用例随机 schema 并重复执行迁移。首轮全量通过：httpapi 386.340s、store 0.583s；最终代码专项 race 通过：httpapi 28.850s、store 1.030s（store 专项名称未匹配用例，数据库行为专项均在 httpapi 中实际执行）。最终代码 vet 与 API/worker 编译通过。

最终全量 ./scripts/go.sh test ./internal/httpapi ./internal/store -count=1 零错误 PASS：httpapi 370.621s、store 0.432s。测试库已在验收后删除。此次验证为隔离数据库和 API 集成回归，未执行真实 SMTP/SMS 投递或真实供应商暂停，未部署到开发服务。已有返佣第二阶段的未跟踪文件保留，本批不纳入提交。
