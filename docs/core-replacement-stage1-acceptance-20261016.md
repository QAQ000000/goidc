# 核心替代阶段一验收（2026-10-16）

目标见 [方向与验收标准](core-replacement-goal-20261016.md)。本批关闭正确性、导航权限和证据登记问题；整个旧 PHP 替代目标仍进行中。

## 交付与审查

三个 `gpt-6.1-sol` 子代理在独立 worktree/分支开发并提交，主代理复核后集成到 `goal/core-replacement-20261016`。导航与调度子代理另做相互源码审查，未发现阻断项。

| 提交 | 结果 | 专项验收 |
| --- | --- | --- |
| `37c75cd`（合并 `bcbce60`） | 不支持暂停的前 100 项完成审计后退出当前模式候选，后续可执行项能推进；模式变化重新评估，保留锁顺序与幂等 | [调度扫描](overdue-scan-progress-acceptance-20261016.md) |
| `4d275d5`（合并 `360212f`） | API 日志菜单、服务端页面、共享页签统一 `audit.view`，其余日志仍为 `notifications.view` | [日志权限与浏览器](api-log-navigation-acceptance-20261016.md) |
| `c4f7aa0`（合并 `90a361e`） | 优惠码、合同设置、商品编辑降为部分完成，生成契约如实提示历史证据完整性 | [证据与范围](restore-evidence-scope-acceptance-20261016.md) |
| `a899673`（合并 `31b4f32`） | 未明确登记的缺摘要也触发校验失败；声明不等于历史图片有效 | 同上，5 项校验器回归 |

没有新增迁移或更改生产配置。调度修复没有为 supplier 增加 suspend 能力。

## 集成检查

新建专用角色和隔离 PG18 `_test` 数据库，设置 `REQUIRE_INTEGRATION=1`。测试连接由仓库外 0600 文件提供，每个 HTTP 用例使用独立 schema；未使用业务数据库。下表不把条件性 browser fixture 或外部验收的跳过计为完成。

| 检查 | 结果与边界 |
| --- | --- |
| `scripts/go.sh build ./...` | PASS |
| `scripts/go.sh vet ./...` | PASS |
| `scripts/test-all.sh` | PASS，强制隔离 PG18、`-count=1 -timeout=30m ./...`；HTTP 包 990.979s，legacy-import 91.944s，store 9.834s，其他包通过或无测试文件 |
| 调度/通知/日志权限相关 `go test -race` | PASS，HTTP 包 33.668s；包括新扫描推进、并发、付款竞争及真实 HTTP 权限边界 |
| `node --test scripts/api-log-navigation.test.mjs scripts/check-admin-browser-evidence.test.mjs` | 最终集成 PASS，16 项，0 fail / 0 skip |
| `npm run typecheck --prefix web` | PASS |
| `npm run lint --prefix web` | PASS，0 errors，7 条既有 warnings |
| 独立 worktree `npm run build -- --webpack` | PASS，Next 生产构建与 TypeScript；Web 源码与本批集成一致，未覆盖常驻 `.next` |
| 三身份 × 四宽度真实浏览器 | PASS，1440/1024/390/320，各高 900；登录、菜单/页签、重定向、真实列表与页面无整体横向溢出 |
| URL、空态、分页、错误恢复 | PASS，筛选刷新恢复、真实空结果与清空、下一页刷新仍为第 2 页；隔离注入 503 后恢复真实 API |
| 新截图完整性 | PASS，13 条 SHA-256 匹配；[独立证据清单](core-replacement-stage1-browser-evidence-20261016.json) |
| 历史截图严格校验 | 预期未通过：143 匹配、6 失配、8 缺摘要，`--strict` 退出 1；14 条均明确登记但不能当作有效历史图片 |
| 历史异常登记一致性 | PASS，`--strict-unacknowledged` 退出 0，只表示没有未登记异常，完整性仍为 false |

可复查新截图：

```sh
node scripts/check-admin-browser-evidence.mjs --manifest docs/core-replacement-stage1-browser-evidence-20261016.json --strict
```

截图保存在忽略目录 `output/playwright/core-goal-stage1-20261016/`，使用独立新路径；未改写历史图片和原摘要。四尺寸截图均由浏览器实际渲染获取，未替换成功响应为假数据；503 仅用于错误态验收。

## 限制及清理

首次页面打开存在既有 favicon 404；错误测试另有预期注入 503。验收捕获窗口内无 JavaScript 异常或非预期 API 失败。权限在后端即时执行，外部撤权后的可见页签需身份刷新；未宣称前端轮询实时撤权。只读日志页的保存、取消、版本冲突和危险写操作为 N/A，未独立测试慢响应加载提示。

调度结果证明候选推进和任务入队，不代表供应商实际暂停；未执行大数据量 EXPLAIN 或容量压测。未调用真实支付、供应商、SMTP/SMS 写操作。新真实商户退款、公网支付、云主机动作、历史异常截图重验及正式切换继续保留在后续范围。

浏览器从界面退出并关闭本次独立会话；HTTP fixture 正常 PASS 并结束，独立 Next 预览停止，3096/38937 不再监听。全部测试完成后删除本次专用数据库、角色及私有连接文件。生产 API/worker/Web 未重启，未推送或部署。
