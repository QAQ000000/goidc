# NOKVM 快照与备份

Go API、任务、管理员核验及 Next.js 客户/后台操作页已实现，2026-10-06 已发布到 schema071 开发实例。旧 PHP 仅作业务和协议参考，不读取旧业务数据。真实 NOKVM 供应商及客户机恢复尚未验收。

## 已确认的协议

依据旧 `public/plugins/servers/nokvm/nokvm.php` 的 `nokvm_ClientAreaOutput`、`CreateSnap/DeleteSnap/RestoreSnap`、`CreateBackup/DeleteBackup/RestoreBackup`（330–377、551–681行）。

| 操作 | 快照 | 备份 |
| --- | --- | --- |
| 主机列表 | GET `/api/snapshot/{virtual_id}` | GET `/api/backups/{virtual_id}` |
| 创建 | POST `/api/snapshot/`，表单virtual | POST `/api/backups/`，表单virtual |
| 删除 | DELETE `/api/snapshot/{point_id}` | DELETE `/api/backups/{point_id}` |
| 恢复 | GET `/api/snapshot/{point_id}/edit` | GET `/api/backups/{point_id}/edit` |

`internal/provision/nokvm_storage.go`复用服务端签名、HTTPS验证、禁止重定向/环境代理、超时、1MiB响应上限及重复JSON字段防护。主机与还原点ID为有效正int64十进制字符串，支持大于JavaScript安全整数的ID。列表仅返回id/name/created_at，不返回路径、供应商账户或凭据；校验名称控制字符、时间、重复ID及最多1000行，按时间排序。无时区created_at沿用profile时区（当前monitoring_timezone，默认Asia/Shanghai，实际接入需确认）。

删除/恢复命令发送前重新读取该主机的列表，目标必须属于该列表；任意或另一主机的还原点不能直接作为命令目标。列表读取失败属于未发送命令，不标为未知写入；发送后的超时、非200及异常/歧义JSON按未知结果处理，明确拒绝保留拒绝结果。code0仅返回CommandAccepted并产生storage_confirmation_pending/Unknown，不声称快照完成或系统恢复完成。

协议测试及全provision竞态测试已通过，证据 `output/nokvm-storage-protocol-20261006.log`；使用本地HTTP协议夹具，真实NOKVM环境未验证。

## API 与任务

GET/POST `/api/v1[/admin]/services/{id}/storage/{kind}`，kind 为 snapshot/backup。GET 返回 kind/limit/items/fetched_at；客户限本人已审核 active/suspended NOKVM 服务，管理员需services.view及客户范围。额度来自不可变购买交付快照，额度0不请求上游。每分钟30次限制，所有响应no-store；远程读在事务外，结束后复查账户、权限、范围、归属、服务状态、审核及资源/profile/购买快照，HTTP再检查会话/API key。

POST 接收 `{ "operation": "restore", "point_id": "10", "risk_acknowledged": true }`。operation 为create/delete/restore，create 的point_id必须为空。要求8至128字符Idempotency-Key；新请求202，同键同操作重放200，换目标或操作409。HMAC绑定服务、幂等键、种类、操作及目标ID；重放先于额度、60秒全存储动作冷却和远程列表变化。仅active NOKVM，管理员需services.view/manage及客户范围；现有任务、配置变更、上游处理中/暂停/流量限制均阻止写操作。

首次事务外预读列表，创建校验数量与购买额度，删除/恢复保存id/name/created_at。真正入队事务锁定原session/API key并重验账户、权限、范围、绑定、购买额度和状态；预读期间退出、撤销密钥、停用或撤权不产生任务。任务payload保存profile/upstream/limit和目标快照；数据库触发器禁止变更动作、服务、幂等键、指纹或payload，没有供应商令牌或系统密码。

worker在外部发送前重验绑定和购买额度，创建再次检查数量，删除/恢复再核对主机列表及目标元数据；恢复只允许实际电源on/off。先持久化外部发送标记，再释放数据库锁发请求。code0仅受理，六动作全部waiting_verification；结果不明、响应落库失败、worker中断及已发送租约过期禁止自动重发。

## 恢复与核验

恢复在pending/running/waiting_verification期间阻断旧系统凭据读取。管理员经现有delivery-verification接口核验：恢复applied必须提供实际os_name、root/administrator的username和12至128位password，前端两次输入。先确认供应商恢复完成及实际客户机登录，不能从还原点名称推断系统。更新独立installed_os、username和AES-GCM系统密码，保留原VNC凭据和购买快照。

not_applied必须确认供应商未执行恢复且原系统和原凭据仍有效，保留旧凭据，任务failed；进行中保持待核验。恢复两种核验都触发延迟只读同步，不再次发恢复指令。创建/删除也须实地核验，列表不能替代完成契约。核验指纹含密码时使用HMAC；审计前清空password，核验依据拒绝含密码；审计失败原子回滚状态及新凭据。六动作均不修改计费、账期、余额或库存。

客户/后台详情显示列表、额度、刷新、加载/错误重试/空状态及任务核验状态。三种操作均有风险确认，删除/恢复还需准确服务名称。只读管理员仅查看，无写按钮与凭据入口；提交报错后刷新任务，恢复响应丢失状态，待处理任务禁用写操作。桌面及390px手机验收见 [快照与备份验收](nokvm-storage-acceptance.md)。

## 边界

没有确认的供应商操作完成接口，不能把受理当作完成。本地HTTP供应商和独立PG18验收不能证明真实快照、备份或恢复可靠性。VNC、流量计费及其他模块继续实施。不开发评论、评价等互动。
