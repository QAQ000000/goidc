# NOKVM 虚拟光驱

旧 PHP 插件只作为协议参考，未读取旧业务数据。客户及管理员服务详情使用独立光驱组件，服务端提供 GET/POST `/api/v1[/admin]/services/{id}/cdrom`。真实 NOKVM 与客户机尚未验收。

## 供应商协议

旧 `nokvm.php` 的光驱页面读取 GET `/api/virtual/{virtual_id}` 与 GET `/api/cd_rom/`，以主机 `cd_roms_id` 判断挂载镜像。挂载/卸载分别发送签名 GET `/api/virtual_cd_rom/{virtual_id}/mount/{image_id}` 和 `/api/virtual_cd_rom/{virtual_id}/unmount/{image_id}`。旧模板说明挂载会切换为光驱启动，卸载切换为硬盘启动。没有已证实的供应商完成状态或启动顺序查询协议。

目录最多 1000 项，要求唯一正 int64 十进制 ID、有效名称和描述；主机 ID 必须与服务绑定的上游 ID 相同，挂载 ID 必须属于目录。GET 响应仅返回 id/name/description、mounted_id 和 fetched_at；供应商路径、令牌、账户及额外字段不透传。列表读取、入队前读和 worker 发送前读都验证主机绑定及目标目录。挂载要求当前为空，卸载要求当前镜像与目标一致。

## 授权与任务

客户仅能访问本人已审核 active/suspended NOKVM 服务。管理员 GET 需要 services.view 和客户范围，POST 另需 services.manage；只读管理员无写按钮。POST 使用 `{ "operation":"mount", "image_id":"10", "risk_acknowledged":true }`，需要 8 至 128 字符 Idempotency-Key。页面在对话框中明确提示启动顺序风险并要求确认。只能对 active 服务写入，现有待处理任务、配置变更、资源处理中及 60 秒冷却会阻止新任务。

远程预读在数据库事务外，结束后重新核验权限、客户范围、归属、服务与上游绑定；入队事务再锁定会话或 API key 及用户、服务、管理权限，预读期间撤销凭据不会入队。HMAC 指纹绑定服务、幂等键、动作及镜像 ID，同键同请求重放返回原任务，换目标或操作冲突。schema073 限制动作、payload 和不可变请求字段；payload 只保存目标镜像快照、主机挂载 ID、profile 和上游绑定。

worker 在标记外部发送后释放事务锁，重新读取主机与目录确认目标和状态未变，才发送命令。code 0 只表示受理，任务进入 waiting_verification。响应歧义、超时、异常或已发送 worker 租约失效均不自动重发。管理员需核验实际光驱及启动顺序，再用原有 delivery-verification 接口登记 applied/not_applied；不需要系统密码或财务操作。核验期间客户页面显示待处理状态并禁用后续写入。

本地签名协议夹具、隔离 PostgreSQL HTTP 夹具覆盖权限、重放、未知结果、请求不可变、会话撤销和财务不变量。真实供应商对 `cd_roms_id` 缺省值、完成时间及 BIOS 行为仍需现场确认。
