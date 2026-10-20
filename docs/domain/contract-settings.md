# 合同退役边界

状态：已退役。合同及合同设置整体退出当前产品范围；模板、申请、客户确认、审核、邮寄、作废、状态、打印和下载均不再开放，不保留独立历史查询或回执恢复入口。

## HTTP 与页面

原有23个合同 API 保留路由及原认证、权限、会话与 CSRF 包装，通过入口鉴权后统一返回 `410 contracts_retired`。处理器不调用 Store、不读取对象或文件、不重放成功结果，也不写合同、资金、回执和审计。匿名、错误 Cookie、无权、禁用 Bearer 及跨站写入仍先执行原安全规则。完整注册见 [server.go](../../internal/httpapi/server.go)，覆盖见 [退役路由测试](../../internal/httpapi/retired_contracts_test.go)。

`/admin/contracts`、`/admin/contract-settings`、`/console/contracts` 页面返回404，前后台菜单和合同设置组件移除。`GET /admin/permissions` 与超级管理员 `/me` 权限目录隐藏 `credit.*`、`contracts.*`；旧角色ID继续被服务端识别，仅为兼容存量角色保存，不赋予恢复模块的能力。新前端不接入这些路由；旧客户端收到410后停止重试及下载。

订单条款、隐私政策、发票申请、电子发票文件、工单附件、客户文件及下载中心是独立能力，继续按各自授权处理。“移除数据导出、保留业务文件下载”的决定不包含已退役合同下载。

## 数据与发布

迁移 [184](../../db/migrations/184_retire_credit_and_contracts.sql) 关闭合同、信用及前台信用显示开关。通用设置不重新开放这些字段；修改旧设置值也不能重新启用退役 handler。

用户确认没有合同历史需要保留；本批仍不做物理清表，不重写旧迁移及文件存储结构。遗留结构不是活动功能或待办。若部署前发现真实合同、在途任务或文件，应先人工核查，不直接在生产清除。

停止旧 API、worker、导入等 writer 后，迁移、API、worker、Web 成套发布；不得新旧混跑或直接降级旧程序，见[工程治理契约](../architecture/engineering-governance.md)。

## 验证与历史

[退役测试](../../internal/httpapi/retired_contracts_test.go)覆盖23条路由、原鉴权与CSRF、无 Store 依赖以及历史合同/账务/回执/审计不变；[迁移测试](../../internal/store/retired_features_migration_test.go)覆盖关闭开关及保留事实。严格入口为 `node scripts/test-admin-api.mjs --integration`，须独立 PostgreSQL 18、逐项 run/pass、零 skip。

[旧生命周期设计](../admin-contract-lifecycle-plan.md)和[历史验收](../admin-contract-lifecycle-acceptance.md)仅解释过去实现，PDF、电子签署、强制签约等旧差距不再计为当前待办。数据库、浏览器、外部文件及部署分别按实际执行证据判断。
