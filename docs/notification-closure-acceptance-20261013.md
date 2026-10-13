# 2026-10-13 通知链路验收与操作流程

## 结果

SMTP 收件箱实际到达、真实邮箱注册和邮箱找回密码均已通过。短信 `verification_code`、`password_reset` 已绑定批准模板 `AVBn22`；真实短信验证码发送被上游额度不足拒绝。用户选择本轮保留额度不足证据，不追加发送。

| 验收项 | 结果与证据 |
| --- | --- |
| SMTP 收件箱 | 使用配置中已有凭据登录阿里企业邮箱 IMAPS 993，以只读 INBOX 和 BODY.PEEK 读取邮件；未修改已读状态。两封 SMTP test 的 Received 时间分别为 13:17:30、14:55:44（Asia/Shanghai），与 email_outbox 4、9 对应 |
| 上游模板 | 真实查询 `AVBn22`，业务状态 200、审核状态 2；签名为 `【智简魔方】`，正文 `验证码@var(code),5分钟内有效,请勿泄漏于他人` |
| 模板绑定 | 正常管理员 PATCH 接口更新 SMS 模板 4、5，版本均 1→2；上游 ID 为 AVBn22，正文为 `验证码{{code}},5分钟内有效,请勿泄漏于他人`。没有申请新模板 |
| 真实邮箱注册 | email_outbox 16，22:53:17 到达 INBOX；从邮件正文读取验证码，通过注册 API 创建专用客户 21，email_verified=true，挑战已消费，实际登录成功 |
| 真实邮箱找回密码 | email_outbox 17，22:56:06 到达 INBOX；从邮件正文读取验证码，重置 API 成功，旧会话已撤销，新密码实际登录成功 |
| 真实短信验证码 | 已登录客户 21 通过手机绑定 API 申请，sms_outbox 3 绑定真实挑战及 AVBn22，有效期 300 秒、max_attempts=1；发送一次，HTTP 200、业务状态字符串 405、`短信条数已用完`；未完成手机验证或短信找回密码 |
| 本次响应留存 | 第 3 条短信原始响应 46 字节、不含 BOM、不截断；先加密入库，再完成失败记录。解密回验 SHA256 与原始响应一致。共享证据只保留去敏元数据 |
| 清理 | 客户 21 已停用、会话 0、phone_verified=false；短信 3 的挑战已作废，Outbox expired，正文已清空，尝试次数及上限仍为 1；SMTP 邮件、Outbox、审计和加密响应保留 |

注册只支持邮箱，因此本轮真实注册通过邮箱完成。手机绑定用于验证 `verification_code` 短信模板；没有把已知测试号码直接写成已验证，以绕过手机号所有权验证。

## 第一条短信误报的边界

第一条 sms_outbox 1 用户确认收到，但本地记录失败。当前数据库和当次 worker 日志只有通用 `SMS delivery failed`，没有原始网关响应。不能还原当时 HTTP 状态、响应格式或业务状态，也不能追改为 sent。

本次短信 3 明确因额度不足失败；该响应不解释第一条短信的不一致。既有 BOM、字符串状态兼容修正仍保留，但没有证据证明首条误报由它们导致。

## 代码与运行状态

新增迁移 `137_sms_delivery_evidence.sql` 已应用运行库，仅新增证据表和索引。原有运行服务保持可用，短信发送前短暂停止空闲常驻 worker，单次发送结束后恢复。真实验证码 API 使用当前源码构建的临时 API 8089，短信 3 使用当前源码的专用单条发送器；邮箱由常驻 worker 投递。临时 API 已停止。

验收时本轮源码修改尚未提交、推送或替换常驻 API/worker。2026-10-14 用户要求将本轮代码与文档提交本地 Git，常驻 API/worker 发布仍未执行。两个模板的配置更新已生效；常驻旧 API 的 SMS 挑战仍为 10 分钟，需发布新 API 后才能与模板的 5 分钟一致。常驻旧 worker 不会自动保存新增响应证据，后续真实短信测试必须使用带留存能力的发送器，或先发布本轮 API/worker。

新代码统一 SMS 挑战、Outbox 和 API 的有效期为 5 分钟，邮箱保持 10 分钟。worker 将响应正文在 BOM 清理和 JSON 解析前留存，包含非 2xx、业务拒绝、非法 JSON、部分读取失败；传输失败以固定诊断类型记录。响应上限 64 KiB，超限标记截断。通用 HTTP 适配器仍按 HTTP 2xx 判成功，读取证据失败不改变其既有成功约定。

## 响应证据操作流程

- `sms_delivery_evidence` 按 `(outbox_id, attempts, locked_at)` 区分每次租约，手工重试重置 attempts 不会覆盖旧证据。
- 正文通过现有 provision 密钥 AES-GCM 加密，AAD 包含 Outbox、发送次数和 UTC 租约时间。手机号、验证码、应用密钥及网关原始正文不得写入普通日志、审计 payload 或管理员列表 DTO。
- 证据保留 7 天；带本轮源码的 worker 在短信过期清理时删除到期证据。只读诊断优先查看 provider、http_status、outcome、response_truncated、created_at、expires_at。
- 必须读取原文时，在受控主机用 provision 密钥解密 `response_encrypted`，AAD 为 `sms:response:<outbox_id>:<attempts>:<locked_at UTC RFC3339Nano>`。不把解密结果复制到共享文档或普通日志。旧 worker 尚未发布期间可由运维执行 `DELETE FROM sms_delivery_evidence WHERE expires_at<=now()` 清理到期证据。
- 保存证据失败时 worker 不完成该次发送，留下租约及脱敏错误；应先核对上游实际投递结果，再决定是否重试，避免将未知结果当作未发送。

## 后续短信验收

本轮用户已选择保留额度不足结果。需要恢复验收时，先补充当前智简魔方商户短信额度，再发布带响应留存及 5 分钟挑战的 API/worker，或使用相同源码的专用测试进程。

重新启用专用客户 21并通过正常登录取得会话，重新申请手机绑定验证码；旧挑战已作废，不能复用。发送上限先设为 1，实际收到后由用户提供验证码，通过手机绑定确认接口建立 phone_verified。随后通过短信找回密码申请和确认接口完成重置，再验证旧会话撤销、新密码登录。每次先保存原始响应，再处理结果；额度拒绝不得盲目重发，只有用户确认手机收到才能声称终端送达。

## 回归验证

- notification/store race 通过；新增响应留存覆盖 BOM 成功、HTTP 拒绝、额度不足、非法 JSON、超限、传输失败、部分读取失败及通用 HTTP 成功约定。
- 隔离 PG18 `payment_utf8_test`、随机 schema 强制运行验证码、SMS、Outbox 专项通过（51.499 秒）。
- 新增证据加密、AAD 租约隔离、重复保存、DTO 不泄露、7 天清理及验证码有效期测试通过；注册和重置并发/错误码回归 race 通过。
- `go vet`（notification/store/httpapi/worker）、API/worker/migrate 构建、`git diff --check` 通过。

本地证据位于忽略目录 `output/notification-closure-20261013/`：`inbox-evidence.json`、`registration-evidence.json`、`password-reset-evidence.json`、`sms-3.json`、`template.json`。没有保留登录密码、验证码或会话文件。没有新增前端修改或浏览器交互验收；未覆盖外部其他收件商、手机终端送达和短信找回密码成功路径。
