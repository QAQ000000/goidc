# 文档入口

本页是代码 Agent 和开发者的唯一活动文档入口。先读本页，再按领域进入稳定命名的契约；`docs/archive/` 只保存历史快照和恢复索引，不作为当前规则来源。

## 事实优先级

1. 当前代码、数据库迁移和测试决定已实现行为。
2. 本页及下列稳定命名的领域文档描述当前约束和已知差距。
3. 带日期的验收、计划和运行记录只说明当时范围，不能覆盖当前代码或当前差距审计。
4. `output/` 是本地或 CI 生成物；其中的 JSON、截图、日志和归档包不是知识库文档，也不提交凭据。

## 按领域进入

| 领域 | 入口 | 主要内容 |
| --- | --- | --- |
| 架构与验证 | [architecture](architecture/README.md) | 生命周期治理、契约到测试、项目状态、容量和发布验证 |
| 业务契约 | [domain](domain/README.md) | 商品、交易、支付、服务、供应商、NOKVM、合同 |
| 后台与权限 | [admin](admin/README.md) | 页面台账、配置、RBAC、合同设置和后台 API |
| 迁移与切换 | [migration](migration/README.md) | 旧数据、切换、回退和历史审计 |

## 审计输入与输出

审计输入的存储和运行边界见 [audit/README.md](../audit/README.md)。

项目级门禁声明源是 [audit/contract-gates.json](../audit/contract-gates.json)，只描述契约与验证入口，不包含运行证据。

机器可读的审计输入放在被忽略的 `output/audit-inputs/`：

- `legacy-inventory.json`：由 `node scripts/inventory.mjs` 从外部旧源码生成。
- `route-reviews.json`：人工审查清单，供 `node scripts/route-coverage.mjs` 校验。
- `admin-restore-browser-evidence.json`：浏览器证据摘要，供只读校验器复核。

用 `GOIDC_LEGACY_INVENTORY`、`GOIDC_ROUTE_REVIEWS`、`GOIDC_BROWSER_EVIDENCE` 指向受控输入；生成结果写入 `output/audit/`。这些文件可能很大、带环境路径或绑定外部证据，不能复制回 `docs/`。

## 维护规则

- 活动文档使用稳定名称；新增 `-YYYYMMDD.md` 需要先说明它为何不能更新现有契约。
- 设计、当前规则和验收结论写在不同章节；过程日志、原始 JSON、截图、压缩包放 `output/` 或外部制品存储。
- 文档只引用当前入口。历史链接集中在 [封存索引](archive/README.md)，不要在新文档中复制历史表格。
- 运行 `node scripts/check-doc-governance.mjs` 检查入口、体积、日期文件和工件污染；运行 `node scripts/check-doc-links.mjs` 检查相对链接。
