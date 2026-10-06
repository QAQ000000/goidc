# 服务账期变更

Go 原生服务现在支持在月、季、年之间变更账期。旧 PHP 和旧数据仅作业务参考。

客户在服务详情选择目标账期后，API 会锁定服务、商品 revision 和当前服务版本，按目标账期价格及已有配置重新计算周期价，并按剩余当前账期折算差额。正差额生成变更账单，负差额在交付成功后返还余额；报价十分钟有效。

接口：`POST /api/v1/services/{id}/billing-cycle-quotes`，提交 `target_billing_cycle`、`expected_version`、`expected_revision`；随后复用 `POST /api/v1/services/{id}/configuration-changes` 提交报价。付款和人工/模拟交付沿用原配置变更队列，交付成功才更新服务 `billing_cycle`、周期价和版本。服务原订单及购买快照保持不变。

已有任务、取消申请、未付款续费、过期服务、一次性服务、重复账期或版本变化都会拒绝报价。账期变更与配置/商品变更共享待处理唯一约束、库存/余额锁、幂等和审计事务。

schema062的百分比循环续费权益在账期交付后保留，下一次续费按新的周期原价减免。账期变更差价仍按原价计算，详见 [循环续费优惠](recurring-promotions.md)。

验证覆盖隔离 PG18 报价、负差额/正差额计算、付款、队列交付、合同切换、配置/商品升级回归、全量 Go 回归、前端 typecheck/lint 和 webpack 构建。部署批次证据位于 `output/billing-cycle-20261006/`，schema056 备份位于 `bin/previous/` 本批时间戳目录。
