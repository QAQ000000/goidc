# 信用额度退役边界

状态：已退役。信用额度、信用付款、授信管理及还款不再属于当前产品范围，不保留独立历史查询、还款或原付款 key 恢复入口。旧门户计划及验收只说明历史实现，不能用作待办或重新接线依据。

## HTTP 与页面

原有13个信用 API 保留路由及原认证、权限、CSRF 包装；通过入口鉴权后统一返回 `410 credit_retired`，不访问 Store、不查询对象、不重放回执、不写资金或审计。匿名、错误入口、无权限及不合法跨站写入仍先返回其原认证/授权错误，不能借退役响应泄漏受保护资源。

覆盖后台账户/消费/还款、额度/冻结/还款登记与设置，以及客户账户/消费/还款、账单信用状态和信用付款。完整 method/path 以 [server.go](../internal/httpapi/server.go) 和 [退役路由测试](../internal/httpapi/retired_credit_test.go)为准。

`/admin/credit`、`/admin/credit-settings`、`/console/credit` 页面返回404；菜单、客户详情信用入口及账单信用付款入口移除。`GET /admin/permissions` 与超级管理员 `/me` 权限列表不再展示 `credit.*`、`contracts.*`；既有角色中的旧权限ID仍被服务端识别以兼容角色保存，不表示功能可用。旧客户端收到410后停止付款或还款重试，不换 key 绕过下线。

## 数据与发布

迁移 [184](../db/migrations/184_retire_credit_and_contracts.sql) 关闭信用、合同及前台信用显示开关。设置值不能重新开启已退役路由，当前通用设置写入也不重新提供信用开关。

本批不物理清库、不重写旧迁移或财务 SQL；保留表结构与既有财务投影是防止改变账本口径，不是保留信用业务门户。统一交易、客户摘要或经营报表中的历史消费/实际还款仍按[现金与业务收款口径](credit-cash-reporting.md)解释，不能因此恢复额度管理、信用查询或还款操作。用户确认当前无历史业务；若部署前发现真实债务或未决写入，须先人工核查处理，不在生产随手删除或伪造还款。

先停止旧 API、worker、导入等 writer，再成套发布迁移、API、worker 和 Web；旧程序不得混跑或直接降级。发布与回退遵循[工程治理契约](architecture/engineering-governance.md)。

## 验证

单元与隔离数据库入口见 [retired_credit_test.go](../internal/httpapi/retired_credit_test.go) 和 [迁移测试](../internal/store/retired_features_migration_test.go)，纳入 `node scripts/test-admin-api.mjs --integration`。行为要求：原入口鉴权先行、所有旧路径410、历史回执不恢复、信用及账务事实无变化、184只关闭开关且迁移器重复调用不重复执行。

数据库、浏览器和部署按实际执行结果分别记录；代码、静态页面检查或历史门户截图不能替代本批浏览器/生产验证。
