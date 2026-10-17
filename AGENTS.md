# Agent 文档边界

- 开始任务先读 [`docs/INDEX.md`](docs/INDEX.md)，再按领域入口下钻。
- 当前行为以代码、数据库迁移和测试为准；归档快照只提供历史背景。
- 不把 `output/`、截图、日志、JSON 审计输入或外部绝对路径当作活动知识文档。
- 新规则更新稳定命名的领域文档；过程记录和机器工件放 `output/` 或外部制品存储。
- 跨模块变更遵循[工程治理契约](docs/architecture/engineering-governance.md)，用[领域验收矩阵](docs/architecture/contract-verification-matrix.md)把契约、测试和证据串起来；未执行的数据库、浏览器或外部链路不得标记为通过。
- 修改文档后运行 `node scripts/check-doc-governance.mjs` 和 `node scripts/check-doc-links.mjs`。
