# 发票申请

原生税务发票申请与应收账单独立。旧 PHP 仅作业务参考；schema059 新增抬头、地址、申请、账单快照和幂等操作记录。

## 客户流程

客户在 `/console/invoice-information` 维护个人/公司抬头和收货地址。公司抬头必须填写税号；编辑/删除需要当前版本，已被申请使用的资料不能删除。每个客户最多一个默认地址，切换默认地址会推进原默认地址版本。

`/console/invoice-applications/new` 选择抬头、地址和 1 至 20 张本人已支付的原生正金额账单。申请保存抬头、地址、项目和金额快照，后续资料修改不影响已提交申请。导入或待复核账单暂不参与。申请金额使用整数分，最高为 1 亿元。

同一账单存在待审核或已开具申请时不可重复申请或退款。驳回后可重新申请；并发提交、退款和审核通过行锁串行检查，幂等键防止重复提交。

## 管理流程

后台 `/admin/invoice-applications` 按状态筛选和分页，详情展示客户资料、提交快照及审核记录。读取需要 `invoice_applications.view`，审核还需要 `invoice_applications.manage`；已有普通管理员角色不会自动获得新权限。超级管理员可在管理员与权限页授权，客户销售范围同时生效，历史幂等重放也重新检查当前权限与归属。

待审核申请可驳回（必须填写原因）或登记已开具（必须填写实际发票编号，可填写快递公司、单号和备注）。这是人工登记，并未接入税务开票服务；最终状态不可再次审核，写操作要求版本号和幂等键，审计失败会回滚业务写入。

## 接口

- `GET/POST /api/v1/invoice-profiles`，`PUT/DELETE /api/v1/invoice-profiles/{id}`。
- `GET/POST /api/v1/invoice-addresses`，`PUT/DELETE /api/v1/invoice-addresses/{id}`。
- `GET /api/v1/invoiceable-invoices`：可申请的本人账单。
- `GET/POST /api/v1/invoice-applications`，`GET /api/v1/invoice-applications/{id}`。
- `GET /api/v1/admin/invoice-applications`，`GET /api/v1/admin/invoice-applications/{id}`，`POST /api/v1/admin/invoice-applications/{id}/review`。

列表支持 `limit`、`offset`；申请列表另支持 `status`。创建/审核使用 `Idempotency-Key`，资料编辑/删除及审核使用 `expected_version`。

## 验证边界

独立 PG18 `_test` 数据库通过全量 Go 回归及专项：快照不变、所有权、拒绝后重申请、并发重复申请、待审核/已开具退款保护、销售转交后重放拒绝、即时撤权、审计失败回滚、默认地址并发与版本冲突。lint、typecheck、vet 和 webpack 生产构建通过。隔离浏览器实际完成客户提交、后台登记、客户查看审核记录；390px 手机详情无横向溢出，截图在 `output/playwright/invoice-mobile.png`。驳回流程已由 API 集成测试覆盖。

真实税务服务、电子发票文件、红冲/作废、运费/税费策略和物流追踪仍待实现。本批不提取旧数据，也不向开发库写入测试账单或申请。

schema059 已部署。`bin/previous/20261005T225233Z-invoice-applications/` 保存 schema058 dump、四个旧二进制和旧前端，SHA256 校验通过；恢复到独立 `_test` 库后旧 API、迁移059和新 API 均通过，迁移及实际部署前后的业务摘要一致。现场 root 只读列表/页面、注销及会话撤销通过，五张新增表均为0。未演练整站正式切换或真实税务集成。
