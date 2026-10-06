CREATE TABLE notification_templates (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  kind text NOT NULL UNIQUE CHECK (char_length(kind) BETWEEN 1 AND 80),
  name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
  subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 240),
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 100000),
  active boolean NOT NULL DEFAULT true,
  updated_by bigint REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO notification_templates(kind,name,subject,body) VALUES
('password_reset','密码重置','重置 ZJMF 密码','您好，\n\n请打开以下链接重置您的 ZJMF 密码（30 分钟内有效）：\n{{reset_link}}\n\n如果不是您本人操作，请忽略此邮件。');
