# 原生规则、验证码与服务操作历史验收

2026-10-06，本批由独立 gpt-6-luna 分支实现并经主分支审查合并：原生商品配置规则、可选图形验证码、客户及管理员服务操作历史。旧 PHP 和旧数据仅作参考，本批没有评论、评价或社交互动。

## 检验

- **PASS**：PG18 隔离数据库全量 Go 回归（246.905 秒）、Go race 专项（29.749 秒）、`go vet`、Next.js lint（0 errors，2 个既有 data-URL `<img>` warning）和 typecheck。
- **PASS**：独立 preview/release webpack 构建；发布 BUILD_ID `ef182c1a5f9c8aa9d571e236a554873936820ff2419f186c0fb6db8b808c5de5`，四个 Go binary SHA256 保存在 `output/native-security-20261006/release-SHA256SUMS`。
- **PASS**：浏览器夹具 743.88 秒。管理员创建/编辑规则并刷新后仍存在；客户选择高性能但不启用备份时按钮禁用，启用备份后加入购物车并结算 3150 分；操作历史安全投影、10 条分页、状态筛选、空结果和无敏感 payload 通过；验证码图像可读，错误答案消费旧挑战并刷新错误提示。
- **PASS**：SQL 断言订单 2 张、总额 4483 分、启用规则 1 条、验证码消费至少 2 次；隔离数据库和 schema 已清理。
- **PASS**：发布前 schema074 dump 实际恢复到独立 PG18 数据库，旧 API 就绪、迁移到 schema077、新 API 就绪，既有业务摘要一致；部署前后环境和 SHA 校验通过。
- **PASS**：开发实例发布后 schema077、API/worker/web active，root 登录、身份、目录、权限、站点设置、服务操作历史、注销后 401 及人工资源 409 边界通过。当前业务计数仍为 2 用户、4 商品、2 订单、2 服务、2 账单。

验证码默认关闭；线上 live-check 使用 `auth_captcha_enabled=false`，未在生产库写入测试挑战。真实 SMTP/SMS、真实支付商户、真实 NOKVM 供应商和浏览器外部验收仍属于后续边界。
