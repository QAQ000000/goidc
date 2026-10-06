# 新闻与知识库

源行为来自 `app/admin/controller/NewsController.php`、`KnowledgeBaseController.php`、`app/admin/validate/KnowledgeBaseValidate.php` 和 `app/home/controller/KnowledgeBaseController.php`。本批迁移文章与分类 CRUD、草稿/发布/归档、发布时间和知识库访问范围。旧系统的多分类、标签、富文本/图片、文章排序、SEO/自定义字段仍待迁移。按用户2026-10-06要求，不开发评论或文章评价互动。

## 管理入口与权限

后台 `/admin/content` 分新闻、知识库及文章、分类。`content.view` 允许读取列表、详情和分类；`content.manage` 同时要求读取权限，允许创建、全量编辑和删除。普通只读管理员能打开详情但不能提交。超级管理员拥有两项权限，客户和未登录访客不能访问管理接口。浏览器会话和已授权管理员 API 密钥均可使用内容管理；角色/账号管理仍遵循其原有会话限制。

迁移 `036_content_management.sql` 为原有 `content.manage` 角色追加 `content.view` 并推进角色版本，其他角色不扩大授权。管理写入在事务内持有管理员管理共享锁和内容互斥锁，等待后重新检查有效账号、角色与两项权限；关联分类、版本和审计与写入原子提交。真实角色管理使用独占管理员锁，因此撤权与已开始的内容修改按锁顺序提交。

| API | 行为 |
| --- | --- |
| `GET /api/v1/admin/content` | 可按 `content_type`、`status`、`q`（标题或标识）、`category_id` 筛选，返回 `items/total/limit/offset`；列表省略正文 |
| `GET /api/v1/admin/content/{id}` | 完整详情，不增加阅读数 |
| `POST /api/v1/admin/content` | 创建；默认草稿、公开，返回 id/status |
| `PUT /api/v1/admin/content/{id}` | 全量保存，需要 `expected_version`；类型不可改变 |
| `DELETE /api/v1/admin/content/{id}` | 需要 JSON `expected_version`，永久删除 |
| `GET /api/v1/admin/content-categories` | 可按类型筛选，含关联文章数和版本 |
| `POST /api/v1/admin/content-categories` | 创建分类 |
| `PUT /api/v1/admin/content-categories/{id}` | 全量保存，需要版本，类型不可改变 |
| `DELETE /api/v1/admin/content-categories/{id}` | 需要版本；任何关联文章（含草稿、归档）都会阻止删除 |

文章字段为 `content_type/category_id/title/slug/excerpt/body/status/access_level/published_at/expected_version`。分类字段为 `content_type/name/slug/description/sort_order/active/expected_version`。保存响应不作为最新详情；再次 GET 取得当前版本。并发变更返回 409 `version_conflict`，界面提供重新加载；等待后的权限不足为 403，缺失对象为 404，重复标识或关联约束冲突为 409。

标题最多 240 字符、摘要 1000、正文 20000；分类名称 120、说明 500，排序 0–1000000。同类型标识唯一，使用小写字母开头及字母、数字、短横线，最长 120。正文按纯文本显示，换行/制表符保留，其他控制字符拒绝。文章 JSON 上限 256 KiB，支持四字节 Unicode 正文和 JSON 转义；其他普通 JSON 接口仍为 64 KiB。

## 发布与访问范围

`status` 为 `draft/published/archived`。发布且时间留空时新文章立即发布；编辑已发布文章留空会保留原发布时间。草稿或归档清除发布时间，再发布时重新计算。未来时间的文章在该时刻起通过查询可见，无需发布队列；后台 GET 的 `scheduled` 字段反映查询时状态。时间接受带时区的 JSON 时间，年份 2000–2200；界面要求填写时区并以北京时间显示。发布到隐藏分类会被拒绝；隐藏已使用分类会隐藏其文章，重新显示后恢复符合发布条件的文章。

新闻只能公开。知识库 `access_level` 为 `public`、`login`、`active_service`，后两种分别要求有效凭据、有效凭据且本人拥有 `active` 服务且 `import_review_required=false`。其他客户的服务、暂停服务和待复核历史服务不满足条件；此处沿用旧源码按 Active 状态判定的行为，不额外根据到期日期推导状态。管理员不会绕过客户详情的服务条件，可通过后台详情读取。

公开 API：`GET /api/v1/news`、`/news/{id}`、`/knowledgebase`、`/knowledgebase/{id}`。列表与详情同时排除草稿、归档、未来发布时间、隐藏分类及权限不足文章；列表搜索正文、标题和摘要，分页总数只统计允许读取的文章。`category_id=0` 表示未分类；不指定表示全部。分页默认 25、最多 100；页面每页 20。公开分类 `GET /api/v1/content-categories?content_type=news|knowledge` 返回全部启用分类的公开名称和说明，不公开隐藏分类或管理元数据，不按文章访问范围过滤分类名称。

可选 Cookie 或 API 密钥用于识别知识库阅读权限；有 Bearer 时优先使用它，无效、撤销、过期或停用凭据按匿名处理。拒绝详情统一 404，不暴露受限标题或正文；阅读数仅在成功详情事务中增加，返回增加后的数值。列表及详情不返回作者、版本、可见范围、状态等后台元数据。认证或分类在已经开始的请求中变化时遵循数据库快照和锁规则，不承诺撤销已经返回的正文。所有响应禁止缓存。

客户页 `/news`、`/knowledgebase` 提供搜索、分类和分页，知识库提示登录入口；详情的不可访问提示中性，不显示受限内容。手机长标题和正文自动折行，编辑弹窗在视口内滚动。

## 验证

专项隔离 PG18 测试覆盖权限与 API 密钥、已有角色迁移、CRUD、分类删除关联和并发分配、发布/归档/定时、Unicode 限制、公开字段、阅读计数、搜索分页、凭据过期/撤销/停用、服务归属/状态/导入隔离、并发编辑、等待撤权和审计失败回滚。全量 Go 测试包含强制真实旧库只读导入演练，HTTP API 包 30.488s；`go vet ./...`、前端 lint、类型检查与生产构建通过。

2026-10-05 隔离生产构建 Playwright 验收通过：实际分类/文章创建、草稿、定时及立即发布、隐藏与恢复分类、有关联分类删除拒绝、编辑冲突后重载、归档/删除、只读管理员控件及 API 拒绝、匿名搜索/详情无受限标题、登录客户读取、23 篇文章的 20/3 分页和搜索重置。正文内 `<script>` 按文本展示；390×844 弹窗与公共长正文无横向溢出，弹窗边界 x=16/width=358/height=759.59。有效服务正向权限由数据库集成测试证明，浏览器验证了无有效服务时的隐藏。

浏览器发现 HTML pattern 在 Chrome 的 Unicode v 模式下不接受未转义短横线，已修复并重验无效标识被阻止；更新后的两个前端生产构建均通过。隔离预览因外部 node_modules 软链使用 webpack，本目录使用 Turbopack。编辑重载和手机尺寸检查等待异步数据/布局稳定后通过；测试中主动触发的 409/404 属于预期拒绝，隔离浏览器还有一次 favicon.ico 404，未作为业务成功依据。fixture 774.648s 正常结束，随机 schema、临时 3089 服务和浏览器已清理。详细记录与截图在忽略目录 `output/playwright/content-acceptance-20261005.md`。

开发实例已应用 schema 036，API、worker 和 Next.js 服务运行，健康和就绪为 200。root 只读浏览器检查 27 项权限、空内容列表、知识库创建弹窗和手机布局，未保存任何文章或分类；退出后公共页正常，最终控制台无错误/警告。开发库仍有原来两个账号、零导入批次、零文章/分类，站点版本 1、注册开启、维护关闭。备份在 `bin/previous/20261005T031115Z/`，包含 schema 035 custom dump 和三个旧二进制；已读取 dump 目录，未演练恢复或完整切换回滚。旧 PHP 与 MySQL 未修改，整体重构继续按台账推进。
