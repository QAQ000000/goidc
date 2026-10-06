# 客户服务导航与商品归属

服务导航来自旧系统 `nav_group` / `nav_group_user` / `products.groupid`，与购买目录的 `product_sections/product_categories` 分开维护。源码依据：`ConfigGeneralController::productGroupPost`、`CommonController` 的 navindex/addindex、`common.php::updateUgp`、`CartController` 下单后的 is_show 更新及 `HostController` 服务查询。

## 行为

- 管理员设置名称、固定图标与排序；名称最多 255 字符，同排序按编号排列，新增上限 100 组。开发实例未导入旧默认分组，现有商品保持未分组；隔离旧数据迁移保留源分组和归属。
- 商品服务归属单独保存，不改价格、库存或购买目录分类。归属变更增加商品 revision，旧购物车需重新确认；既有服务按商品**当前**分组显示。
- 客户首次初始化导航，已有分组均显示。初始化后新增分组默认隐藏；即使首次初始化时没有分组，也记录初始化状态。
- 每个客户可独立显示/隐藏导航。隐藏不影响服务详情、操作权限、全部服务或分组筛选；管理员数据仍受销售客户范围约束。
- 创建新订单会恢复所购商品分组的导航，包含尚未付款的订单；成功幂等重放不重复恢复。不把服务状态变化或管理员转组当作新购。
- 左侧数字为本人该组 `active` 服务数；导航设置同时列出本人全部状态的服务数。导入待复核服务可列出，但不算运行中，原操作复核保护继续生效。
- 删除分组必须确认转移到另一组或未分组。商品归属和 revision、个人偏好清理及审计在同一事务中完成，不删除服务，不自动修改转移目标的个人显示状态。
- 图标只接受 `server/cloud/database/globe/box`，不渲染旧任意 HTML/CSS 图标类。

## API

金额、服务生命周期与权限模型沿用现有契约。新增管理接口使用 `products.view/manage`；服务分页使用 `services.view`。API 密钥同样受当前角色、账号状态及范围约束。

| 接口 | 功能 |
| --- | --- |
| GET `/admin/service-groups` | 分组、version 及商品数 |
| POST `/admin/service-groups` | 创建 name/icon/sort_order |
| PUT `/admin/service-groups/{id}` | 完整更新及 expected_version |
| DELETE `/admin/service-groups/{id}` | expected_version、target_group_id（null 为未分组） |
| PUT `/admin/products/{id}/service-group` | group_id（null 为未分组）、expected_revision |
| GET `/service-navigation` | 本人全部分组、visible、preference_version 与服务数；初始化缺失偏好 |
| PUT `/service-navigation/{id}` | 本人 visible、expected_version；不接收 user_id |
| GET `/services/page`、`/admin/services/page` | items/total/limit/offset，group_id、status、q 筛选 |

原 `/services`、`/admin/services` 保持数组返回，同样支持筛选。group_id=0 表示未分组，省略为全部；未知但合法编号返回空列表。搜索名称/标识，状态白名单校验，limit 默认 25、最大 100。分页总数和结果共用一个 SQL 快照，包括超出范围的空页。服务列表/详情及商品详情附带当前服务分组编号和名称。

## 一致性

schema `039_service_navigation.sql` 新增三张导航表和商品外键，不重写历史订单或服务。目录写入使用既有管理员授权共享锁 + 排他目录锁；客户初始化/偏好及订单使用目录共享锁后按用户导航状态行串行化。版本冲突返回 409，未知记录返回 404，审计失败事务回滚。删除与下单并发时，先删除会使原购物车版本失效；先下单仍允许随后删除转移，不能留下悬空归属或偏好。

## 验证边界

隔离 PG18 测试覆盖首次/空首次初始化、新增分组隐藏、个人隔离、隐藏可访问、新购恢复与幂等重放、当前归属与删除转移、运行数/所有数、搜索/状态/分页/空页、客户与销售范围、只读权限/API 密钥权限撤销、同版本并发、删除下单并发及审计回滚。真实旧库继续只读导入演练，不把导航测试数据导入开发库。

schema 040 已补齐旧 nav_group/nav_group_user 与商品 groupid 的迁移映射，并在真实旧库隔离演练验证；没有向开发库导入，不代表旧站点数据切换。详见 [旧数据迁移](legacy-migration.md)。旧菜单关联 navRelid、模板类型路由、任意 fa_icon 类与其他产品控制不在本批范围。备份恢复和完整切换回滚另需验收。

2026-10-05 全量强制隔离 PG18 与真实旧库只读迁移演练通过（HTTP API 52.334s，legacy import 3.912s），vet、前端 lint/typecheck、Turbopack 主目录及 webpack 隔离构建通过。隔离生产构建浏览器验证分组创建/名称排序编辑、商品归属独立保存、个人隐藏/显示、隐藏组访问、22 条服务的 20/2 分页、状态与搜索、商品转组后既有服务归属、删除转移与已删除组链接提示、只读控件禁用和无保存入口。390×844 页面及导航弹窗无横向溢出。fixture 575.118s 正常结束，随机 schema 与预览环境清除，最终浏览器控制台无错误/警告。复现与截图位于忽略目录 `output/playwright/navigation-acceptance-20261005.md`。

开发实例已部署 schema 039：三个项目服务 active，healthz/readyz 200，27 项权限与 109 条管理路由。原 2 账号/4 商品/2 订单/2 服务保留；部署前后商品（忽略新增 service_group_id）、订单、服务、订单项及用户完整 JSON 聚合摘要一致。无新服务分组或商品归属，未留下隔离测试数据，站点版本 1、注册开启、维护关闭、零导入批次。部署前备份 `bin/previous/20261005T050604Z/` 含 schema 038 custom dump（369711 bytes）及三个旧二进制，dump 目录 432 行可读取；未演练恢复或完整切换回滚。root 部署后仅打开空列表及新增弹窗，不保存业务修改，验收会话注销。旧 PHP/MySQL 未修改，完整重构目标仍在进行。
