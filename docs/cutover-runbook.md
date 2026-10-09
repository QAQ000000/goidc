# 生产切换与可验证回滚 Runbook

本文是将旧 PHP 站点切换到 Go API + Next.js 的操作规程。它描述可执行的
停机窗口、证据和回滚动作，不代表当前系统已经满足生产替换条件。当前 P0
结论见 [`rewrite-readiness.md`](rewrite-readiness.md)，在真实支付、真实上游、
数据问题复核和回滚演练完成前，执行人必须选择“停止切换”。

## 0. 角色、变量和硬门槛

需要四个角色：变更负责人、数据库负责人、应用负责人和业务验收人。至少两人
在电话或值班群中确认每个闸门。示例变量如下，生产执行时从受控密钥系统注入，
不要写入 shell 历史或 Git：

```bash
export LEGACY_ROOT=/www/wwwroot/zjmf-manger-decoded-副本
export APP_ROOT=/www/wwwroot/zjmfgo
export MYSQL_DB=idc
export PG_DB=zjmfgo_prod
export RELEASE=/www/wwwroot/zjmfgo/releases/<BUILD_ID>
export CUTOVER=/var/backups/zjmfgo/cutover-$(date -u +%Y%m%dT%H%M%SZ)
```

切换前必须有：

* MySQL 只读导出、校验和、binlog/GTID 位置和可读取的恢复副本；
* PG18 custom dump、迁移版本、Go/Web/Worker 构建校验和及配置快照；
* 独立 `_test` PG18 的导入 dry-run、apply、金额和服务快照核对；
* 至少一条真实支付回调、供应商交付和通知投递证据；
* 回滚负责人明确知道旧 PHP 的 Nginx upstream 和最近一次成功备份。

当前不得以“测试库导入成功”代替这些门槛。旧目录只能读，任何备份、维护页、
Nginx 变更都必须写到独立发布目录。

## 1. 停机窗口 Checklist

时间以 `T0` 为正式切流时刻。每一步记录 UTC 时间、执行人、命令退出码和日志
路径；任意 P0 闸门失败立即跳到第 4 节回滚。

| 时间 | 操作与证据 | 闸门 |
| --- | --- | --- |
| T-30m | 发布冻结；确认值班人员、DNS/负载均衡 TTL、旧 PHP upstream、`$CUTOVER` 可写；执行 `sha256sum` 记录三项二进制和 `.next/BUILD_ID`。 | 清单签字 |
| T-20m | 在 Nginx 临时 server 中挂维护页，只允许 `/healthz`、`/readyz` 和回滚管理来源；`nginx -t && systemctl reload nginx`，curl 公网确认 503/维护页。 | 旧站不再接收业务写入 |
| T-18m | 停止 Go Worker 的消费或将调度锁设为 drain，等待当前外部调用完成；记录队列深度和 in-flight 任务。 | 无未知外部动作 |
| T-15m | 以有权限的 DBA 执行 `SET GLOBAL read_only=ON; SET GLOBAL super_read_only=ON;`，断开应用连接；保留 root 管理连接。 | `SHOW GLOBAL VARIABLES LIKE '%read_only%'` 全为 ON |
| T-14m | 导出 MySQL 一致性快照（示例见下节），写入 `$CUTOVER/mysql.sql.gz` 和 `.sha256`；记录 `SHOW MASTER STATUS` 或 GTID。 | 文件可读、校验和可复算 |
| T-10m | 在目标 PG18 执行 `pg_restore --clean --if-exists` 到预创建的空库，运行 `./scripts/go.sh run ./cmd/migrate`；检查迁移版本、表约束、金额 CHECK 和行数摘要。 | PG schema/摘要通过 |
| T-7m | 运行增量导入工具的只读预检；只有报告没有阻断问题时才执行 apply。导入必须使用已审核的 `--file-author-id`，禁止对开发库运行。 | source checksum 未漂移 |
| T-5m | 启动 API、Web、Worker：`systemctl start zjmfgo-api zjmfgo-web zjmfgo-worker`；等待 `/readyz=200`，检查 8088/3088 监听、systemd 状态、日志无 panic。 | 三服务 ready |
| T-3m | 冒烟：匿名首页、登录页、管理员登录、`/api/v1/site`、`/api/v1/catalog`、客户账单只读、支付回调签名拒绝和一个已知业务只读请求。 | 全部返回预期状态码 |
| T-1m | 业务验收人核对客户、商品、在运服务、订单/账单分、通知 outbox 和 worker 无死信增长。 | 业务摘要一致 |
| T0 | 移除维护页，启用标准反代并 `nginx -t && systemctl reload nginx`；记录公网首个 200 和发布 BUILD_ID。 | 切流完成 |
| T+2m | 连续两分钟探针和错误率观察；禁止此时修改商品/账单策略。 | 观察通过 |
| T+5m | 业务验收人完成登录、账单读取、模拟支付/真实测试支付回调和服务详情检查；任一失败按第 4 节执行。 | 放行或回滚 |

### MySQL 快照示例

```bash
mkdir -p "$CUTOVER"
/www/server/mysql/bin/mysqldump --host=127.0.0.1 --user=root --password \
  --single-transaction --routines --events --triggers --hex-blob \
  --databases "$MYSQL_DB" | gzip -1 > "$CUTOVER/mysql-${MYSQL_DB}.sql.gz"
sha256sum "$CUTOVER/mysql-${MYSQL_DB}.sql.gz" > "$CUTOVER/mysql.sha256"
/www/server/mysql/bin/mysql --host=127.0.0.1 --user=root --password -N -e \
  "SHOW MASTER STATUS; SHOW GLOBAL VARIABLES LIKE 'gtid_binlog_pos';"
sha256sum "$CUTOVER/mysql-${MYSQL_DB}.sql.gz"
```

导出必须在只读开关后进行；若生产使用复制，应同时记录 replica lag、relay
位置和备库可恢复性。导出后不要把口令、完整 DSN 或 cookie 写入日志。

### PG18 校验示例

```bash
pg_dump --format=custom --no-owner --file="$CUTOVER/pg-before.dump" "$DATABASE_URL"
sha256sum "$CUTOVER/pg-before.dump" > "$CUTOVER/pg.sha256"
psql "$DATABASE_URL" -Atc "select max(version) from schema_migrations"
psql "$DATABASE_URL" -Atc \
  "select count(*) from users; select count(*) from products; select count(*) from orders; select count(*) from invoices; select count(*) from services"
curl --fail http://127.0.0.1:8088/readyz
```

项目导入器默认为 dry-run；正式 apply 必须显式指定 `DATABASE_URL`、真实源
`LEGACY_MYSQL_DSN`、只读 `LEGACY_UPLOAD_ROOT` 和有效超级管理员 ID。它会保存
源记录校验和、旧 ID 映射、金额复核项和 `password_reset_required` 标记。

## 2. 标准生产 Nginx 反代模板

这是发布配置模板，不是让执行人直接覆盖当前 127.0.0.1 PHP vhost。先复制旧
配置和校验和，替换 `<PUBLIC_HOST>`、证书、维护页路径和日志路径，再执行
`nginx -t`。旧 PHP upstream 必须保留为 `legacy_php`，以便五分钟回滚。

```nginx
upstream zjmfgo_api { server 127.0.0.1:8088; keepalive 32; }
upstream zjmfgo_web { server 127.0.0.1:3088; keepalive 32; }
upstream legacy_php { server 127.0.0.1:9000; } # 按旧站真实 fastcgi/upstream 替换

map $http_upgrade $connection_upgrade { default upgrade; '' close; }

server {
    listen 80;
    server_name <PUBLIC_HOST>;
    client_max_body_size 50m;

    location = /healthz { proxy_pass http://zjmfgo_api; }
    location = /readyz  { proxy_pass http://zjmfgo_api; }
    location /api/ {
        proxy_pass http://zjmfgo_api;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Origin $http_origin;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 75s;
        proxy_send_timeout 75s;
    }
    location / {
        proxy_pass http://zjmfgo_web;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 75s;
    }
    add_header X-Content-Type-Options nosniff always;
    add_header X-Frame-Options SAMEORIGIN always;
    add_header Referrer-Policy strict-origin-when-cross-origin always;
    add_header Permissions-Policy "camera=(), microphone=(), geolocation=()" always;
}
```

不要用 `Access-Control-Allow-Origin *`。Go 的 `PUBLIC_ORIGIN(S)` 是允许来源
白名单，Nginx 只转发 `Origin`，不得自行放宽跨域。支付回调、Cookie 和写请求
必须经过同源/Origin 校验；长连接或流式供应商回调按实际端点单独设置超时，不把
全站超时无限放大。

## 3. 冷启动和自愈验收

三个单元的依赖关系是 API 先于 Web/Worker；每个单元配置 `Restart=on-failure`。
API ready 依赖 PG18，`/healthz` 只表示进程存活，`/readyz` 才表示数据库可用。

```bash
systemctl restart zjmfgo-api zjmfgo-web zjmfgo-worker
for i in $(seq 1 120); do
  curl -fsS http://127.0.0.1:8088/readyz && break
  sleep .25
done
systemctl is-active zjmfgo-api zjmfgo-web zjmfgo-worker
systemctl show zjmfgo-api zjmfgo-web zjmfgo-worker \
  -p MainPID -p ExecMainStartTimestamp -p MemoryCurrent
curl -fsS http://127.0.0.1:8088/healthz
curl -fsS http://127.0.0.1:3088/admin-login
```

若 API 进程存活但 `/readyz` 非 200，禁止切流；检查 PG、迁移和 `journalctl -u
zjmfgo-api`。若 Worker 重启，先检查 provision/outbox 任务的幂等状态，不能用
重复启动掩盖未知供应商结果。

本次预演证据（2026-10-09 UTC）如下：三单元 active，API ready 约 397ms；RSS
基线 API 18MiB、Web 75MiB、Worker 14MiB；8088 的 healthz/readyz 和 3088 的
首页、`/admin-login` 均为 200。原始日志在被 Git 忽略的
`output/cutover-rehearsal-20261009033837Z-cold-start/`。

随后对每个单元注入一次受控 `SIGKILL` 验证自愈：API 3.241s、Web 3.897s、
Worker 3.177s 内恢复到新 PID 和业务探针，三者最终 active；证据在
`output/cutover-rehearsal-20261009040834Z-self-heal/`。探针必须允许重启瞬间的
连接拒绝，并在窗口内持续重试；一次瞬时拒绝不是切换失败。

## 4. 五分钟可验证回滚

满足任一条件立即回滚：`/readyz` 连续 30 秒非 200；公网 5xx 超过基线两倍且持续
60 秒；客户/账单/金额摘要不一致；支付回调验签或幂等失败；供应商动作出现未知
结果；通知重复投递；任何数据撕裂、权限越界或 panic。业务负责人可以直接判定
高风险，不等待五分钟。

从 `R0` 开始计时：

1. `R0-R30s`：Nginx 恢复维护页，停止新流量；保留 `/healthz` 供运维访问。
2. `R30s-R60s`：停止 Go Worker 和 API/Web 写入口：
   `systemctl stop zjmfgo-worker zjmfgo-web zjmfgo-api`。记录正在执行的任务，
   对外部未知结果标记人工核验，不重放。
3. `R60s-R120s`：确认没有 PG 双写/异步消费者；撤销发布配置，恢复已校验的
   `legacy_php` Nginx server，`nginx -t && systemctl reload nginx`。PHP 只读解除
   前先确认它连接的是原 MySQL，不能让旧站连接 PG。
4. `R120s-R180s`：若 Go 产生了任何业务写入，保留 PG 作为审计证据，不把它们
   反向写回 MySQL；使用发布前 PG dump/快照恢复到新的隔离 PG 目标，禁止覆盖
   正式库后再猜测。若没有写入，直接弃用该 PG 数据库即可。
5. `R180s-R300s`：`SET GLOBAL read_only=OFF` 只对 MySQL 执行一次；旧 PHP
   `/healthz`/首页/登录/账单只读冒烟，业务验收人确认订单、账单和服务。最后解除
   维护页并观察五分钟。

这套回滚不做“PG 写入复制回 MySQL”，因此不会制造双向冲突。支付、供应商和
Worker 的外部调用必须以任务状态、供应商查单和人工确认结束；未知状态不得按
失败重试。回滚后保存 `$CUTOVER`、Nginx diff、systemd 日志、PG/旧库校验和，
由变更负责人签字后才能重新安排切换。

## 5. 本次冷启动/割接预演结果

使用真实旧 MySQL 的只读账号和旧 `public/upload`，目标为临时 PG18 数据库
`zjmfgo_legacy_dryrun_20261009033303_test`，结束时已自动 `DROP DATABASE`。

* `dry_run` 与完整 `apply` 均退出 0；源实体包括 4 用户、28 商品、11 订单、
  12 账单、1 服务、2 工单、3 附件、33 邮件模板和全部配置子项。
* PG 中服务交付快照为 1，金额写入 `amount_cents`；旧源的账单/订单不一致、
  孤儿分项和历史复核项被保留为审计 issue，不能当作迁移错误静默吞掉。
* 旧站 `database.authcode` 通过受控环境变量 `LEGACY_CMF_AUTHCODE` 注入后，4 个
  迁移用户均导入 `legacy_cmf$` 临时哈希并设置 `password_reset_required=false`；
  归档的 4 条用户 payload 均不含 `password`。首次正确登录会条件升级为 bcrypt
  并清除临时值。算法和升级路径已有隔离集成测试，但本次真实源没有可用于登录
  验收的客户明文密码，真实账户登录仍是未验证项。
* 完整 JSON 报告和 stderr 位于 `output/cutover-rehearsal-20261009033303/`；
  逐账单整数分对比证据位于 `output/cutover-rehearsal-20261009035840-debug/precision-summary.json`，
  12/12 条源账单逐 ID 一致，总额均为 1,807,929 分。该目录不提交敏感凭据。
  报告中的历史 issue 必须由业务负责人逐条确认后才能进入正式 apply。

最新一次只读旧 MySQL 重跑在新的 `_test` PG18 库执行，设置了旧站
`database.authcode` 的 `LEGACY_CMF_AUTHCODE`：dry-run、apply 均退出 0；4 个
客户均得到 `legacy_cmf$` 临时哈希，4 条用户源归档均不含 `password` 字段。该
临时库已删除，旧 MySQL 仍只读未写入。

兼容登录代码构建后又执行了当前二进制的 systemd 验收：`zjmfgo-api`、`zjmfgo-web`
和 `zjmfgo-worker` 均 active，`/healthz`、`/readyz`、首页和 `/admin-login` 均为
200；本次启动 PID 分别为 920157、920158、920159，systemd 报告内存约 6.4、
94.8、4.0 MiB。逐个注入受控 `SIGKILL` 后，新 PID 在约 3.17s、3.74s、3.17s
内恢复，最终三个探针仍为 200；重启窗口的首次连接拒绝只发生在 Web 尚未监听期间。

本次结果证明了工具能在隔离 PG18 冷启动并完成全实体导入，不证明旧站可以立即
下线。正式切换前必须重新生成带生产快照校验和的报告，并完成真实支付、上游、
通知、密码策略和可逆回滚演练。
