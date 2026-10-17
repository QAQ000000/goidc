# 后台页面契约入口

完整 105 页台账由 `node scripts/admin-page-contracts.mjs` 根据当前代码、后台台账、旧版规范和可选浏览器证据生成：

```bash
node scripts/admin-page-contracts.mjs
```

生成结果写入被忽略的 `output/audit/admin-page-contracts.json` 和 `output/audit/admin-page-contracts.md`。机器输入默认来自 `output/audit-inputs/admin-restore-browser-evidence.json`，也可通过 `GOIDC_BROWSER_EVIDENCE` 或命令行参数指定。

当前状态、权限和实现差异以 [后台 105 项台账](admin-full-ui-restore-ledger.md) 为准。源码存在不等于功能完成；生成器只做静态登记，不执行编译、浏览器或真实外部渠道验收。浏览器摘要失配、缺少截图摘要或未运行校验器时，结果只能作为历史范围记录。

旧版长索引保留在 [历史快照](archive/snapshots/admin-page-contracts.md)，不作为当前规则入口。
