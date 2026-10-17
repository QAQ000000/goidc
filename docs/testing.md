# 测试入口

测试按行为和业务域保留，不以测试/生产行数比例作为删除标准。资金、权限、并发、幂等及故障恢复断言是必需的回归保护。

| 范围 | 入口 | 前置条件 |
| --- | --- | --- |
| 编译全部 Go 测试 | `./scripts/go.sh test ./... -run '^$'` | Go 依赖可用；不执行测试 |
| Go 单元及可用集成测试 | `./scripts/go.sh test ./...` | 无测试 DSN 时数据库用例会跳过，不能声称集成验收通过 |
| 强制数据库全量 | `./scripts/test-all.sh` | `ZJMFGO_TEST_DATABASE_URL` 指向独立 PG18、库名以 `_test` 结尾 |
| Node 工具回归 | `node --test scripts/*.test.mjs` | Node；进程、namespace 等专项以各项环境条件为准 |
| worker 强杀恢复 | `./scripts/test-worker-recovery.sh` | 回环 PG18 测试库；实际执行两套 SIGKILL 用例 |
| 浏览器夹具 | `go test` 的具体 `Test…BrowserFixture` | 测试 DSN、新的 ready/finish 路径和外部浏览器驱动 |

浏览器夹具是供浏览器连接的临时 API 环境，单独启动或编译夹具不代表浏览器验收。未显式配置的浏览器、真实外部渠道、旧库及常驻进程专项仍可能跳过。

`internal/httpapi/browser_helpers_test.go` 统一 ready/finish 校验、私有 JSON 写入、等待期限及 worker 退出；各业务文件只维护场景和断言。`stop` 会取消并等待 goroutine，可在断言前调用，也可在清理中重复调用。NOKVM 的测试时钟调整保留在具体场景中。

浏览器夹具常用变量：`ZJMFGO_BROWSER_READY_FILE`、`ZJMFGO_BROWSER_FINISH_FILE`、`ZJMFGO_BROWSER_ORIGIN`、`ZJMFGO_BROWSER_LISTEN_ADDR`。`ZJMFGO_BROWSER_TIMEOUT_SECONDS` 可覆盖等待期限，仍受 Go 测试总期限约束；供应商等专项可有自己的变量。

前端检查仍使用 `npm --prefix web run lint`、`typecheck`、`test:proxy`。构建使用独立目录，禁止绕过运行目录保护；此文档不启动或重启项目服务。

静态审查生成器 `node scripts/route-coverage.mjs` 和 `node scripts/admin-page-contracts.mjs` 将新结果写入忽略的 `output/audit/`。前者仍读取版本化的 `docs/generated/legacy-inventory.json` 与 `docs/audit/route-reviews.json`；生成器所需旧源码及旧页面规范须另行提供。历史生成快照保存在封存包。
