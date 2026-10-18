# 审计输入边界

机器审计输入不属于活动知识库。当前工作树把它们放在被 `.gitignore` 忽略的 `output/audit-inputs/`，生成结果放在 `output/audit/`；这两个目录可由 CI 制品或受控外部存储提供。

项目级门禁声明源是已跟踪的 `audit/contract-gates.json`。它只登记稳定契约、实现路径、测试入口、评审要求和发布证据类型；`node scripts/check-contract-gates.mjs` 只校验 JSON 结构和路径存在性，不执行测试、不代表人工 Review，也不生成真实外部或生产证据。修改该清单时必须同步对应稳定契约和测试入口，并运行门禁脚本。

`audit/module-build-profile.json` 是另一份受控声明，列出 API/worker 默认构建必须包含的模块及契约版本。候选构建器用它核对二进制实际注册清单，输出证据放在候选产物的 `meta/`，不写回本目录。它不包含实例配置或凭据；规则见[编译模块契约](../docs/compiled-module-architecture.md)。

脚本默认读取：

- `GOIDC_LEGACY_INVENTORY`，默认 `output/audit-inputs/legacy-inventory.json`
- `GOIDC_ROUTE_REVIEWS`，默认 `output/audit-inputs/route-reviews.json`
- `GOIDC_BROWSER_EVIDENCE`，默认 `output/audit-inputs/admin-restore-browser-evidence.json`

输入可能包含外部绝对路径、浏览器摘要和大量机器字段。不要把它们复制进 `docs/`、提交凭据，或把历史 PASS 解读为当前状态。
