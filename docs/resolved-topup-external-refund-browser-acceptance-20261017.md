# 补账后充值外部退款浏览器专项验收

日期：2026-10-17。结果 **PASS**。产品源提交 `aa397f2`（主线集成 `d462e49`），本验收 worktree 产品提交 `29c5025`；夹具为 `6baf13d` 的 `TestResolvedTopupRefundBrowserFixture`。没有修改产品或新增夹具，没有触达真实支付、退款、供应商或生产环境。

## 环境与证据

使用 Playwright 技能、独立 Chrome、真实 Go TCP API、专用 PostgreSQL 18 `_test` 数据库及独立随机 schema，实际应用迁移153。六组数据由真实签名通知与测试内入账故障产生；五笔再经真实补账 API 入账，一笔保留未补账。没有直接修改客户余额或伪造成功 API 响应。

Next.js 在独立 worktree 构建，依赖链接主仓 `node_modules`，未覆盖主仓 `.next`。`API_INTERNAL_URL=<隔离 API> npm run build -- --webpack` 通过，并实际检查构建的 rewrite 指向隔离 API。预览端口3111。

四尺寸1440、1024、390、320，高度900；另含320×500登记及驳回弹窗，登记底部操作可滚动到达。30张截图保留在主仓忽略目录 `output/playwright/resolved-topup-refund-20261017/`；[去敏索引](generated/resolved-topup-external-refund-browser-acceptance-20261017.json)记录摘要、布局、15项检查和只读数据库佐证，不含密码、Cookie、明文幂等键或连接串。截图不入 Git。

## 专项结果

| 检查 | 结果与证据 |
| --- | --- |
| 新增可见入口及取消 | PASS：已补账且有真实正向流水的充值显示可用登记入口；四尺寸展示一致。填写后取消没有退款、冻结、流水或审计写入。 |
| 登记冻结 | PASS：账面余额103570分保持不变，预留0→1234分；仅一条待复核登记，未生成负向流水。 |
| 独立复核冲正 | PASS：员工4登记、员工5实际确认；余额103570→102336分、预留1234→0，账单变为`refunded`，恰有一条负向流水。原支付仍`succeeded`，原正向流水仍为一条，原`paid_at`保持。 |
| 原失败与补账事实 | PASS：原结算仍`pending_liability / balance_posting_failed / ledger_id=null`；确认前后原结算和补账记录的完整行SHA-256相同。补账实际正向流水与原付款关联保留。 |
| 驳回释放 | PASS：独立驳回后预留1234→0，余额102336分不变，负向流水为0，账单保持`paid`。 |
| 登记已提交丢回执 | PASS：`route.fetch()`先取得真实201并提交，再仅向浏览器返回标记503。刷新不自动提交；切换只读员工不继承原请求，切回本人用相同body/key重放，无第二条登记或重复冻结。四尺寸恢复展示通过。 |
| 确认已提交丢回执 | PASS：先真实确认并扣减1234分，再遮蔽回执。刷新后四尺寸均展示原确认请求；同body/key重放，余额、预留、审计及负向流水保持不变，负向流水仍恰为一条。 |
| 版本及并发409 | PASS：过期`expected_version`真实返回`409 version_conflict`且无资金/审计变化；另一合法登记先提交后，浏览器原请求真实409并保留原键/版本/载荷，显式读事实后才结束被拒绝请求。 |
| 权限与客户范围 | PASS：只读员工没有登记按钮，直接写API返回403；仅本人客户范围员工读取范围外退款返回404。申请员工无复核权限，页面不提供复核且直接POST403。本专项未另做超级管理员自审场景。 |
| 未补账保护 | PASS：页面登记禁用；直接API返回`409 external_refund_topup_not_credited`，退款、预留、负向流水与退款审计均为0。 |
| 可用余额不足 | PASS：真实API新建人工账单，客户实际余额付款消费99368分，账面余额2968分、其他退款预留2468分，可用500分。新退款登记真实409，前后余额、预留、退款行、负向流水及退款审计完全一致。未直接改余额。 |
| 客户安全投影 | PASS：客户详情显示已退款并保留补账历史；`resolution`仅含`amount_cents / ledger_id / created_at`，无申请键、请求哈希、员工字段或内部说明。 |
| 响应式与错误 | PASS：30张截图的文档宽度均不超过视口，弹窗边界与内容未横向溢出；人工查看320确认、短屏底部及390恢复截图，操作可达。JavaScript异常0，非预期API错误0。 |

最初短屏测量在尺寸动画尚未稳定时失败，当时尚无退款写入；最终采用稳定后测量，并单独留存滚动到底部的截图，不计失败探测为通过。背景列表请求在导航时存在`context canceled`日志，不涉及资金写入；两次503均为明确标记的已提交回执遮蔽。

最终只读汇总：6条原失败结算、5条真实补账事实均保留；退款为2条confirmed、1条rejected、1条pending_review；预留为2条applied、1条released、1条pending；负向退款流水恰为2条。它们是隔离测试事实，不证明真实商户或付款人到账。

## 复查与清理

```bash
node scripts/check-admin-browser-evidence.mjs \
  --manifest docs/generated/resolved-topup-external-refund-browser-acceptance-20261017.json \
  --root /www/wwwroot/zjmfgo --strict
```

fixture正常退出PASS（698.270秒），精确查询本次schema残留0；ready、manifest和finish文件自动移除，专用测试数据库已删除。3111预览与隔离API无监听，两个独立浏览器均已关闭。仅保留本地截图和去敏索引；未推送或部署。本批没有重复既有全量Go回归，后端PG18/race等证据见[产品专项验收](resolved-topup-external-refund-acceptance-20261017.md)。
