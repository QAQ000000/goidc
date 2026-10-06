# 登录图形验证码

站点设置中的“登录、注册和找回密码需要图形验证码”默认关闭。启用后，`GET /api/v1/auth/captcha?action=login|register|forgot-password` 返回一次性 PNG data URL、挑战 ID 和五分钟过期时间；答案只保存在服务端的摘要中，浏览器只提交挑战 ID 和填写的答案。

挑战绑定到 HttpOnly、SameSite=Lax 的随机浏览器 nonce。服务端会校验动作、nonce、过期时间和未消费状态，并在同一行锁内标记消费，因此错误答案、过期挑战、重放和并发请求都会失效。每个挑战只能尝试一次，签发按现有对端地址限流，并用有界清理删除过期记录。Cookie 的 Secure 属性跟随 `COOKIE_SECURE`。

登录和管理员登录共用 `login` 动作；MFA 仍在图形验证码校验后的登录流程中继续。注册仍受 `registration_enabled` 约束，维护模式的登录、注册、找回密码和验证码签发保持可用。关闭功能时不签发 Cookie、不写挑战记录，旧客户端可以继续提交原有认证字段。

管理端设置接口使用 `settings.view` 和 `settings.manage` 权限，版本号和 `site.settings_updated` 审计记录保持有效。更新请求省略 `auth_captcha_enabled` 时会保留当前值，以兼容旧设置客户端。
