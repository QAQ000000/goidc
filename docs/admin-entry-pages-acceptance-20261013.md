# 六个后台独立入口验收

日期：2026-10-13。对照旧规范第 13、29、88、102、104、105 页，补独立入口并复用已有真实业务能力。本批使用三个 GPT-6.1-sol 独立工作分支；提交经检查后合并，管理员编辑由主分支实现并经子代理两次复核。

## 页面与契约

| 页面 | 路由与真实 API | 本批行为与限制 |
| --- | --- | --- |
| 交易流水 | `/admin/transactions`；`GET /admin/transactions/page` | 成功支付、余额账本、缺少支付记录的已付账单事件；关键词、类别、方向、渠道、上海日期、整数分金额及分页；`items/total/limit/offset`；客户范围同时限制列表和 total。信用结算明确为中性事件，不混算现金合计。 |
| 推介设置 | `/admin/affiliate/settings`；`GET/PUT /admin/affiliate/policy` | 默认计划、归因与冻结周期、四类默认佣金、上限、提现门槛；生成新规则版本，克隆商品覆盖及阶梯规则，历史佣金不重算。停用沿用现有业务规则：停止新申请、归因、计佣及新提现，旧提现申请仍可处理。 |
| 供应商商品目录 | `/admin/suppliers/catalog`；账户列表、原目录/详情/导入 API | 独立账户选择、手动只读外调、本地快照筛选分页与原导入表单。入口需 `products.view`，外调、详情与导入仍需 `products.manage`。本批浏览器验收使用真实无账户空态；真实上游导入未在此新入口重测。 |
| 添加商品分组 | `/admin/catalog-groups/new?kind=section\|category`；`GET/POST /admin/catalog-groups/{kind}` | 复用分组表单和真实一级父分组；列表跳转及返回保留层级；创建增加绑定请求内容的幂等回执、事务权限/凭据复核与审计。原无键 API 调用保持兼容。 |
| 编辑管理员 | `/admin/access/staff/{id}`；`GET/PATCH /admin/staff/{id}` | 超管会话限定、超级管理员账号保护；复用账号/角色/客户及工单范围字段；密码确认、提交及重置确认、离开提示。写入事务复核会话，版本冲突与幂等回放；权限与凭据变更撤销旧会话/API 密钥。密码回执仅保存 bcrypt12 验证器，不保存明文或密码的快速摘要，不在浏览器存储密码。 |
| 创建短信模板 | `/admin/notifications/new?channel=sms`；`POST /admin/notification-templates` | 复用模板编辑器、变量校验、供应商模板 ID、启停；创建幂等、事务权限/凭据重查、审计 version1；返回保留通知列表筛选。创建模板不会发送短信。 |

新增全局导航包含交易流水、推介设置和供应商商品目录；只读账号重定向到客户列表时，分组/销售候选请求按 `customer_groups.view` / `@super` 读取，避免越权副请求；客户资料中的分组选项也按分配权限读取。创建与编辑入口从相应列表进入。没有新增迁移，复用现有回执表；迁移到 133 在本批每个隔离测试 schema 中重复执行并检查。

## 自动化结果

| 检查 | 结果 |
| --- | --- |
| `scripts/go.sh test ./... -count=1 -timeout=25m` | PASS；启用 `REQUIRE_INTEGRATION=1` 的隔离 PG18；HTTP 集成 709.544 秒 |
| `scripts/go.sh vet ./...` | PASS |
| `TestTransactionEntriesBrowserFixture` | PASS；隔离浏览器 API 正常结束并清理 schema |
| `TestAdminStaffDetailAndSafeRetry` | PASS；详情权限/脱敏、并发回放一次写入、一次审计、版本与内容冲突、会话失效拒绝、密码 bcrypt12 回执与不同密码拒绝 |
| `TestAdminEntryCreation*` | PASS；分组/模板并发回放、内容冲突、权限撤回、失效凭据拒绝、审计失败回滚 |
| `TestTransactionEntries*` | PASS；真实事件来源与去重、30/29 条权限范围、筛选分页、上海日期边界、只读无审计写入 |
| `TestAffiliatePolicy*`、`TestSupplierCatalogEntry*` | PASS；设置权限、版本/幂等、商品/阶梯快照保留、停用行为、目录只读与原导入权限/成本/快照边界 |
| 专项 `-race` | PASS；管理员、分组/模板创建、交易流水、RBAC 路由覆盖 |
| `npm run typecheck --prefix web` | PASS |
| `npm run lint --prefix web` | PASS；0 错误，8 个既有警告 |
| 独立副本 `npm run build -- --webpack` | PASS；最终副本含六个新路由及窄屏修复，未覆盖运行中的 `.next` |
| `git diff --check` | PASS |

## 浏览器证据

使用 Playwright 技能、系统 Chrome、独立 Next.js 生产预览 `127.0.0.1:3101` 和 `TestTransactionEntriesBrowserFixture` 的真实 Go API。数据由隔离测试库中的真实充值、账单、支付、退款和信用操作生成，页面没有静态假数据。

每个入口均检查 1440×900、1024×768、390×844、320×800，共 24 张截图。最终稳定验收区间无页面级横向溢出、无 JavaScript 异常、无失败 API；退出按钮始终在视口内。修复了 CSS 中 `.admin-header-search` 覆盖响应式 hidden 导致窄屏账户操作被挤出视口的问题。1440 与 320 截图做了视觉对照，1024 与 390 检查布局；没有宣称逐字检查全部长表格内容。

截图位于 `output/playwright/admin-entries-20261013/{transactions,affiliate,supplier,group,staff,sms}-{1440,1024,390,320}.png`。稳定布局结果位于 `layout-final.json`；截图摘要及逐尺寸记录已加入 `docs/admin-restore-browser-evidence.json`。

已操作确认：短信模板保存后返回短信列表并读到新记录；一级分组创建后返回正确层级并显示新分组；推介设置取消后恢复原值、保存回读版本 2；管理员经确认弹窗保存并回读版本 2；交易流水下一页为 2/2，刷新恢复分页 URL，关键词无结果显示真实 `暂无数据` 和总数 0。

推介设置浏览器真实并发更新由版本 2 写至 3，再提交旧版本返回 409，显示草稿保留和对照恢复提示；证据为 `affiliate-conflict.png`。预期 409 属于主动冲突验证，未混入稳定布局验收统计。只读账号浏览器验证得到 29 条流水（超管 30 条），其他客户不可见，管理员详情和推介设置 API 返回 403，禁止入口返回客户列表。交易流水另验证了延迟响应时的加载骨架、注入预期 503 错误后的提示，以及撤销故障后点击重新加载恢复 29 条真实记录；证据为 `transactions-error.png`。其余页面和未知保存恢复的完整逐页浏览器状态矩阵仍需继续补验；服务端权限、冲突、幂等、审计已经专项与全量集成覆盖。本批未进行真实短信投递、真实上游新入口导入或正式部署验收。

## 台账与交付边界

台账六项保守保持「部分完成」：供应商筛选 URL 恢复、推介商品及阶梯规则编辑、管理员语言/工单签名/业务邮件偏好，以及完整状态矩阵仍待补齐。静态生成索引的路由或 API 命中只代表源码存在，不代表旧版 105 页全部深度还原完成。

本批未部署至运行站点，运行环境迁移 133 尚未应用。临时预览、测试数据库/角色和凭据在验收后删除；仅保留截图和日志证据，不生成历史二进制备份。
