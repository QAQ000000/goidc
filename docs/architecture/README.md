# 架构与验证

- [工程治理契约](engineering-governance.md)：项目生命周期、Agent 上下文、证据等级和发布安全边界。
- [领域验收矩阵](contract-verification-matrix.md)：契约到测试、审计入口和证据边界。
- [项目状态与开发入口](../reconstruction.md)：当前实现、边界和入口。
- [编译期模块架构](../compiled-module-architecture.md)：适配器和模块注册约束。
- [支付模块开发契约](payment-module-development.md)：方式注册、回调协议、历史快照、查询与资金安全验收。
- [供应商模块开发契约](supplier-module-development.md)：不可变账户身份、配置 schema、命名凭据、持久交付与兼容验收。
- [应用文件存储契约](file-storage.md)：数据库旧文件、本地/S3 固定位置、授权读取、对象 journal 与延迟回收。
- [内部调度任务契约](scheduler.md)：共享任务目录、原执行循环、取消、审计及日全量续费边界。
- [机器人渠道开发指南](bot-channel-development.md)：新增平台的实现步骤、接口骨架、身份授权、迁移接线和验收清单。
- [测试说明](../testing.md)：Go、前端、数据库、浏览器和外部依赖的验证边界。
- [容量验证](../capacity.md)：只读负载、worker 和资源预算。
- [重构准备度](../rewrite-readiness.md)：旧系统静态基线与替换限制。
- [构建产物保留](../build-artifact-retention.md)：运行产物和清理规则。

发布契约和运行候选入口集中在 [发布与切换](../migration/README.md)；历史批次证据位于归档快照，不是通用架构规则。
