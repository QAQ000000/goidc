# 下载中心

本批完成公共下载中心的 Go API 和 Next.js 页面，迁移 041。后台入口 `/admin/downloads`，客户入口 `/downloads`，管理导航和客户导航均可进入。客户专属文件已在后续迁移 042 完成，见 [客户文件](customer-files.md)；服务详情产品资料入口也已完成，见 [服务产品资料](service-downloads.md)。多层分类已由迁移043完成，见 [下载分类树](download-category-tree.md)。旧文件批量导入仍待迁移，不能据此宣称完整下载模块或整体重构完成。

## 源码与行为

依据旧项目 `app/admin/controller/DownloadsController.php`、`app/openapi/controller/DownloadsController.php`、`app/common/model/DownloadsModel.php` 和 `app/common/logic/Download.php`。旧文件包含标题/说明/分类、隐藏、登录要求、产品限制、本地/远程来源与下载次数；产品关联来自 `product_downloads`，产品客户条件为本人 Active 主机。

迁移041的分类最初为扁平分类，后续043已增加完整树及继承隐藏，见 [下载分类树](download-category-tree.md)。分类，名称最多 255 字符、说明 1000，排序 0–1000000。隐藏分类连同文件列表、分类计数和直接下载一起隐藏；含文件的分类拒绝删除，需先转移或删除文件。文件名称最多 255、说明 10000 字符，说明按纯文本展示。未分类可用；新文件默认隐藏，发布由管理员明确选择。

与旧代码不同，直接下载同时检查隐藏文件/分类；搜索使用参数查询并排除隐藏内容。产品限制检查全部关联产品，持有任意一个即可，修正旧 openapi 只检查首个关联产品的问题。已隐藏商品不影响已有有效服务的下载资格。

## 权限与下载

新增 `downloads.view` 和 `downloads.manage`，后者须同时具有前者；已有角色不会自动获得新增权限，超级管理员可以分配。后台读取包含隐藏文件和完整来源，写入在 RBAC 共享锁与下载目录锁后重新检查当前权限和账户状态。文件、分类、设置修改和审计提交同一事务，使用 `expected_version` 拒绝覆盖并发变更。

访问级别：

| 值 | 条件 |
| --- | --- |
| public | 匿名可下载 |
| login | 有效会话或有效 API 密钥且账户启用 |
| product | 上述登录条件，且本人持有任意关联产品的 active 服务，import_review_required=false |

产品限制至少指定一个产品，提交最多 200 个编号，保存时去重。其他客户、暂停或待复核导入服务不满足条件。沿用按服务状态判断的契约，不从到期日期另行推导状态。

公开列表展示锁定文件的标题/说明及 `can_download`，便于客户知道权限要求；不返回外部地址、存储内容、作者、后台版本或产品 ID 数组。列表提示不能代替下载时权限检查。隐藏文件或分类直接下载为 404；未登录要求为 401，缺指定产品为 403。无效、撤销、过期凭据按匿名处理。有有效 Bearer 格式时优先使用 Bearer，不借用同时提供的 Cookie。

下载事务在等待目录锁后重新读取凭据、下载开关、分类/文件状态和服务条件，随后读取文件并递增计数。并发替换、隐藏和下载在目录锁上排序；不承诺撤销已授权并返回的字节。GET 成功授权一次计一次，本地为开始响应，远程为返回地址，不能用计数证明外部文件实际下载完毕。HEAD 不递增、不返回内容；列表和后台详情不计下载。下载计数不推进编辑版本。

下载开关在后台可版本化启停。关闭时客户列表返回 `enabled=false` 和空项目、分类为空、直接下载为 403；后台可恢复。站点维护模式继续覆盖客户下载 API，后台保持可访问。

## 文件与外部链接

本地文件限制 50 MiB，支持 txt/vcf、png/jpg/jpeg/gif、pdf、zip/rar、doc/docx/xls/xlsx/ppt/pptx、rtf、key/numbers/pages。拒绝空文件、路径型名称、控制字符、未知扩展名及内容类型不符。文本、图片、PDF 检查探测 MIME；ZIP、RAR、OLE 和 RTF 检查文件签名，不能等同于杀毒或文档结构完整性验证。

文件记录字节长度、SHA-256 与原始文件名。默认保存 PostgreSQL `bytea`；部署配置可将新上传或明确替换的文件写入私有本地目录或 S3，按固定对象引用读取，历史字节不自动搬迁。完整配置、未知上传和延迟回收规则见[应用文件存储](architecture/file-storage.md)。文件绑定、元数据、产品关联和审计原子提交；外部上传后业务失败时保留 journal，由安全回收流程处理。不传 file 保留原内容；远程改本地必须上传，本地改远程解除原内容引用，替换保留下载次数。外置后备份须包含数据库和对象。API 仍一次有界读取最大 50 MiB，保留 HEAD、条件请求及受限多段 Range；大量并发下载容量尚未验收。

附件响应强制 `Content-Disposition: attachment`、UTF-8 文件名、nosniff 和 no-store，不在前端内联执行。浏览器按实际响应文件名保存，避免列表加载后被替换时继续使用旧名称。Next 配置请求缓冲上限 52 MiB；API 总 multipart 上限 50 MiB+256 KiB，metadata 最大 128 KiB。实际 11 MiB 文件已经通过 Next 生产预览上传和下载校验。

外部链接只接受完整 HTTP/HTTPS URL，拒绝用户密码、反斜杠和控制字符等无效地址。授权后 API 返回 JSON `{url}`，浏览器访问，Go/Next 不抓取目标，没有服务端文件路径或远程取文件功能。界面明确标为外部下载；外部服务器控制其自身文件权限，已取得地址无法靠本项目撤销，需外部站点提供鉴权。真实外部资源的可用性尚未验收。

## API

以下路径以 `/api/v1` 为前缀：

| 方法/路径 | 用途/权限 |
| --- | --- |
| GET /downloads | 搜索、分类和分页、锁定状态，可匿名 |
| GET /download-categories | 可见分类及可见文件数量，可匿名 |
| GET /downloads/{id}/file | 即时权限核对后返回本地字节或外部地址 |
| GET /admin/downloads | 分页完整文件列表，downloads.view |
| GET /admin/downloads/{id} | 文件完整元数据，downloads.view |
| POST /admin/downloads | 创建，downloads.manage |
| PUT /admin/downloads/{id} | 编辑/替换，downloads.manage |
| DELETE /admin/downloads/{id} | 删除文件及关联，downloads.manage |
| GET /admin/download-categories | 分类及全部文件数量，downloads.view |
| POST /admin/download-categories | 创建分类，downloads.manage |
| PUT /admin/download-categories/{id} | 编辑/排序/隐藏，downloads.manage |
| DELETE /admin/download-categories/{id} | 删除空分类，downloads.manage |
| GET /admin/download-products | 产品 ID/名称/启用状态供关联选择，downloads.view |
| GET /admin/download-settings | 下载开关/版本，downloads.view |
| PUT /admin/download-settings | 设置开关，downloads.manage |

文件创建/更新使用 multipart，`metadata` 为 DownloadInput JSON，`file` 为可选单文件。metadata 包含 category_id/null、title、description、active、access_level、source_type、remote_url、product_ids、expected_version。删除使用 JSON expected_version；分类和设置使用 JSON。搜索 q 匹配标题/说明；category_id 不传为全部、0 为未分类，非法值 400；分页默认 25、最大 100，页面每页 20。

## 验证与剩余范围

后续迁移044已完成旧分类/公共文件/产品关联及客户文件受控导入，包含字节指纹、源归档、层级和权限保留、现场修改重跑保护及失败回滚；当前真实源下载四表为空，非空文件由隔离夹具和API验证。部署与限制见 [旧下载资料迁移](legacy-download-files.md)。以下为原下载中心批次证据。

隔离 PG18 测试覆盖本地字节和 SHA、替换保留次数、保留原文件、来源切换、隐藏文件/分类直接下载、分类引用删除保护、字段/路径/MIME/50 MiB 上限、管理员只读/API 密钥、账号停用/凭据撤销、第二个关联产品、服务归属/暂停/导入复核、关闭/恢复、并发更新和计数、等待期间凭据/管理权限撤销、审计故障原子回滚及 HEAD。031 历史权限迁移仍严格保留 26 项原权限，不因本批扩权。

强制全量回归 `REQUIRE_INTEGRATION=1 REQUIRE_LEGACY_REHEARSAL=1 go test ./... -count=1` 与 vet 通过，HTTP API 包 50.949s、真实旧库导入演练包 12.283s。前端 lint/typecheck/Turbopack 生产构建通过。2026-10-05 旧 MySQL 只读事务检查 downloadcats/downloads/product_downloads/userdownloads 均 0 行；本批未写入旧库，也未执行下载文件导入。

隔离生产预览浏览器验收和开发实例部署结果见本批验收记录与下方部署记录。客户专属文件已通过所有权及管理员销售范围锁独立保护，契约见后续 [客户文件](customer-files.md)；产品关联、服务列表与实际下载见 [服务产品资料](service-downloads.md)。旧本地文件需要受控根目录、源归档、ID 映射、SHA 与重跑保护，不能直接接受网页提交的服务器路径。分类层级已完成；旧文件导入、手工路径登记与大容量存储/续传继续保留在迁移台账。

2026-10-05 隔离生产预览 Playwright 验收通过：超级管理员实际创建分类与文件，中文文件实际下载与原上传 SHA-256 一致；11 MiB 文件通过 Next 上传替换后下载字节/名称一致、下载次数保留；关闭/恢复中心，匿名登录文件 401 提示、公开文件可下载、登录客户受限文件下载、分类筛选与无结果搜索、只读管理员无新建/保存/删除入口且字段/开关禁用；390×844 客户页面与只读详情无横向溢出。浏览器未逐一验证产品正向权限、编辑冲突、外部站点连接和分页翻页，它们中产品/冲突/分页由数据库集成测试覆盖，外部连接仍未验证。晚补 23 文件的 20/3 分页、搜索、分类公开/后台计数专项 0.485s 通过，vet 再次通过。文件大小单位展示小改动后主前端 lint/typecheck/Turbopack 构建通过；隔离预览使用 webpack。

浏览器主动触发的 401 控制台记录属于预期权限拒绝；分类筛选的一次即时断言读取到了加载状态，改为等目标标题加载后重验通过。浏览器 fixture 外层命令最终返回 143，未取得正常 PASS 结束日志，不能将其作为测试包通过依据；真实浏览器交互、下载文件和 API 结果独立保存，最终核对该 fixture/API 进程已退出、测试随机 schema 为 0、3089 监听及预览目录已清理。详细截图与本批验收记录在忽略目录 `output/playwright/`。

开发实例已部署迁移 041：29 项权限、121 条后台路由，三个项目服务 active，API healthz/readyz 与 `/downloads`、代理下载列表/分类均 200。root 实际会话只读检查下载设置版本 1/启用、0 文件/分类、4 商品、29 权限、0 导入批次后注销。users/products/orders/services/order_items 完整 JSON 聚合摘要部署前后相同，站点版本 1/注册开/维护关保持。旧 PHP/MySQL未修改。备份 `bin/previous/20261005T060649Z/schema040.dump` 391349 字节、可读取目录 458 行，包含四个旧程序和前后完整性摘要；未演练备份恢复或完整切换回滚。
