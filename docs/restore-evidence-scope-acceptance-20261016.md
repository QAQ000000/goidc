# 台账与截图证据范围修正验收（2026-10-16）

基线：`f651dfa`。本批只修正文档、生成契约和只读校验工具；未修改业务、未连接数据库、未运行浏览器。历史验收原有 `result`、`checks`、`scope`、`screenshotSha256` 和截图文件全部保留。以下静态能力映射不等于本次端到端验收通过。

## 台账能力映射

105 项当前登记为 **44 已完成、61 部分完成、0 待实现、0 后端阻塞**；36/38/103 因已知旧版关键能力缺口从已完成降为部分完成。台账下方 2026-10-12/13 的批次数值是历史快照，未改写。

| 条目 | 已有能力及源码 | 缺口与登记依据 |
|---|---|---|
| 36 优惠码 | `web/src/components/data-pages.tsx` 的 `PromoCodesView` 读取真实列表，新增、启停、循环续费、升级专用优惠和本页启停状态过滤；`web/src/components/admin-order-create.tsx` 的建单优惠码目录已使用 `/admin/promo-codes/page` 分页 | 主页面仍读取数组 `/admin/promo-codes`，未接起止时间编辑、时间列与真实分页；建单目录能力不能代替主页面还原 |
| 38 合同设置 | `web/src/components/financial-settings.tsx` 的设置视图与 `web/src/components/contracts.tsx` 的模板视图：总开关、纯文本模板创建编辑、启停、分页、版本和草稿；`internal/httpapi/contracts.go` 对接真实 API | 强制签订和商品模板绑定策略未实现；已有合同生成/确认/审核/邮寄生命周期仍保留其原验收范围 |
| 103 编辑商品 | `web/src/components/product-editor.tsx` 当前分区：基本信息、定价、服务归属；本地商品另有配置项、配置规则、购买字段，供应商品另有只读供应来源 | 当前分区与旧版十分区不等价；按下表逐项保留已有能力与剩余编辑入口/语义缺口 |

旧规范十分区来自 `docs/frontend/admin-full-ui-interaction-specification.md` 第 103 项（旧项目本机来源见台账）。

| 旧版分区 | 当前能力映射 | 尚未完整还原的范围 |
|---|---|---|
| 详情 | 商品基本信息、描述、分类、排序、库存、上架、审批和订购验证要求 | 旧版字段与商品类型细分尚未逐字段收口 |
| 定价 | 默认价格、初装费与多账期 `PricingPanel` | 旧版币种和成本列等未完整对齐 |
| 试用 | 当前商品编辑器未提供独立试用分区 | 试用策略、表单和流程待补 |
| 自动开通 | 基本信息有人工/模拟/NOKVM/供应商交付模式，NOKVM 配置；供应来源提供关联摘要 | 旧版全部模块参数与自动开通时机未完整映射 |
| 产品配置 | `ProductConfigurationPanel`、`ProductConfigurationRules`：配置项和规则 | 旧版全部高级配置、隐藏、优惠及弹窗语义需逐项验收 |
| 升级选项 | `/admin/products/{id}/upgrade-targets`、`/admin/products/{id}/configuration-upgrade-policy` 已注册；`product-upgrades.tsx`、`configuration-upgrades.tsx` 已有真实 API 组件 | 当前 `product-editor.tsx` 未导入这两个组件，没有对应编辑分区；不应表述为系统全无升级能力 |
| 自定义字段 | `PurchaseFieldsAdmin` 提供购买字段 | 旧版字段类型、显示位置、全部校验与使用场景未完整对齐 |
| 商品推介计划 | `/admin/affiliate/settings` 已有商品专属规则与阶梯，`/admin/affiliate/rules` 真实读写；见原推介验收 | 当前商品编辑分区未接该入口和完整旧版商品推介语义；不应表述为全局缺失 |
| 文件下载 | `web/src/components/downloads.tsx` 与 `/admin/downloads`、`/admin/download-products` 已有下载中心、商品访问权限和商品目录 | 当前商品编辑器缺少旧版文件分区、附件管理和快速上传入口的完整对应；不应表述为全局缺失 |
| 链接 | 商品已有 `slug` 等标识 | 当前商品编辑器未提供旧版独立链接分区；slug 不足以证明该分区完整还原 |

## 截图完整性

以本机 `/www/wwwroot/zjmfgo` 为截图根目录，只读检查 157 条记录：全部文件存在，149 条带原 SHA-256，其中 143 条与当前字节匹配，6 条失配；其余 8 条没有历史摘要，登记为 `missing_hash`（完整性未建立，非已判定损坏）。摘要匹配只证明当前字节等于登记摘要，不证明业务验收、视觉内容或历史生成过程。

| 历史条目（索引从 0 开始） | 路由 / 宽度 | 当前状态 |
|---|---|---|
| 6 | `/admin/logs/jobs` / 1440 | `mismatch`，需重验 |
| 12 | `/admin/jobs` / 1024 | `mismatch`，需重验 |
| 14 | `/admin/logs/jobs` / 1024 | `mismatch`，需重验 |
| 22 | `/admin/logs/jobs` / 390 | `mismatch`，需重验 |
| 28 | `/admin/jobs` / 320 | `mismatch`，需重验 |
| 30 | `/admin/logs/jobs` / 320 | `mismatch`，需重验 |
| 104–107 | `/admin/configurable-options` / 1440、1024、390、320 | `missing_hash`，完整性未建立 |
| 108–111 | `/admin/order-settings` / 1440、1024、390、320 | `missing_hash`，完整性未建立 |

多份历史验收文档（如 `admin-restore-interaction-acceptance.md`、`admin-batch-customer-ticket-acceptance.md`）共用 `output/playwright/admin-restore-20261011/`；六条失配路径未按验收批次隔离，但本索引内各仅出现一次。共享目录和失配不足以证明具体覆盖来源；本批不追认当前图片为原历史图片。对失配条目新增 `screenshotIntegrity`，保存原期望摘要、此次观察摘要、复核日期、原因和 `usableForIntegrityProof:false`；原 `screenshotSha256` 不替换。八条无摘要记录仅新增 `missing_hash` 声明，不补写历史摘要。顶层 `integrityReview` 保存此次复核快照，实时状态仍须运行校验器。

`result:PASS` 继续表示历史原检查范围，不变成当前完整性证明。生成契约保留原摘要、范围与逐条声明，浏览器列明确标为历史 PASS；有失配或未建立摘要的页面附加提示。生成器不读取截图，不把声明当成本次实时校验。

原生成产物落后台账与源码，本次重生成同时刷新静态 API 行号、候选迁移等来源引用。第 48 项台账已有多个路由，现有生成器不能解析为单一独立页面，因此保守标为未验证；这不表示销售设置/结算能力消失。本批未修改该路由解析规则，源码存在与业务完整性仍须分别判断。

只读复核命令（`--manifest` 相对当前目录，`--root` 仅用于解析截图路径）：

```sh
node scripts/check-admin-browser-evidence.mjs --root /www/wwwroot/zjmfgo --strict
node scripts/check-admin-browser-evidence.mjs --root /www/wwwroot/zjmfgo --strict-unacknowledged
node --test scripts/check-admin-browser-evidence.test.mjs
node scripts/admin-page-contracts.mjs
git diff --check
```

校验器只输出可读 JSON，不写清单、截图或结果文件。状态包含 `verified`、`mismatch`、`missing_hash`，另将缺文件、无效摘要和读取错误分列。普通模式报告全部状态；`--strict` 对任何非 verified 记录返回非零，因此本清单预期退出 1，`strictPassed:false, overallIncomplete:true`。`missing_hash` 仍表示未建立完整性，不是已判定字节损坏。

需要仅阻止新异常时，显式使用 `--strict-unacknowledged`：未声明失配、未声明缺摘要、后续字节漂移、缺文件、无效摘要或读取错误返回非零。已声明失配只有在原摘要、此次观察摘要、日期、原因和不可用标志都对应时才被确认已登记；仍计入 mismatch，仍不可用于完整性证明。缺摘要条目只有具备完整 `screenshotIntegrity`（`status:missing_hash`、`usableForIntegrityProof:false`、非空 `checkedAt/reason`）才计入 `declared_missing_hash`；否则计入 `unexpected_missing_hash` 和 `unacknowledged_failures`。两类缺摘要都保持 `strictPassed:false, overallIncomplete:true`，不补写历史摘要。该模式退出 0 只表示 `recordingConsistent:true`，`strictPassed` 仍为 false、`overallIncomplete` 仍为 true。两个 strict 模式不能同时指定。独立 worktree 无截图目录时直接使用上述主项目 `--root`，不必复制或修改产物；可用 `--manifest /绝对路径/清单.json` 指定清单。

验证：五个临时目录测试通过，覆盖分类、原输入与文件不变、已登记失配、后续覆盖、原期望摘要改动、无效声明、缺摘要声明完整性、CLI JSON/退出码；无需数据库。缺摘要新增回归确认未声明条目在 `--strict-unacknowledged` 退出 1，完整声明退出 0，且 `--strict` 均退出 1。主项目文件复核结果为 `verified=143, mismatch=6, missing_hash=8, missing_file=0, declared_mismatch=6, unexpected_mismatch=0, declared_missing_hash=8, unexpected_missing_hash=0, strict_failures=14, unacknowledged_failures=0`。严格完整性未通过（预期退出 1）；记录一致性通过（`--strict-unacknowledged` 退出 0）。原八条无摘要记录及全部截图未改动。生成器重新生成 105 个唯一旧 hash 的契约；`git diff --check` 通过。未进行浏览器重验；六条失配的有效替代证据需要未来重新验收并写入独立截图路径。
