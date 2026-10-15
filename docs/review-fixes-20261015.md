# 2026-10-15 审查修复批次

本批次从冻结基线 `freeze-20261015`（`eb0dade`）单独建分支，只做正确性修复，不新增功能。
每个问题一条分支，独立验证后在集成分支合并，再对合并结果执行完整回归。

## 问题、影响范围与验证

| 分支 | 问题 | 影响 | 验证 |
| --- | --- | --- | --- |
| `fix/auth-transport-20261015` | `COOKIE_SECURE` 只认字面 `true`，其余取值静默失效；反向代理后限流、黑名单和审计只看到 `127.0.0.1`；管理员失败登录写入**用户名维度**黑名单，任意来源 3 次即可全局锁定该账号 180 分钟 | 会话 Cookie 缺少 Secure；登录限流退化为全局单桶；管理员账号可被低成本拒绝服务 | 新增 `clientip_test.go`、`auth_transport_test.go` 等 12 项；`./internal/config/...`、`./internal/httpapi/...` 全量通过 |
| `fix/credential-rotation-20261015` | 客户自助改密与 MFA 确认不撤销 API key；`TopUp` 从不读取 `finance_settings` | 泄露的长期 API key 在轮换密码后仍然有效并绕过 MFA；后台充值开关与上下限静默失效 | 新增 4 个测试文件；`./internal/store/...`、`./internal/httpapi/...` 全量通过 |
| `fix/supplier-lease-20261015` | 只读 `sync` 任务也写账户租约与 `external_started_at`，崩溃恢复不清租约，账户开通队列被锁死；逾期暂停对不支持该动作的交付模式入队注定失败的任务且无告警 | 一次进程崩溃即可阻塞整个供应商账户的交付；欠费逾期不暂停且无人知道 | 新增 `provision/actions.go` 与 4 个测试；`./internal/store/...`、`./internal/supplier/...`、`./internal/provision/...`、`./internal/httpapi/...` 全量通过 |
| `fix/affiliate-turnover-20261015` | 结算时找不到生效规则集就跳过节转基数记录；`affiliate_commission_entries.status` 缺少数据库级不变式；`invoice_items`、`order_items`、`services` 热外键列缺索引 | 历史账单不计入营业额基数，后续阶梯佣金少付；接口返回的佣金状态缺少约束兜底；父行操作与导入查询放大为全表扫描 | 迁移 138/139/140 幂等且可重放；新增 2 个测试文件；`./internal/store/...`、`./cmd/legacy-import/...`、`./internal/httpapi/...` 全量通过 |
| `fix/web-hardening-20261015` | 缺少 `not-found`/`loading`/`global-error`；`date()` 对可空时间戳抛错导致整页崩溃；401 不统一跳转；订单状态缺中文映射；客户启停失败被静默吞掉；多个后台页面跳转目标不一致且缺服务端权限门；残留死代码 | 非法 ID 落到无品牌默认页；空时间戳崩溃；会话过期后页面静默失效；中文界面出现英文枚举；管理员误以为操作生效 | `npm run typecheck` 通过、`npm run lint` 0 error、`npm run build` 成功 |

## 合并后验证

集成分支 `integrate/review-fixes-20261015` 合并全部五个分支（无冲突），在隔离 PG18 数据库执行：

```text
./scripts/go.sh vet ./...        exit 0
./scripts/go.sh build ./...      exit 0
./scripts/go.sh test -count=1 -timeout 40m ./...   exit 0
  internal/httpapi 841.136s，internal/store 6.238s，cmd/legacy-import 66.970s，其余包通过，0 失败
web: typecheck 通过、lint 0 error / 7 warning、next build 成功
```

跳过项仍为需要外部依赖的浏览器夹具与真实凭据用例，未计入通过数。

## 独立复核

除分支作者自测外，另派独立复核员逐项构造反例，结论：9 项修复中 8 项 VERIFIED、1 项 PARTIAL，
未发现回归性契约破坏、无伪修复、无测试弱化。同时独立确认：

- 供应商与 NOKVM 的 `sync` 路径确实只发 GET，因此崩溃后自动重试不会重复创建上游资源。
- 写动作的 `waiting_verification` 保守语义未被放松。
- 迁移 139 的 CHECK 与迁移 100 的触发器 CASE 一致，不会拒绝既有合法数据。
- RBAC 映射未改动，路由数量不变（544）。

## 未决事项（需产品/运维决定，本批次不改）

1. **`require_active_order` 语义**：实现只认人工审核提升后的 `active` 状态，而常规支付流程把订单停在 `paid`。
   legacy 的 `addfunds_require_order` 检查单一"已激活"状态，在 Go 中对应 `paid` 与 `active` 两者。
   当前保守实现意味着开启该开关后，走常规支付闭环的客户将无法充值。代码注释已如实说明现状；
   是否放宽为同时接受 `paid` 属产品决定。
2. **自动 IP 黑名单只增不减**，且出口 IP 共享（NAT/跳板）时任一使用者触发阈值会牵连该出口的其他账号。
   建议增加过期行清理与更细的封锁粒度。
3. **营业额基数不做退款冲正**，仅靠读侧排除已退款账单；已按含退款基数结算过的早先账单不会重算。
4. **迁移 139 在单事务内全表修复并加约束**，大表上线需预留锁窗口。
5. 本次修复未应用任何历史数据回填；`docs/affiliate-rules-api.md` 记录了只读诊断 SQL。

## 说明

本批次未运行完整的 Mimosa 项目审计（其扫描基线因文件数上限不完整），因此本记录只声明上述
测试与复核结论，不代表项目整体安全状态。
