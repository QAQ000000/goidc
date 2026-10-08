# 推广返利基础与支付入账

本批完成单级推广基础、注册归因和付款后的冻结佣金。提现、冻结解锁、退款冲正、阶梯结算和界面留待后续批次；不向客户站内余额发放佣金。

## API

| 方法 | 路径 | 认证或权限 |
| --- | --- | --- |
| GET | `/api/v1/affiliate/click/{code}` | 匿名，限流；返回记录结果与站内落地路径 |
| GET | `/api/v1/affiliate/overview` | 当前客户账户汇总 |
| POST | `/api/v1/affiliate/apply` | 当前客户，Idempotency-Key |
| GET/POST | `/api/v1/affiliate/links` | 当前客户；创建要求审核通过与 Idempotency-Key |
| PATCH | `/api/v1/affiliate/links/{id}` | 链接属主，Idempotency-Key |
| GET | `/api/v1/admin/affiliate/accounts` | affiliates.view |
| GET | `/api/v1/admin/affiliate/attributions` | affiliates.view |
| POST | `/api/v1/admin/affiliate/accounts/{id}/status` | affiliates.manage，Idempotency-Key |

链接创建接收 `landing_path`、可选 `product_id`、`note`；更新接收 `landing_path`、`note`、`status`。管理员账户状态接收 `approved/suspended/rejected/closed` 和 `expected_version`，版本不匹配返回冲突。同一幂等键携带不同参数返回冲突。

## 归因与计佣

点击保存随机会话令牌的 SHA-256 哈希，不保存原始 IP；来源地址去除用户信息、查询串和片段。链接、会话与日期共同防重。无效或停用链接返回 `/`，不写推广 cookie。浏览器复用 HttpOnly 会话 cookie，注册事务按数据库中最早有效点击选择推广人，完成邮箱验证码后才写正式归因。未验证注册不产生正式归因。自推、已归因客户和已转换会话不会创建新关系。

余额付款、易支付回调和主动查单共用现有付款事务，创建 `pending` 佣金与 `created` 事件。默认计划没有预设返佣规则，未配置时不产生佣金。实际账单净额按订单项目毛额比例分摊，最后一项吸收分级舍入差异；比例使用整数万分比，乘法分段计算以避免 bigint 溢出。续费和升级使用对应账单项目净额。首次购买与重购按已有成功付款历史判断。

规则按账单创建时间选择生效版本；条目保存规则版本、冻结期、整数计算输入、账单与项目快照。订单项目快照排除敏感购买字段。已入账账单重放不会重新计算，也不修改原条目。推广人余额不变，付款人的正常余额付款仅按账单金额扣减。

## 验证

`internal/httpapi/affiliate_test.go` 覆盖首次点击、验证注册、自推、重复归因、失效链接、权限、并发归因、三条支付结算链路、优惠净额、固定产品规则和付款事务回滚。`internal/store/affiliate_test.go` 覆盖 bigint 边界、整数规则、安全落地路径与来源脱敏。数据库验证必须设置 `REQUIRE_INTEGRATION=1` 和指向独立 `_test` 数据库的 `ZJMFGO_TEST_DATABASE_URL`；无数据库参数的跳过结果不算验收。

真实易支付账户收款、正式站点推广 URL 与页面浏览器链路尚未在本批验证。
