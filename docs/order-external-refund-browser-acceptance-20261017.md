# 人工未交付订单外部退款浏览器验收

日期：2026-10-17。结果 **PASS**，范围为首批 manual、全部未交付且没有资源任务的整单外部退款登记、独立复核及订单/服务投影。真实商户退付、付款人到账和资源销毁不在本次证明范围。

## 版本与环境

核心流程在新建隔离 fixture 上完整重跑，前端源 `f95aeb4`（主线 `beec800`），最终迁移154源 `114932f`（本 worktree `69c11fc`，主线 `0d12e4a`）。后来只追加两项布局差异：备注操作行 `flex-wrap`（`c6fdba0`），以及退款弹窗标题为移动端44px关闭点击区保留空间；最终提交和构建 ID 见[去敏索引](generated/order-external-refund-browser-acceptance-20261017.json)。核心资金流程的通过不冒称在纯布局提交上重复写入；最后布局截图和几何检查在重建后的版本重测。

使用 Linux Chrome、Playwright、真实 Go TCP API、独立 PostgreSQL 测试数据库中的随机 schema `zjmfgo_test_88f9fa5418645da5`。八组订单通过真实 cart 或 admin quote/submit 及本地签名付款通知建立；每组有三个 manual 服务。外部网关隔离拒绝出站动作。独立 worktree 构建和预览端口3094、隔离 API40639；未在主仓构建、重启或更改常驻8088/3088。

四种宽度及短屏：1440×900、1024×768、390×844、320×740、320×568。截图保留在独立 worktree 忽略目录 `output/playwright/order-external-refund-20261017/final/`，索引记录每张截图 SHA-256、源版本、实际测量和去敏数据库快照；不收录密码、Cookie、连接串或明文幂等键。旧 fixture 的 `pre-UI-fix` / `pre-ux-f95aeb4` 不计入最终数量。

## 结果

| 检查 | 结果与证据 |
| --- | --- |
| 登记与独立确认 | PASS：员工3通过浏览器登记订单1，员工4独立确认。订单/账单 refunded，三个服务 cancelled 且 inventory_released；原 pending 核验历史保留并标为退款终态优先。 |
| 驳回释放 | PASS：订单2独立驳回，hold released；原 paid 订单和三个未交付服务保持，审核及备注恢复原资格。 |
| 已提交丢回执 | PASS：订单3真实服务端201提交后，只有浏览器响应被 abort；刷新及显式读取最新事实不改原body/key，按原请求核实返回200 replayed，同一退款ID、没有重复冻结。索引只保留body/key摘要。 |
| 订单版本409 | PASS：独立超级管理员实际修改订单4备注使版本3→4；旧草稿真实409并保留原版本、完整服务版本、body/key。显式读取事实仍不替换原请求，仅明确结束被拒绝请求后归档。 |
| 权限与范围 | PASS：只读员工无写控件、直接写403；无 order_reverse 的员工登记/复核禁用且403；owned客户范围外读取404；申请人自审403 external_refund_self_review。已核验订单8登记禁用。 |
| 提交前身份改变 | PASS：打开草稿后切换真实会话员工，确认前检测身份/权限变化，不发送退款POST，草稿保留。 |
| 冻结与终态投影 | PASS：pending/applied 的审核、删除、备注禁用；选中pending订单时批量核验/取消/删除禁用。服务详情无确认交付入口；released恢复原资格。五尺寸订单/服务详情及列表没有页面横向溢出。 |
| 弹窗与短屏 | PASS：五尺寸弹窗保持视口内，无内容横向溢出；320×568底部提交可滚动到达。datetime-local step=1，实际到账时间不自动填默认值。两标题与关闭点击区的文字矩形不交叠。 |
| 320备注布局 | PASS：实看最初长禁用说明被两按钮挤成长列，随后只给操作行添加flex-wrap；重建后完整重测五尺寸投影，320说明可正常横排换行。 |
| 控制台 | PASS：完整核心流程结束读取浏览器console，0 errors / 0 warnings。 |
| 真实商户、无限库存 | N/A：本浏览器fixture使用两个有限库存产品，未验收真实商户退款或无限库存；后端专项结论由主代理另列。 |

最终只读前后核对：客户余额始终12345分，所有原支付完整投影及 invoice.paid_at 不变。只有新确认订单1释放三单位，两个有限库存商品分别增加2、1；已有 seeded confirmed 的三单位单独留在基线，不重复计入本次变化。provision_jobs 3→3（原已核验负例的任务），退款及 hold 各6条。订单3保持 pending；订单4版本冲突未新增退款。原核验 pending 历史不被改写。

前端定向 ESLint、typecheck 和独立 webpack build 均通过。五尺寸布局测量先等待过渡稳定300ms，短屏底部另留实际滚动截图；最初标题pr-6对44px关闭点击区仍有4px进入，修为pr-12后重新测量。没有把失败探测计为通过。

## 证据复查与生命周期

```bash
node scripts/check-admin-browser-evidence.mjs \
  --manifest docs/generated/order-external-refund-browser-acceptance-20261017.json \
  --root /tmp/zjmfgo-order-external-refund-ui-20261017 --strict
```

主代理已独立 strict 复核68/68截图，并用只读SQL重查余额、付款、账单、订单、服务、hold、退款、商品、释放单位、任务10个投影，均与索引 facts.final_db 完全一致。

2026-10-17 04:45（UTC+08:00）清理完成：创建本次唯一finish文件后，fixture正常退出PASS（2403.288秒）；精确SQL查询schema残留0、对应schema会话0，ready/fixture manifest/finish均自动移除。专用40639 API与3094预览无监听，PID1795833/1823469已退出；仅关闭orderrefundfinal20261017浏览器会话，CLI列表为空。/proc文件描述符检查中，独立worktree的web/.next与最终证据目录无打开文件。68张截图和去敏证据完整保留，清理后strict摘要仍68/68通过；未清空worktree/output。常驻API8088 PID608393、Web3088 PID1647696维持原监听，未操作worker。
