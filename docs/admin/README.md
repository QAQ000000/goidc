# 后台与权限

- [后台 API 需求取舍](api-requirements.md)：全域功能组、旧版来源、复用/优化/新增与明确排除范围。
- [后台 API 契约](api.md)：接口目录生成、前端共同协议、客户摘要/交易/日志及续费的字段、权限和失败状态。
- [后台 105 项台账](../admin-full-ui-restore-ledger.md)：旧恢复批次的范围和证据参考；不能替代当前实际页面和逐动作接口盘点。
- [页面契约索引](../admin-page-contracts.md)：短入口；详细 Markdown 和机器 JSON 由生成器写入 `output/audit/`。
- [配置与站点设置](../admin-configuration.md)：后台配置边界。
- [RBAC](../admin-rbac.md)：角色、权限、范围和撤权规则。
- [定时任务日志与目录](../architecture/scheduler.md)：后台筛选、执行频率、审计语义及权限。
- [后台前端设计](../admin-frontend-design.md)：页面交互和响应式约束。
- 报表：[当前统计与 JSON 查询口径](../credit-cash-reporting.md)、[旧版完整恢复契约](../report-restoration-contract.md)。API/页面已补，独立PG18竞态回归与Next/Chrome模拟API浏览器分别通过；真实Go API浏览器全链路、旧全量迁移/对账和外部凭证未验收，台账继续部分完成。
- [合同设置](../domain/contract-settings.md)：合同设置及模板管理的当前规则和验收边界。

旧批次计划、浏览器验收和截图摘要放在 [归档快照](../archive/snapshots/)，不覆盖当前台账。
