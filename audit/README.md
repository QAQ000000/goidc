# 审计输入边界

机器审计输入不属于活动知识库。当前工作树把它们放在被 `.gitignore` 忽略的 `output/audit-inputs/`，生成结果放在 `output/audit/`；这两个目录可由 CI 制品或受控外部存储提供。

项目级门禁声明源是已跟踪的 `audit/contract-gates.json`。它只登记稳定契约、实现路径、测试入口、评审要求和发布证据类型；`node scripts/check-contract-gates.mjs` 只校验 JSON 结构和路径存在性，不执行测试、不代表人工 Review，也不生成真实外部或生产证据。修改该清单时必须同步对应稳定契约和测试入口，并运行门禁脚本。

`audit/module-build-profile.json` 是另一份受控声明，列出 API/worker 默认构建必须包含的模块及契约版本。候选构建器用它核对二进制实际注册清单，输出证据放在候选产物的 `meta/`，不写回本目录。它不包含实例配置或凭据；规则见[编译模块契约](../docs/compiled-module-architecture.md)。

`audit/admin-api-requirements.json` 声明当前后台所需功能组、页面、旧版来源和处置决定，独立记录 API、UI 和验证状态。它是需求声明，不是全部接口已验收的证据。`scripts/admin-api-inventory.mjs` 从当前 Go AST 与显式指定的旧源码采集路由；`scripts/admin-api-consumers.mjs` 从 Next 页面递归收集文件级请求候选。完整目录及机器结果写入 `output/audit/`，按[后台 API 需求](../docs/admin/api-requirements.md)解读，不能用动态候选或路由数量计算业务完成率。

脚本默认读取：

- `GOIDC_LEGACY_INVENTORY`，默认 `output/audit-inputs/legacy-inventory.json`
- `GOIDC_ROUTE_REVIEWS`，默认 `output/audit-inputs/route-reviews.json`
- `GOIDC_BROWSER_EVIDENCE`，默认 `output/audit-inputs/admin-restore-browser-evidence.json`

输入可能包含外部绝对路径、浏览器摘要和大量机器字段。不要把它们复制进 `docs/`、提交凭据，或把历史 PASS 解读为当前状态。
