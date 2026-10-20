# 迁移与切换

- [旧数据迁移](../legacy-migration.md)：导入、冻结、复核和回滚边界。
- [切换手册](../cutover-runbook.md)：维护、代理、切流和切回步骤。
- [发布准备](../release-readiness-next-contract.md)：当前接管前置条件和未关闭风险。
- [旧接口审计](../api-compatibility-audit.md)：审计输入、覆盖口径和协议兼容限制。
- [历史审计索引](../archive/README.md)：历史成员、证据和恢复说明。

迁移184退出信用及合同，关闭相关开关但不物理清表、不改旧财务SQL；旧查询、还款、回执恢复与合同下载均不再提供。先停旧writer，再成套发布迁移/API/worker/Web；发现真实债务/合同先人工核查，不用“无历史”的预期替代现场核验。详见[信用退役](../customer-credit-portal-contract.md)、[合同退役](../domain/contract-settings.md)。

迁移文档区分“静态源码证据”“隔离测试通过”和“可接管生产”。任何一项不能替代另外两项。
