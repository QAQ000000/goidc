# 后台页面契约入口

当前后台需求取舍从[全域 API 需求](admin/api-requirements.md)进入；精确协议见[后台 API 契约](admin/api.md)。受控需求声明、真实注册路由和实际页面消费候选分别维护，不以旧页面条目数衡量 API 完成度。

先生成当前路由和页面消费候选：

```bash
node scripts/admin-api-inventory.mjs --legacy-root "$GOIDC_LEGACY_ROOT"
node scripts/admin-api-consumers.mjs
```

运行前把 `GOIDC_LEGACY_ROOT` 设为实际可读的旧源码目录；inventory 要求显式参数，不读取该变量或猜测默认路径。输出为被忽略的 `output/audit/admin-api-inventory.{json,md}` 与 `output/audit/admin-api-consumers.{json,md}`。消费者扫描仅需当前仓库；依赖为文件级候选关联，未知路径/方法显式保留，不证明页面实际调用、字段完整、运行权限或外部链路。

旧 105 条台账的兼容索引仍可由 `node scripts/admin-page-contracts.mjs` 根据台账、旧版规范和可选浏览器证据生成：

```bash
node scripts/admin-page-contracts.mjs
```

生成结果写入被忽略的 `output/audit/admin-page-contracts.json` 和 `output/audit/admin-page-contracts.md`。机器输入默认来自 `output/audit-inputs/admin-restore-browser-evidence.json`，也可通过 `GOIDC_BROWSER_EVIDENCE` 或命令行参数指定。

[后台 105 项台账](admin-full-ui-restore-ledger.md) 用于追溯旧版页面范围及当时验收，不是当前全量 API 清单。其共享路由、query 子视图和实际 Next 动态页不一一对应。源码存在不等于功能完成；生成器只做静态登记，不执行编译、浏览器或真实外部渠道验收。浏览器摘要失配、缺少截图摘要或未运行校验器时，结果只能作为历史范围记录。

旧版长索引保留在 [历史快照](archive/snapshots/admin-page-contracts.md)，不作为当前规则入口。
