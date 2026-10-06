# NOKVM NAT 端口转发与建站

Go API、worker、管理员核验和 Next.js 客户/后台服务详情已接入。旧 PHP 的 `nokvm.php` 435–549 行和 `nat_acl.html`、`nat_web.html` 仅用于确认协议与字段；不读取旧业务数据。

## 已确认的上游接口

| 资源 | 本 VM 列表 | 创建 | 删除 |
| --- | --- | --- | --- |
| 端口转发 | GET `/api/nat_acl/{virtual_id}` | POST `/api/nat_acl/`，表单 `virtual,name,type,interior_port` | DELETE `/api/nat_acl/{id}` |
| NAT 建站 | GET `/api/nat_web/{virtual_id}` | POST `/api/nat_web/`，表单 `virtual,domain,exterior_port,exterior_type,interior_port,interior_type` | DELETE `/api/nat_web/{id}` |

端口转发外网端口由供应商自动分配；协议依旧模板为 `tcp`、`udp`、`all`。建站依旧模板仅提供外网端口 80、双侧 HTTP；域名不允许 URL、端口、凭据、路径或控制字符。端口为 1–65535。供应商签名只由 Go 服务端构造，NAT 列表只向客户返回 `id/name/type/domain/exterior_port/interior_port`，ID 保持十进制字符串，可超过 JavaScript 安全整数。列表上限 1000、响应上限 1 MiB；重复 ID、重复 JSON 键或无效字段均拒绝。创建前读取本 VM 列表检查购买额度；删除在预读和 worker 发送前分别检查目标属于本 VM，worker 还比较预读时的字段。

## 本地 API 与安全边界

GET/POST `/api/v1[/admin]/services/{id}/nat/{kind}`，`kind` 为 `acl` 或 `web`。GET 返回 `kind/limit/items/fetched_at`；POST 使用 `Idempotency-Key`（8–128 字符），`risk_acknowledged:true`，以及：

- 转发创建：`{"operation":"create","point_id":"","name":"SSH","type":"tcp","interior_port":22,"risk_acknowledged":true}`。
- 建站创建：`{"operation":"create","point_id":"","domain":"example.test","exterior_port":80,"interior_port":8080,"risk_acknowledged":true}`。
- 删除：`{"operation":"delete","point_id":"9007199254740993","risk_acknowledged":true}`。

GET 要求本人已审核 active/suspended NOKVM 服务，或管理员 `services.view` 与客户范围；POST 只允许 active，管理员还需 `services.manage`。功能以不可变购买交付快照中的 `nat_acl_limit` / `nat_web_limit` 大于 0 为准；现有产品交付没有单独 NAT 布尔开关。额度 0 不读取供应商目录，也不允许写。产品目录后续修改不改变已购额度。

首次请求 202，同键同请求重放 200；HMAC 指纹包含服务、种类、动作、目标与全部创建字段。不同内容复用键返回 409。预读在事务外完成，真正入队时重验会话/API key、账户状态、管理员授权与范围、服务归属、审核、active 状态、上游绑定与购买快照。服务现有任务、配置变更、上游处理中/暂停/限流状态阻断写操作；NAT 操作有 60 秒冷却。通用 `services/{id}/actions` 拒绝绕开风险确认。schema072 限定四个动作、payload 形状，并禁止更改已入队任务的服务、动作、幂等键、指纹及 payload。

worker 持久化发送标记后释放数据库锁并调用供应商。供应商 `code=0` 只表示命令受理，四种动作一律进入 `waiting_verification`；响应未知、worker 中断、已发送租约过期、落库失败均禁止自动重发。管理员须在供应商控制台实地核验实际结果，再通过既有 delivery-verification API 记录 `applied` 或 `not_applied` 及依据。列表变化本身不作为完成证据。核验与审计同事务，审计失败会回滚。NAT 不改系统凭据、财务、库存或账期。

本地 HTTP 供应商夹具和独立 PG18 测试验证了协议、四动作、归属/额度/权限、预读撤权、并发幂等、未知结果、核验回滚、已发送租约和财务不变。真实 NOKVM 供应商尚未联调，尤其应确认列表字段类型及 `all` 映射的供应商行为；这些测试不证明真实资源已经创建或删除。
