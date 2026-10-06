UPDATE notification_templates
SET body=E'您好，\n\n请打开以下链接重置您的 ZJMF 密码（30 分钟内有效）：\n{{reset_link}}\n\n如果不是您本人操作，请忽略此邮件。',updated_at=now()
WHERE kind='password_reset';
