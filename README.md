# ZJMF Go

PHP 财务系统的独立 Go API / Next.js React shadcn/ui / PostgreSQL 18 重构。业务覆盖和剩余工作见 [重构台账](docs/reconstruction.md)。当前仍在开发，不能替换原 PHP 生产站点。

后台支付渠道和SMTP设置已支持加密存储、版本审计与运行时生效，邮件模板和工单预设补齐配置操作。支付单本地过期后可轮换商户，历史单保留加密凭据用于迟到通知和查单；模拟支付默认关闭。入口、验收链路和仍缺少的配置见 [后台配置](docs/admin-configuration.md)。

开发实例已部署最新迁移：验证码找回、邮箱注册验证、邮件/短信通知渠道和 SMTP 465 隐式 TLS 已接通。浏览器隔离链路、PG18 回归、race/vet、前端检查构建和现场健康检查通过；真实 SMTP 收件、短信供应商、支付、供应商和其他领域仍待完成。

## 本地运行

配置 `.env`（数据库 URL、API 监听地址、前端来源、Cookie 是否 HTTPS）。数据库角色仅操作本项目独立库；示例中没有实际密码。

```bash
set -a
source .env
set +a
go run ./cmd/migrate
BOOTSTRAP_USERNAME=root BOOTSTRAP_EMAIL=root@example.com BOOTSTRAP_PASSWORD='<本地管理员密码>' go run ./cmd/bootstrap
go run ./cmd/api
go run ./cmd/worker
```

旧 MySQL 只读盘点使用独立命令，不会连接或写入 PostgreSQL：

```bash
LEGACY_MYSQL_DSN='idc:只读密码@unix(/tmp/mysql.sock)/idc?parseTime=false' go run ./cmd/legacy-audit
```

审计命令输出 JSON 表计数及异常，不写入任何数据库。`cmd/legacy-import` 默认只读预检，明确指定 `--mode apply` 才会向配置的 PostgreSQL 库导入；历史账单和服务保持待复核，商品默认下架，旧密码需要重置。导入范围、重跑限制和真实旧库演练结果见 [旧数据迁移](docs/legacy-migration.md)。

管理员迁移复核入口为 `/admin/migrations`，可查看导入批次、旧字段、异常和处理记录。只有超级管理员浏览器会话可提交带凭据的归档或恢复决定；账单金额、服务归属或资源未核验时仍保持操作限制。历史已付账单不会通过复核直接获得退款权限。

管理员初始化仅允许没有管理员的数据库，不会重置已有用户。`go run ./cmd/demo` 可选择插入开发商品，不创建真实资源或业务订单。

超级管理员可在 `/admin/access` 管理普通管理员和角色，权限区分读取、修改、充值和退款；撤权对后续请求立即生效。账号停用、改角色、改工单部门范围或重置密码会撤销旧会话和 API 密钥。接口契约、旧系统范围差异及验收见 [管理员权限](docs/admin-rbac.md)。

```bash
cd web
npm ci
npm run dev
```

API 还提供客户服务生命周期接口：`GET /api/v1/services`、`POST /api/v1/services/{id}/actions`；付款后由 `cmd/worker` 消费开通队列。商品默认采用人工交付：worker 将任务置为 `waiting_manual`，管理员通过 `POST /api/v1/admin/services/{id}/delivery` 登记实际资源标识和核验记录后才会启用服务。只有明确将商品 `provision_mode` 设为 `simulation` 的开发商品才会生成 `local-*` 模拟资源。

账单支持原生易支付 MD5 协议及 `mock` 网关。后台或部署配置 `EPAY_API_URL/PID/KEY/TYPES` 后，客户在账单详情选渠道并继续付款；签名回调事务校验账单、金额及渠道流水，支持重复通知，维护期间仍接收通知。待处理在线支付支持本人主动查单、独立后台权限及查询记录；worker 自动查询采用租约、有限重试与逐步退避，确认已付款后补结算，未付款或失败保留原单。账单显示查询来源及下次时间，次数用完后可手动核对。未配置商户且没有明确开启模拟支付时不提供在线渠道。模拟回调保持 POST JSON、`PAYMENT_GATEWAY_SECRET` HMAC-SHA256 和 `X-Payment-Signature`。真实商户付款验收、关单和原路退款尚未验证或实现，配置及限制见 [在线支付](docs/payments.md)。

客户可通过 `POST/DELETE /api/v1/services/{id}/cancellation` 提交或撤回服务取消申请，管理员在 `/admin/cancellations` 审核；立即取消会进入终止队列，到期取消任务按服务 `next_due_at` 延迟执行。客户账户页支持 `PUT /api/v1/account/password` 修改密码，修改后除当前会话外的旧会话都会失效。

管理员可通过 `PATCH /api/v1/admin/customers/{id}` 更新客户姓名、邮箱和启用状态；停用客户会立即撤销其全部会话，并写入 `customer.updated` 审计事件。

登录成功、失败、要求重置密码和注销会写入审计事件（不会记录密码），便于后台审计页追踪认证行为。

登录页提供验证码密码找回：`POST /api/v1/auth/password-reset/request` 返回短期 `challenge_id`，验证码只保存 HMAC，10 分钟过期、单次消费并限制错误尝试；`POST /api/v1/auth/password-reset/confirm` 消费验证码并撤销用户会话、MFA 挑战和 API 密钥。验证码正文进入加密 outbox，由邮件或短信 worker 投递；未配置通知渠道时不会向浏览器返回验证码。注册邮箱验证和账户联系方式验证复用同一挑战接口。

账户页提供 TOTP 两步验证：`POST /api/v1/account/mfa/enroll` 返回一次性密钥和 8 个恢复码，`POST /api/v1/account/mfa/confirm` 确认启用，`POST /api/v1/account/mfa/disable` 需要当前密码及验证码或恢复码。密钥使用 `MFA_ENCRYPTION_KEY`（32 字节、64 位十六进制）以 AES-GCM 加密后保存；登录先返回 5 分钟一次性挑战，验证码通过后才创建会话。恢复码只保存哈希并逐个消费。

管理员可在 `/admin/notifications` 编辑通知模板并查看邮件队列，在 `/admin/smtp-settings` 配置 SMTP，在 `/admin/sms-settings` 配置短信 HTTP JSON 供应商；API 为 `/api/v1/admin/notification-templates`、`/api/v1/admin/email-outbox`、`/api/v1/admin/smtp-settings` 和 `/api/v1/admin/sms-settings`。凭据使用 AES-GCM 加密，列表不会返回正文或秘密，失败任务可重试。465 端口使用隐式 TLS，587 默认 STARTTLS；短信供应商采用注册表，当前内置 generic HTTP JSON，真实供应商仍需单独验收。

超级管理员可在 `/admin/settings` 配置站点名称、Logo、公司与联系信息、条款/隐私链接、备案及邮箱注册开关。页面品牌、标题、描述与页脚使用公开 `GET /api/v1/site` 数据；管理读写 `/api/v1/admin/site-settings` 只接受超级管理员浏览器会话，保存检查版本并原子写审计。关闭注册保留既有账户登录，排队注册也会重新检查开关。范围与验收见 [站点设置](docs/site-settings.md)。

维护模式在同一设置页启停，可配置纯文本公告或外部 HTTPS 跳转。客户业务 API 返回 503；客户页面显示公告并返回 503，或 307 临时跳转。管理员通过 `/admin-login` 恢复访问，已登录管理员可预览客户页面；设置修改权限不变。支付回调与 worker 继续按原规则运行，已开始的请求不强制回滚。前端其他端口部署需设置 `WEB_INTERNAL_URL` 为 Next.js 本机地址，默认 `http://127.0.0.1:3088`。

客户账户页可以创建和撤销 API 密钥。密钥通过 `Authorization: Bearer zjmfgo_...` 调用 API，完整密钥只在创建响应显示一次；服务端只保存哈希，并校验客户状态、撤销时间和过期时间。密钥管理接口 `GET/POST /api/v1/api-keys`、`DELETE /api/v1/api-keys/{id}` 只接受浏览器会话。

已启用或已暂停的周期服务可通过 `POST /api/v1/services/{id}/renew` 生成独立续费账单；账单使用余额支付后，服务到期时间按月、季或年延长，续费请求支持幂等重放。

已启用且未到期的月/季/年服务支持原生配置变更。后台商品配置页开放变更及降配策略，客户在服务详情计算差额并生成独立账单，付款后进入人工或模拟交付；交付完成才更新有效配置与续费金额，降配差额返还余额。待处理变更与续费、服务动作及取消互斥，失败交付可由有权限的管理员重试。报价与金额、退款及剩余范围见 [服务配置变更](docs/service-configuration-changes.md)。

跨商品升级沿用同一变更账单和交付流程，后台维护目标商品及降级策略；提交预留目标库存，交付后切换服务合同并释放源库存，保留历史订单。当前保留原月/季/年账期、要求类型与交付模式兼容，详见 [跨商品升级](docs/service-product-changes.md)。

商品升级专用优惠支持百分比/固定金额和适用条件、限次，成功付款时核销，账单保存原差额与优惠快照，不改变后续续费合同。详见 [商品升级优惠](docs/upgrade-promotions.md)。

产品可配置多个账期价格。目录返回 `pricing` 矩阵，客户在加入购物车时可提交 `billing_cycle`；购物车和结算会锁定对应价格并校验产品 `revision`。管理员通过 `POST /api/v1/admin/products/{id}/pricing` 更新或新增账期价格，传入 `default: true` 可同步产品默认账期。

管理员可在商品详情使用 `POST /api/v1/admin/products/{id}/duplicate` 复制目录商品。复制包含商品账期价格、配置组/选项及其账期价格、产品资料关联，并要求新的名称和标识；订单、服务、购物车、旧身份和审计历史不会复制。

产品还支持配置项组。后台商品详情提供配置组/选项的创建、编辑、启停、排序与删除保护，并可维护旧配置类型、数量最小/最大值、步长和单位；公开目录仅返回启用配置，客户提交 `option_ids`，数量型配置同时提交 `option_quantities`。必选组、跨产品选项、重复组、停用项、数量范围和步长均由购物车和结算事务校验，配置价格按数量计入周期价与初装费，订单保存稳定 ID、数量及名称/值/增价快照，周期价包含配置增价并用于续费。契约及待迁移范围见 [商品配置维护](docs/product-configuration.md)。

`option_quantities` 是以选项 ID 为键的整数映射，例如 `{"12":4}`；配置数量与项目购买件数独立。固定价选项只允许数量 1，省略时默认 1；数量型选项必须提供数量。旧数量区间联动和数量阶梯价格已经由 Go 在购物车、优惠码和结算服务端统一校验，阶梯通过后台按账期维护；旧数量配置导入时会折叠旧区间子项并保留别名映射，导入商品仍需复核后才能上架。

客户可在账户页维护联系人资料、通知偏好和权限标签；管理员可在客户管理中维护联系人及客户分组。联系人邮箱与主账户及其他联系人全局唯一，分组删除前必须先解除客户归属。对应 API 为 `/api/v1/contacts`、`/api/v1/admin/customers/{id}/contacts` 和 `/api/v1/admin/customer-groups`。

管理员可在 `/admin/promo-codes` 创建优惠码；客户通过 `POST/DELETE /api/v1/cart/promo` 应用或移除优惠码，结算会在同一事务内锁定优惠码、重新验证规则、写入订单折扣快照并核销使用次数。支持百分比、固定金额和免初装费；百分比及固定金额可保存循环续费权益并限定每件服务优惠次数。首次购买不占续费次数，成功续费付款才消耗一次，退款不恢复次数；手动及自动续费共用实付计算，停用不撤回已购权益。商品升级差价专用优惠已实现，规则、快照和核销约束见 [商品升级优惠](docs/upgrade-promotions.md)。

商品可选择人工确认、开发模拟或 NOKVM 自动交付。NOKVM 商品在后台选择部署 profile 和固定资源规格，下单保存交付快照；Go worker 按旧协议签名调用开通、暂停、恢复、终止、同步及开机/关机/重启/强制电源接口。电源成功仅表示受理，随后同步实际状态，不改变计费、库存或余额。供应商 token 只在服务端环境，系统密码使用 AES-GCM 加密，普通详情不返回。超时、断网或本地落库失败会进入待核验，禁止盲目重试；管理员确认供应商控制台后登记资源或结束任务。供应商配置、HTTP 限制、权限和恢复规则见 [NOKVM 原生交付](docs/nokvm-delivery.md)。

NOKVM 系统密码重置使用专用API和任务绑定的加密请求，供应商受理后读回确认才更新本地凭据，保留VNC密码及计费。结果不明阻止凭据读取，管理员核验可完成或结束任务，不自动重复请求；前端支持密码确认、随机生成和显隐，权限与恢复契约见同上文档。

NOKVM 系统重装使用后台商品镜像白名单、不可变任务快照和数据清除确认。上游成功响应只记为受理；管理员核验安装完成与新密码后更新当前系统及凭据，独立于购买快照并同步资源状态。重装期间锁定旧密码读取，未知结果不重发，保持计费和库存；真实供应商尚未验收。

NOKVM 快照/备份支持列表、创建、删除和恢复，按购买额度和主机归属校验。全部写操作待核验；恢复期间禁止读取旧密码，管理员确认实际系统与新密码后更新系统记录及加密凭据。不可变任务、幂等及已发送恢复机制禁止自动重发，计费、库存和VNC凭据保持。真实供应商尚未验收，详见 [快照与备份](docs/nokvm-storage.md)。

NOKVM 服务详情支持 CPU、网络和系统盘/数据盘 I/O 监控，提供 1小时、6小时、24小时、7天范围、手动刷新及 CSV 导出。累计流量来自供应商独立计数，不代表所选图表时间窗；缺失采样保留为空，只读管理员可以查看。时区配置、协议及验收边界见 [NOKVM 原生交付](docs/nokvm-delivery.md) 和 [监控验收](docs/nokvm-monitoring-acceptance.md)。

管理员统计报表位于 `/admin/reports`，API 为 `/api/v1/admin/reports/income`、`/new-clients`、`/product-income` 和 `/customer-ranking`。报表以支付时间、创建时间和北京时间自然日为边界，金额使用整数分，覆盖年度收入、月度每日运营、新购/续费产品收入和客户流水排名。

客户订单、客户账单和后台账单支持搜索、状态筛选、分页及详情，展示下单快照、服务项目与付款/退款记录。购物车结算、续费和升级直接进入对应详情；模拟支付与真实渠道明确区分，详情读取按账户归属和管理员销售范围保护。接口和验证边界见 [订单与账单](docs/transactions.md)。

客户可维护发票抬头和收货地址，并对已付款账单提交发票申请；后台独立权限审核、驳回或登记实际开票编号与快递信息，申请保存不可变的提交快照。当前采用人工登记，详见 [发票申请](docs/invoice-applications.md)。人工账单可将同一客户的 2 至 20 张未支付账单合并为新账单，保留原单据和项目历史。

购物车项目可以通过 `PATCH /api/v1/cart/items/{id}` 更新数量、账期、`option_ids`、`option_quantities` 和 `purchase_fields`；接口会在同一版本锁内重新校验商品价格、库存、配置和购买资料，并更新购物车版本，前端购物车提供编辑入口。商品详情可维护文本、长文本、下拉、勾选和网址字段；订单/服务保留购买时资料，账单仅展示获准字段，详见 [产品购买字段](docs/product-purchase-fields.md)。

周期服务可通过 `PATCH /api/v1/services/{id}/auto-renew` 开关自动续费。Worker 每轮扫描未来 24 小时到期的服务，使用基于服务和到期时间的幂等键生成续费账单并尝试余额支付；余额不足会保留错误信息和未支付账单，客户可继续手动支付。

管理员在 `/admin/content` 管理新闻、知识库和分类，支持版本化编辑/删除、草稿/发布/归档、定时发布，以及知识库公开/登录/有效服务客户可见范围。客户页 `/news` 和 `/knowledgebase` 提供分类、搜索、分页和详情，所有读取统一检查可见范围；权限、接口和剩余迁移范围见 [内容管理](docs/content-management.md)。

公共下载中心已迁移：后台 `/admin/downloads` 支持分类、50 MiB 文件上传/替换、外部链接、公开/登录/指定有效产品权限及下载开关；客户 `/downloads` 提供搜索、分类、分页与下载。文件和 SHA-256 存储在 PG，编辑具备版本冲突和事务审计。服务详情“产品资料”使用产品关联并即时校验本人服务和下载权限，见 [服务产品资料](docs/service-downloads.md) 和 [下载中心](docs/download-center.md)。旧分类/公共及客户文件/产品关联已纳入受控只读导入，见 [旧下载资料迁移](docs/legacy-download-files.md)。

客户专属文件入口为后台 `/admin/customer-files`、客户管理内的文件链接和客户 `/console/files`。独立文件权限与客户销售范围共同保护管理员访问；客户只可下载本人文件。支持上传/替换、客户转移、搜索分页、版本保护和原子批量删除，沿用 50 MiB 白名单和下载开关；契约及验收见 [客户文件](docs/customer-files.md)。

下载分类支持多层目录、完整路径、父目录筛选包含后代、上级隐藏继承与原子移动；禁止循环归属和非空删除。现有扁平分类升级为顶级并保持内容，详见 [下载分类树](docs/download-category-tree.md)。旧文件导入保留层级和文件SHA，重复导入保护现场修改，删除产品关联后要求复核。

工单支持 `GET /api/v1/ticket-departments` 查询启用部门，创建时可提交 `department_id`；超级管理员在 `/admin/ticket-departments` 创建、编辑、停用或删除空部门；普通管理员的 `tickets.view/manage` 还受账户部门范围约束，在 `/admin/access` 配置。`GET /api/v1/admin/ticket-assignees?department_id={id}` 查询该部门可用处理人（`0` 表示未指定），并使用 `PATCH /api/v1/admin/tickets/{id}/assignment` 分派部门和处理人。分派会校验部门与管理员状态、推进工单版本并写入审计事件。

工单附件使用 `multipart/form-data` 的 `POST /api/v1/tickets/{id}/attachments` 上传，字段名为 `file`，单文件限制 10 MiB；详情返回附件元数据，`GET /api/v1/tickets/{id}/attachments/{attachment_id}` 下载，`DELETE` 删除。附件存储在 PostgreSQL `bytea` 中，服务端校验文件名、MIME、危险扩展名和 SHA-256，并在每次操作时复核工单所有权；跨客户读取需要工单读取权限及部门范围，写入需要工单操作权限及部门范围。

管理员工单详情支持 `POST /api/v1/admin/tickets/{id}/receive` 接单、`POST /api/v1/admin/tickets/{id}/transfer` 转移、`POST /api/v1/admin/tickets/merge` 合并同一客户工单，以及 `/api/v1/admin/tickets/{id}/notes` 管理内部备注。合并会在同一事务内迁移回复、备注和附件，并将被合并工单标记为不可继续访问。

预设回复管理页位于 `/admin/ticket-presets`，管理员可通过 `/api/v1/admin/ticket-reply-preset-categories` 和 `/api/v1/admin/ticket-reply-presets` 管理分类及模板；工单回复框可以直接插入启用的模板。

待支付订单支持 `POST /api/v1/orders/{id}/cancel`（管理员路径为 `/api/v1/admin/orders/{id}/cancel`），取消会原子关闭未支付账单、终止尚未开通的服务、取消队列任务并恢复有限库存。管理员订单列表支持 `status` 和 `q` 筛选；`POST /api/v1/admin/orders/bulk-cancel` 接收最多 100 个订单编号逐项处理，使用请求幂等键派生订单级幂等键，并返回成功/失败明细。后台 `/admin/orders` 提供勾选和批量取消。

产品创建时可启用 `approval_required`。这类商品支付后订单进入 `review_status=pending`，服务保持待支付状态且不会进入开通队列；管理员通过 `POST /api/v1/admin/orders/{id}/review` 审核通过后才排队开通，也可驳回并保留人工退款决定。订单详情为 `GET /api/v1/admin/orders/{id}`，备注通过 `POST /api/v1/admin/orders/{id}/notes` 管理，后台页面为 `/admin/orders/{id}`。

前端默认 `http://localhost:3088`，Go API `127.0.0.1:8088`，`/healthz` 活性、`/readyz` 数据库就绪。Next.js 代理 `/api/*`，前端不直接跨域连接 Go。

## 验证

商品目录支持两级购买分组、说明/标题/标语、排序和隐藏，后台 `/admin/products` 提供商品维护及账期价格编辑，`/admin/catalog-groups` 管理分组。版本与库存快照避免覆盖并发变更，默认展示价格和结算价格同步；完整 API、权限和待迁移范围见 [商品目录与维护](docs/catalog-management.md)。

客户服务导航单独在 `/admin/service-groups` 管理，商品详情的“服务归属”用于既有及新购服务。客户可隐藏/恢复导航，并按分组、状态、关键词分页查看服务；隐藏不影响访问权限，新购恢复该组导航，删除分组可原子转移商品。默认显示、权限及迁移边界见 [服务导航](docs/service-navigation.md)。

旧数据导入器也保留服务导航分组、商品归属及客户显示偏好：源 ID 转换、缺失关联归档、已初始化默认隐藏规则及现场修改重跑保护已在真实旧库的隔离目标验证。开发实例没有 apply 旧数据；导入范围与正式切换的剩余工作见 [旧数据迁移](docs/legacy-migration.md)。

```bash
go test ./...
go vet ./...
cd web
npm run lint
npm run typecheck
npm run build
```

集成测试要求单独的 `ZJMFGO_TEST_DATABASE_URL`，数据库名必须以 `_test` 结尾；每个测试创建并清理独立随机 schema，不允许使用开发库。设置 `REQUIRE_INTEGRATION=1` 可使缺少隔离数据库配置时直接失败。Go/TypeScript阶梯预览对照测试另需 Node.js 和 `web` 的 TypeScript 依赖；`ZJMFGO_REQUIRE_WEB_TESTS=1 go test ./internal/store -run TestOptionPricePreviewMatchesGo -v` 可强制执行，缺依赖时失败。真实旧库演练另需 `ZJMFGO_LEGACY_TEST_MYSQL_DSN` 和 `REQUIRE_LEGACY_REHEARSAL=1`，源库只读。测试脚本/浏览器证据放 `output/playwright/`，不进入版本库。静态源路由重新扫描：`node scripts/inventory.mjs`。旧路由与当前 Go 路由的证据索引：`node scripts/route-coverage.mjs`，结果写入 `docs/generated/route-coverage.json`；该文件不代表兼容完成，见 [替换准备度与路线图](docs/rewrite-readiness.md)。

仓库内 Go 命令优先使用 `scripts/go.sh`，先运行 `scripts/check-go-toolchain.sh` 检查实际版本、`GOROOT` 和 `GOTOOLDIR`，避免用户级 Go 与 `/usr/local/btgo` 混用。
