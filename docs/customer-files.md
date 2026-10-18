# 客户专属文件

旧userdownloads的归属转换、受控文件读取、字节SHA/源归档及重跑保护已在迁移044完成，详见 [旧下载资料迁移](legacy-download-files.md)。本页原批次中的“旧文件导入待完成”为当时状态。

迁移 042 完成后台客户文件维护和客户“我的文件”。后台全局入口 `/admin/customer-files`，客户管理内可进入 `/admin/customers/{id}/files`；客户入口 `/console/files`。专属文件与公共下载目录独立，客户只可读取本人文件，不需要拥有商品或有效服务。

## 源码与差异

依据旧项目 `app/admin/controller/DownloadsController.php` 675–945 行的 userdownloads 上传、添加、列表、修改、下载和删除流程。保留客户归属、名称、备注、原文件名、上传管理员、创建时间，并增加版本、SHA-256、下载计数和事务审计。旧下载入口接受 uid 和文件编号却未可靠绑定归属；新接口始终从当前身份和文件归属校验权限。旧源码查找未发现客户“我的文件”页面，本批为其补充独立客户入口。

上传接口不接受服务器路径或外部链接。沿用公共下载中心的文档、图片、压缩包白名单，单文件最大 50 MiB；格式与签名检测见[下载中心](download-center.md)。元数据保留在 PG，文件默认保留数据库字节，也可按部署 profile 写入私有目录或 S3，规则见[应用文件存储](architecture/file-storage.md)；默认配置变更不搬迁历史文件。备注按客户可见纯文本展示。2026-10-05 旧库只读盘点 userdownloads 为 0，未迁移或写入旧源数据；后续旧文件导入以页首迁移 044 契约为准。

## 权限与事务

新增独立 `customer_files.view` / `customer_files.manage`，修改权限须包含读取权限；公共下载或客户管理权限不会自动授予文件权限，已有角色不扩权。超级管理员可管理全部客户；普通管理员同时受当前客户销售范围约束。客户选择器只返回授权客户的编号、账号、显示名和状态，搜索最多 100 条；明确指定范围外客户返回 404。

编辑允许更换所属客户，但管理员必须同时有权访问原客户和新客户。未传文件保留原字节，替换更新上传人并保留计数；成功编辑推进版本。转移成功后原客户下一次访问为 404，新客户可下载；不撤回已经授权并返回的字节。客户提交 customer_id 不会扩大查询范围。API 密钥与会话均按当前账户状态、撤销和过期状态核对。

所有列表、下载和写操作按 RBAC 共享锁、公共下载设置共享锁、专属文件目录锁排序，写入和管理下载再按客户编号顺序获取销售范围共享锁。等待后复核角色、凭据和归属；销售转交与文件转移排序，避免两个客户相互转移死锁。修改、替换、删除、计数和审计同一事务，审计失败整体回滚。当前目录锁串行处理专属文件请求，容量与大量并发下载仍待验收。

关闭公共下载设置同时禁止客户专属文件列表与下载（403 downloads_disabled），后台仍可维护和恢复。站点维护模式按原规则保护客户接口。成功 GET 下载递增一次并记录 customer_file.downloaded；HEAD 不返回字节、不计数、不写下载审计，下载不推进编辑版本。附件响应使用实际文件名、UTF-8、attachment、nosniff、private/no-store。

批量删除接受明确 1–100 个不重复文件及各自 expected_version。先检查全部客户范围，逐项核对版本；任一错误回滚全部删除和审计。界面跨筛选或分页清空选择，不隐式删除其他文件。

## API

路径以 `/api/v1` 为前缀：

| 方法/路径 | 说明 |
| --- | --- |
| GET /customer-files | 当前客户文件列表，q 搜索名称/备注/文件名，分页 |
| GET /customer-files/{id}/file | 本人文件实际下载，HEAD 可用 |
| GET /admin/customer-files | 授权范围内列表，customer_id 可筛选 |
| GET /admin/customer-files/owners | 授权客户搜索选择，customer_files.view |
| GET /admin/customer-files/{id} | 授权文件完整元数据，customer_files.view |
| GET /admin/customer-files/{id}/file | 授权下载，customer_files.view |
| POST /admin/customer-files | 上传，customer_files.manage |
| PUT /admin/customer-files/{id} | 元数据/替换/归属变更，customer_files.manage |
| DELETE /admin/customer-files/{id} | JSON expected_version 删除 |
| POST /admin/customer-files/bulk-delete | JSON items:[{id,expected_version}] 原子删除 |

上传/编辑为 multipart/form-data，metadata 包含 customer_id、title、remarks、expected_version，file 创建必填、编辑可省略。分页默认 25、最多 100，页面每页 20。客户响应不返回上传人、后台版本或其他客户身份。

## 验收与部署

隔离 PG18 覆盖本人/跨客户读取、原始字节与 SHA、UTF-8 响应、HEAD 与计数、保留/替换/转移、销售范围及转移两侧授权、独立权限、只读/API 密钥、账户停用与凭据过期、下载开关、路径/MIME/空文件/长度校验、批量范围/版本/重复拒绝、并发编辑 200/409、并发计数、审计故障回滚，以及文件锁/销售锁等待期间的会话/密钥/权限撤销与销售转交。23 文件的 20/3/0 分页、搜索和范围计数通过。031 历史角色仍只保留 26 项原始授权。

全量强制隔离回归及真实旧库只读演练 `REQUIRE_INTEGRATION=1 REQUIRE_LEGACY_REHEARSAL=1 go test ./... -count=1`、vet 通过（httpapi 57.353s，legacy-import 13.772s）；补强批量末项版本失败及销售锁等待会话撤销测试 3.302s 通过。前端 lint/typecheck/Turbopack 生产构建通过，隔离预览另用 webpack 构建。

2026-10-05 Playwright 实际上传中文文件、替换、下载、转移客户 A→B、两侧可见性、后台客户筛选、无结果搜索、只读管理员下载/字段禁用/无上传保存删除入口、勾选两文件批量删除及 390×844 客户页/只读弹窗验收通过。客户 A、B 与只读管理员实际下载 SHA-256 均等于替换文件，计数保留且版本 2→3。一次验收脚本错误等待客户登录跳转 /console（实际跳转 /），修正后重新完成；这不是业务故障。浏览器未逐项测试销售员角色、并发冲突、关闭开关和分页翻页，这些由数据库集成测试覆盖。fixture 正常 PASS 712.978s，随机 schema 为 0、预览进程/3089 监听/目录已清理。详细证据位于忽略目录 output/playwright/customer-file-acceptance-20261005.md。

开发实例已部署迁移 042 和四个新程序：31 项权限、129 条后台路由，三个项目服务 active，healthz/readyz 与公共下载接口正常，专属 API 匿名为 401。root 真实会话只读核对 0 专属文件、1 个授权客户、31 权限及两个后台页面 200 后注销。公共下载设置保持启用/版本 1，0 公共文件/分类；站点版本 1/注册开/维护关、0 旧数据导入批次。users/products/orders/services/order_items 完整 JSON 摘要部署前后相同，无本批测试账号或文件。

备份 bin/previous/20261005T064225Z/schema041.dump 为 413311 字节，可读目录 481 行，包含四个旧程序及前后完整性摘要；未演练备份恢复或完整切换回滚。服务详情产品资料已在后续批次完成，见 [服务产品资料](service-downloads.md)。分类树已在后续迁移043完成，见 [下载分类树](download-category-tree.md)。旧本地文件受控导入/映射/归档/重跑保护和大容量存储/续传继续按台账迁移，整体重构仍未完成。
