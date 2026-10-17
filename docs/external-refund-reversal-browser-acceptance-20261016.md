# 充值外部退款浏览器验收（2026-10-16）

本轮验收代码为 `d980903`，覆盖充值退款申请、余额冻结、独立确认扣减、驳回释放及原请求恢复。结果 **PASS**；没有新增 UI 修复。此结果是隔离 PostgreSQL 18 + 真实 Go HTTP API + Next.js 生产构建 + Chrome 浏览器验收，不代表真实商户原路退款验收。

## 环境与证据

- 独立分支 `acceptance/payment-exception-20261016`，运行端口 3096；没有重启生产 API、worker、Web。
- `TestExternalRefundReversalBrowserFixture` 新建隔离 schema，注册申请、复核、只读、无退款读取权限、仅本人客户范围及超级管理员账户。网关为本地不出款协议 fixture。
- Next.js `next build --webpack` PASS。独立 worktree 复用依赖链接，默认 Turbopack 因链接指向 root 之外拒绝构建；使用 webpack 完成生产构建，没有复制依赖。
- 构建时设置 `API_INTERNAL_URL`，并检查 `.next/routes-manifest.json` 确认转发到隔离 API。一次构建环境探测遗漏该变量，登录被默认后端的来源校验拒绝；未登录成功、未进行业务写操作，该探测不计入最终验收。
- 使用 Playwright 技能。CLI 以临时 `chromiumSandbox:false` 配置启动专用浏览器；有状态资金及故障流程由单进程 Node Playwright 脚本执行，没有增加 test spec。
- 1440、1024、390、320 宽度，视口高度 900。共 72 张真实截图，路径 `output/playwright/refund-reversal-verify-20261016/`；截图未纳入 Git 构建产物。受控证据清单见 [JSON 清单](generated/external-refund-reversal-browser-acceptance-20261016.json)，含截图 SHA-256、布局测量及去敏资金事实，不含账户密码、Cookie、明文幂等键或数据库连接串。

## 验收结果

| 场景 | 结果 | 真实行为与证据 |
| --- | --- | --- |
| 空记录、必填校验、取消登记 | PASS | 空列表正常；空表单被前端校验；取消填妥草稿后退款、余额、预留及账单均未变化。`create-form-{width}.png` |
| 保存申请 | PASS | 服务端创建唯一 `pending_review`，冲正预留为 `pending`；账户余额不扣减，预留总额仅增加 1234 分，没有冲正流水。`requester-pending-{width}.png` |
| 独立确认及取消确认草稿 | PASS | 取消无写入；其他员工确认后账单 `refunded`，余额与预留各减少 1234 分，只有一条负向冲正流水；原成功支付和原充值入账流水均保留。`confirm-dialog-{width}.png`、`confirmed-{width}.png` |
| 申请人自审限制 | PASS | 超级管理员本人申请也隐藏审核按钮；直接 Go API 返回 `403 external_refund_self_review`，资金事实不变。首段脚本曾误将期望码写为 409，修正为实际契约 403 后继续验收，没有重复已提交业务动作。 |
| 独立驳回 | PASS | 冲正预留变为 `released`；预留减少 1234 分，余额不变，负向冲正流水仍为 0。`reject-dialog-{width}.png`、`rejected-{width}.png` |
| 真实并发 409 | PASS | 弹窗仍保留原版本时，另一真实 API 登记先成功；当前提交 409，保存原键、版本、载荷，不自动重试；显式读取最新事实并确认后才结束被拒绝请求。`conflict-reviewed-{width}.png` |
| 登记成功后丢响应 | PASS | 通过 `route.fetch()` 使服务端先返回 201 并提交，再对浏览器中断响应；刷新不自动发请求，原请求可恢复；以原 body/key 重放只返回既有记录，没有重复冻结。`lost-response-restored-{width}.png` |
| 员工身份隔离 | PASS | 保留申请人的未知请求后切换只读员工，不显示或继承前一员工的恢复动作；切回原员工仍能原样恢复。`reader-isolated-{width}.png` |
| 确认成功后丢响应 | PASS | 服务端先确认并扣减，浏览器丢响应；刷新后原请求重放，余额与预留不再变化，负向冲正流水仍恰为 1。`review-lost-response-restored-{width}.png`、`review-replay-confirmed-{width}.png` |
| 写权限、读取权限、客户范围 | PASS | 只读员工无写按钮，直接写 API 403；无退款查看权限员工不渲染证据，读取 API 403；仅本人客户范围的员工读取范围外退款 API 404。`denied-no-evidence-{width}.png` |
| 既有审核状态 | PASS | 已确认、已驳回、待复核均读取真实记录；最终状态不再显示二次审核按钮。`reviewer-pending-{width}.png`、`existing-confirmed-{width}.png`、`existing-rejected-{width}.png` |
| 读取失败与恢复 | PASS | 仅对列表读取注入标记的 503；错误可见，撤销故障后“重新加载”恢复真实 API 待审核记录。`api-error-{width}.png` |
| 交易汇总 | PASS | 页面显示真实 API 退款金额；净网关收入等于原网关收入减外部退款。`summary-financial-verified-{width}.png` |
| 四尺寸与浏览器错误 | PASS | 全部截图时 document 宽度不超过视口；弹窗无横向内部溢出、未超出视口边界。人工查看 320 登记、390 确认、320 恢复截图，未见操作按钮或说明遮挡。可横向滚动的原资金表保留容器内滚动。JS 异常 0，非预期 API 失败 0；409/权限拒绝/显式注入的 503 单独列入证据。 |

## 资金事实样本（金额为分）

隔离客户拥有其他 fixture 预留，以下比较相同客户的前后总额，不能把总预留误读为单张退款金额。

| 动作 | 账面余额前 → 后 | 总预留前 → 后 | 本账单负向冲正流水 | 原成功收款 / 原充值流水 |
| --- | --- | --- | --- | --- |
| 独立驳回 | 103570 → 103570 | 2468 → 1234 | 0 | 1 / 1 |
| 另一张账单确认并丢响应 | 103570 → 102336 | 3702 → 2468 | 1 | 1 / 1 |
| 原确认请求重放 | 102336 → 102336 | 2468 → 2468 | 仍为 1 | 仍为 1 / 1 |

未触达真实商户、供应商、短信或 SMTP；没有部署或推送。本轮仅验证本地已发生外部退款事实的登记与独立复核，不把人工登记等同于网关执行退款。

收尾：fixture 创建 finish 信号后正常 PASS（1186.097s），ready/fixture 文件均移除；查询 PG namespace 确认专用 schema 已删除。3096 预览已停止且无监听，独立 Node 浏览器已关闭，CLI 专用会话确认已不再打开。截图与去敏清单保留，未提交构建产物或账户资料。
