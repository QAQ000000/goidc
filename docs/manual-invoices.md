# 人工账单

人工账单是 Go 原生的独立应收单，旧 PHP 与旧数据仅作业务参考。本批使用 schema056，开发库不写入测试业务记录。

## 管理流程

- 管理员需要 `invoices.view` 与 `invoices.manage` 才能创建、编辑或作废；`invoices.manage` 不会隐式授予现有角色。
- `/admin/invoices/new` 通过受权限保护的客户搜索选择器创建账单；项目金额使用整数分保存，可包含负数优惠行，总额必须为 0 至 1 亿元。
- 未支付、无待处理支付意图的人工账单可编辑。客户和账单来源不可改；版本号和幂等键防止并发覆盖与重复操作。
- 作废需要版本号和原因，保留账单项目、付款历史及审计记录，作废后不可付款。

## 付款与边界

人工账单支持余额支付和现有 mock 支付意图，付款不会创建订单、服务或开通任务；余额退款写入余额流水。待处理（包括已过期但仍为 pending）的支付意图会阻止编辑和作废。自动订单、续费和升级账单仍使用原金额和交付合同。

## 接口

`GET /api/v1/admin/invoice-customers`、`POST /api/v1/admin/invoices`、`PUT /api/v1/admin/invoices/{id}`、`POST /api/v1/admin/invoices/{id}/cancel`。写操作要求 `Idempotency-Key`；更新和作废要求 `expected_version`。

## 验证

Go 全量回归、`go vet ./...`、前端 lint/typecheck/webpack 生产构建通过。隔离浏览器验收覆盖创建、负数项目、编辑、移动 390px 无横向溢出、作废、客户余额支付和权限边界；证据见 `output/manual-invoices-20261006/`。schema055 dump 恢复后旧 API、迁移056、新 API 和业务摘要一致性均通过。备份位于 `bin/previous/` 中本批时间戳目录。
