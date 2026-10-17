# ZJMF Go

将旧 PHP IDC 财务系统重构为 Go API、Next.js/React/shadcn/ui 和 PostgreSQL 18。
当前仍在开发及验收，不能直接替换旧 PHP 生产站点。

[项目状态与导航](docs/reconstruction.md) · [当前差距](docs/replacement-gap-audit-20261017.md) · [测试说明](docs/testing.md) · [历史审计](docs/archive/README.md)

## 结构

| 目录 | 职责 |
| --- | --- |
| `cmd/api`、`internal/httpapi` | JSON API、认证、RBAC、客户及工单范围 |
| `cmd/worker` | 资源交付、支付对账、续费和通知任务 |
| `internal/store` | PostgreSQL 事务、资金账本、订单和服务状态 |
| `internal/payment`、`supplier`、`provision`、`notification` | 编译期外部适配器 |
| `web/src/app`、`web/src/components` | 客户门户和管理后台 |
| `db/migrations` | 嵌入式 SQL 迁移与 checksum |
| `internal/g09fixture`、`scripts/g09-*` | 已可执行验证使用的协议、场景和运行工具 |

核心业务链路：商品与配置 → 购物车 → 订单/账单 → 支付 → worker交付 → 服务续费、变更或取消。
其他领域包括工单、文件、内容、信用额、返佣、报表与后台配置，按[后台台账](docs/admin-full-ui-restore-ledger.md)维护范围。

## 本地运行

要求 Go 1.25、PostgreSQL 18+、Node.js 与 npm。复制 `.env.example` 为本地 `.env` 并设置真实配置；凭据只存部署环境，不提交仓库。

```bash
set -a
source .env
set +a
./scripts/go.sh run ./cmd/migrate
BOOTSTRAP_USERNAME=root BOOTSTRAP_EMAIL=root@example.com BOOTSTRAP_PASSWORD='<本地管理员密码>' ./scripts/go.sh run ./cmd/bootstrap
./scripts/go.sh run ./cmd/api
# 另一个已载入环境的终端
./scripts/go.sh run ./cmd/worker
```

初始化只允许尚无管理员的数据库，不会重置已有用户。`cmd/demo` 可选创建开发商品。

```bash
cd web
npm ci
npm run dev
```

默认 Web `http://localhost:3088`，API `127.0.0.1:8088`。
`/healthz` 检查存活，`/readyz` 检查数据库就绪；登录页返回 200 不代表业务数据库可用。
本地 Next.js 转发 `/api/*`；正式环境应由受控反代将 `/api/` 直接转给 Go，并配置可信代理、HTTPS 和安全 Cookie，见[切换手册](docs/cutover-runbook.md)。
仓库的 systemd 文件是部署模板，使用前核对实际目录与 Node 路径。

## 验证与构建

```bash
./scripts/check-go-toolchain.sh
./scripts/go.sh test ./... -run '^$'
node --test scripts/*.test.mjs
# 显式配置独立 PG18 测试库，库名必须以 _test 结尾
./scripts/test-all.sh
npm --prefix web run lint
npm --prefix web run typecheck
npm --prefix web run test:proxy
```

无测试库的裸 `go test` 会跳过数据库用例，不是完整验收。浏览器夹具、真实渠道、旧库和 worker 专项各有前置条件，见[测试说明](docs/testing.md)。
只读负载使用 `node scripts/load-test.mjs --origin http://127.0.0.1:18088`，详见[容量验证](docs/capacity.md)。

禁止在正在运行的 Web 目录构建：`prebuild` 会检查同目录 Next 进程，不得绕过。
候选应在独立 checkout/worktree 使用独立 `.next` 构建；不要对运行实例共享的依赖执行 `npm ci`。
[候选构建器](docs/release-candidate-builder-20261017.md)支持同提交的 API/worker/migrate、SQL 摘要和 Web 产物封存；构建不等于部署。

## 领域契约

- 商品与交易：[目录](docs/catalog-management.md)、[配置](docs/product-configuration.md)、[购买资料](docs/product-purchase-fields.md)、[订单账单](docs/transactions.md)。
- 资金与服务：[支付](docs/payments.md)、[支付操作手册](docs/payment-operations-runbook.md)、[配置变更](docs/service-configuration-changes.md)、[商品变更](docs/service-product-changes.md)。
- 集成与权限：[模块扩展](docs/compiled-module-architecture.md)、[供应商协议](docs/supplier-protocol.md)、[NOKVM](docs/nokvm-delivery.md)、[RBAC](docs/admin-rbac.md)。
- 数据与发布：[旧数据迁移](docs/legacy-migration.md)、[正式发布边界](docs/release-readiness-next-contract-20261017.md)、[封存索引及恢复方法](docs/archive/README.md)。

旧 MySQL 盘点使用 `cmd/legacy-audit`；`cmd/legacy-import` 默认只读预检，明确 `--mode apply` 才写目标库。
旧接口能力覆盖不等于旧 URL/payload 兼容。运行产物写入忽略的 `output/`，活动文档维护当前契约，历史批次日志和原始证据集中封存。
