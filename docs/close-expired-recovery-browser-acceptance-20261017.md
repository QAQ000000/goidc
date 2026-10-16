# 过期空结果 Lookup 精确恢复专项验收（2026-10-17）

结论：**PASS**。基线为 `e49d5d7` 加精确恢复修复 `f00b86c`（隔离分支等价提交 `bfe47d5`）。本次新增真实 Go API + Chrome 专项证据，不把旧 `7d0440a` 的 18 项 / 67 张截图当作过期恢复证明。

隔离 PG18 `_test` 数据库与一次性 schema；独立 worktree 生产 Next.js 构建（`npm run build -- --webpack`），共享 `node_modules` 软链接，使用独立 `.next`；Playwright CLI Chrome。没有真实商户调用、资金收付、业务数据库访问或产品测试后门。测试完成后 fixture 正常退出并清理自己的 schema。

## 真实 API 与浏览器检查

| 检查 | 结果 | 证据 |
| --- | --- | --- |
| 最新同关单其他员工核验不能掩盖原员工原键记录 | PASS | 原 expired 空结果记录最旧，105 条较新记录均由其他员工通过真实 Lookup API 创建 |
| 跨 100 条分页仍找到原记录 | PASS | 恢复实际 GET `limit=100&offset=0` 与 `offset=100`；原记录位于第 106 条 |
| 错误员工、错误 key 不能结束原请求 | PASS | 显式读取真实事实后无结束按钮；另一员工 GET 不含原员工请求摘要 |
| 错误原版本不能结束 | PASS | 原键配错误版本真实 POST 返回 409；原版本载荷保留，读取后仍无结束按钮 |
| 原员工原键重放过期申请 | PASS | 真实 409 `close_verification_expired`；未显式读取前无结束按钮 |
| 完整原记录关联符合后才允许结束 | PASS | 本人 SHA256、账单、支付、关单、本地金额、原版本、远端操作号、expired、空结果及过期租约全部匹配；读取后出现结束按钮 |
| 结束只读请求后使用新 key 查询 | PASS | 新 Lookup 实际保存 `unknown`；原关单仍 unknown，支付 pending、账单 unpaid、版本 1，冻结仍有效 |
| 四尺寸布局 | PASS | 1440×900、1024×900、390×844、320×568；拒绝态与读到原记录态共 8 张截图，文档无横向溢出；页面 JS 异常 0 |

截图与 SHA256、恢复 GET 轨迹、真实 API 元组和数据库最终不变量见[去敏索引](generated/close-expired-recovery-browser-acceptance-20261017.json)。图片保存在主仓 `output/playwright/close-expired-recovery-20261017/`。

## 验证边界

错误金额记录被真实数据库关联 trigger 拒绝构造。金额、账单、支付、操作号、状态、租约、结果不匹配时不可结束，由实际组件 `readFacts` 回调测试覆盖（`node --test scripts/payment-close-recovery.test.mjs`，14/14 通过）；这些不计为浏览器伪造 API 场景。

过期申请仅由 `_test.go` 夹具在 INSERT 时设置历史创建时间及原截止时间；不改写不可变历史。105 条较新核验通过实际 API 创建。浏览器恢复资料使用该原申请的 key/body 构造，没有伪造服务端成功事实。CLI 第一次完成新 key Lookup 后未导出报告，随后只重复读取/恢复检查导出索引，没有重复新 key POST；自动化重复 dialog 接受属于夹具问题，单列于索引，不记为产品页面异常。

最终数据库断言：107 条核验申请（1 原 expired + 105 其他员工 + 1 新 key），原 expired 结果 0、决策 0，原 Close `unknown` 且 `unresolved=true`，支付 `pending`、账单 `unpaid`、版本 1、账单余额流水 0。Go fixture `TestPaymentCloseExpiredRecoveryBrowserFixture` 正常 PASS。
