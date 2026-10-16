# 供应商客户运维 API 补全

- 客户新增 `POST /api/v1/services/{id}/vnc`，与管理员既有 `POST /api/v1/admin/services/{id}/vnc` 共用 handler、每用户限流、`Cache-Control: no-store`、URL 校验及敏感信息最小化响应；成功响应沿用 `{data:{url}}`。
- 身份和管理员标志仅来自认证上下文及固定路由。请求体或 query 的 admin、actor、user_id 不影响权限。非 owner 返回 404，停用账户拒绝访问。
- 供应商服务详情新增 `supplier_capabilities`，从服务冻结 snapshot 指向的绑定及适配器读取，不请求上游；客户 `create`/`sync` 固定为 false，后台保持原能力。供应商停用或绑定缺失返回全 false。动作执行仍以 store 状态和权限检查为准。
- `services.go` 原有供应商动作范围为 create/sync、power 动作、reset_password、rescue_system/exit_rescue_system、reinstall；本次只额外禁止客户 create/sync，不开放 suspend/resume/terminate。
- VNC 返回前再次验证账户、管理员权限、owner、资源 ID/snapshot、服务版本、供应商版本及绑定。中途角色改变、权限撤销或资源变化均不释放临时 URL。审计仅记录打开事件，不包含 URL 或密码。上游未知结果单次请求失败，不自动重发。

验证使用 `/tmp/zjmfgo-refund-local.env` 的专用 PG18 `_test` 数据库，每测试创建随机 schema；上游为 httptest loopback。专项 race 通过：

```sh
go test -race ./internal/httpapi -run 'TestSupplier(CustomerConsole|ConsolePasswordRescueAndReinstall)' -count=1
go vet ./internal/httpapi ./internal/store
```

覆盖 owner 成功、其他客户拒绝、无效 URL、未知响应不重发、敏感值未写入审计、客户 create/sync 拒绝，以及请求期间账户停用、角色/权限撤销、owner/资源/服务版本/供应商版本变化。管理员原有密码、救援、重装流程回归通过。以上为本地 API/数据库/协议验证；未调用真实供应商、未变更生产进程，未证明真实 VNC 可连接。
