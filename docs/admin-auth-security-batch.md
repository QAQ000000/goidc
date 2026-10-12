# 注册登录与 MFA 设置首批

`/admin/auth-settings` 使用 `GET/PUT /api/v1/admin/auth-settings`；`/admin/security/mfa` 使用 `GET/PUT /api/v1/admin/security/mfa`。读取需要 `settings.view`，写入需要 `settings.manage`、浏览器会话、`expected_version` 和 `Idempotency-Key`。版本冲突返回 409，重复请求返回原结果，变更与审计在同一事务。

迁移 `127_auth_security_settings.sql` 建立独立认证与 MFA 策略表。原有 `site_settings` 的注册、邮箱验证、图形验证码字段保持公共接口兼容；任一后台页面修改这些共享字段都会推进另一设置页的版本。MFA 页面仅返回两个策略开关及版本和更新时间，不读取个人密钥或恢复码。关闭策略仅阻止新的个人 MFA 开通，已开通者登录仍强制验证。

客户登录总开关和用户名、邮箱、已验证手机号登录方式在真实登录路径生效；管理员登录不受客户登录开关约束。邮件/短信验证码渠道开关在真实签发与渠道发现路径生效，渠道实际投递仍要求配置并启用 `channel_instances`。连续管理员登录失败阈值与封禁分钟数在登录阻断路径生效。旧版纯邮箱注册仍为当前可用流程，手机独立注册、注册手机号强制验证和图形验证码自动启动没有完整流程，API 明确拒绝将这些开关设为开启，页面不展示无效控件。旧版“注册时选填字段”由客户自定义字段模块维护，不复制到此页。
