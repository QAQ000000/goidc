# 推介商品与月营业额规则

`GET /api/v1/admin/affiliate/rules` 需要 `affiliates.view`，返回
`{version,product_rules:[{product_id,event_type,mode,rate_bps,amount_cents,max_cents}],tier_rules:[{lower_cents,upper_cents,rate_bps,amount_cents}]}`。
`GET /api/v1/admin/affiliate/rules/schema` 同权限，返回 `{products:[{id,name,slug}]}`，不要求商品管理权限。

`PUT /api/v1/admin/affiliate/rules` 需要 `affiliates.manage`，使用上述两个完整数组及 `expected_version`，不传 `version`；`Idempotency-Key` 必须为 8–128 字符。空数组清空此类当前规则。后台重新检查有效 session 和角色授权。相同请求同键返回原收据；同键不同内容、过期版本返回 409。未知字段返回 400。商品重复组合、无效商品/事件/金额/阶梯返回 409。

事件为 `first_purchase/reorder/renewal/upgrade`。模式为 `inherit/disabled/rate/fixed`；继承和禁用要求比例与固定金额为零、上限为 null。比例模式要求 `rate_bps` 为 1–10000、固定金额为零；固定模式要求正整数分金额、比例为零。上限可为 null 或正整数分。数据库沿用原有 `override`，API 将其转换为 rate/fixed，不修改历史模式。

阶梯下限闭合、上限开放，`upper_cents:null` 表示无限。允许不连续但不允许重叠；未命中继承默认规则。比例和固定金额不能同时为正，0/0 表示此档零佣金。金额与有限边界最大 100000000 分；商品规则最多 2000 条、阶梯最多 200 条。不得重复商品/事件。

保存和计划设置共享串行锁与版本号，新建规则集快照并复制默认规则；历史规则集、佣金和金额不回算。计佣仍使用账单创建时有效的规则集。优先级为商品覆盖/禁用、上海自然月营业额阶梯、默认事件规则。商品继承同样参与阶梯；阶梯只替换比例/固定金额，保留该事件默认规则的金额上限。

营业额按推广账户累计合格已结算账单商品净额，以实际到账时间所属上海自然月为周期。当前整张账单净额先纳入选档，每行应用同一档，商品覆盖仍优先；同一账单多行只累计一次，零佣金账单也计营业额。推广账户事务锁串行化不同客户的同时到账。记录专用唯一账单营业额表；退款/取消订单、已退款账单、未还清信用消费不计后续营业额。充值没有有效商品行，因此不计营业额。信用消费仅全部还清后计佣，到账时间取结清还款记录 `received_at`，不取信用占用时 `invoice.paid_at`。还款原有同事务计佣入口和幂等回放保持有效。

佣金快照保存月窗口、时区、累计营业额、当前账单净额及匹配档位上下界。金额计算沿用整数分、定点基点拆分防溢出、商品上限和不超过净额约束；营业额累计溢出会回滚付款事务，不产生错误金额。

迁移 134 只建立历史营业额基线，不创建或改变历史佣金：仅回填有实际到账时间、现存有效归因且归因不晚于账单创建、能找到账单创建时规则集的已付款商品/续费/升级账单；排除当前退款/取消与未结清信用。无法从现存数据证明历史归属或规则集的账单不推测回填。没有历史归因/规则/到账证据的数据需要人工核对；后续规则变更不会补算它们的佣金。

## 营业额计入与状态投影（2026-10-15 修复）

迁移 138：`affiliate_turnover_entries.rule_set_id` 改为可空，明确规则集是证据而非营业额计入的前置条件。已付款的销售/续费/升级/重购账单只要存在有效归因就无条件写入一次基数分录，即使账单创建时没有生效规则集、即使净额为零、即使最终佣金为零；这类账单只跳过佣金分录，`rule_set_id` 记为 NULL。同一账单仍只计入一次，并发由 `affiliate-turnover-account` 账户级串行锁与 `affiliate_turnover_entries.invoice_id` 主键共同保证；重复到达同一账单的付款/还款/回调入口不会重复计基数，也不会跳过既有佣金分录。无商品行的充值、流量包账单仍不计营业额。

`affiliate_commission_entries.status` 自迁移 100 起就是分桶列的派生投影（BEFORE UPDATE 触发器），桶列才是权威；迁移 139 增加 `affiliate_commission_status_derived` CHECK 约束并在建约束前重算存量不一致行，使释放/提现预留/打款/冲正/抵扣任一路径绕开触发器也无法把过期状态写回。客户接口继续返回该字段，其取值始终等于分桶派生结果，前端契约不变。

迁移 140 为 `invoice_items(invoice_id)`、`order_items(order_id)`、`services(product_id)` 补前导列索引，消除父行删除/更新及导入器逐账单查询的全表扫描。

### 历史数据回填

迁移 134 之上新增的这两类修复都不回填历史数据：
- 若某数据库在修复前已产生过“有归因但创建时无生效规则集”的已付款账单，它们仍缺基数分录。这是少付历史佣金的问题，需人工确认后再回填，本仓库不自动改写历史事实。只读诊断见下。
- `status` 不一致若存在，迁移 139 会就地重算（仅重写 `status` 自身，不改分桶金额、不改 `amount_cents`）。这是把过期副本对齐到权威桶列，不改变任何客户金额。

```sql
-- 只读：找出缺少营业额基数、但有有效归因的已付款账单。
SELECT i.id,i.invoice_no,i.user_id,i.amount_cents,i.created_at,i.paid_at
FROM invoices i
JOIN affiliate_attributions at ON at.user_id=i.user_id AND at.status='active'
JOIN affiliate_accounts a ON a.id=at.account_id
WHERE i.status='paid' AND a.user_id<>i.user_id
  AND NOT EXISTS(SELECT 1 FROM affiliate_turnover_entries t WHERE t.invoice_id=i.id)
ORDER BY i.id;
```

