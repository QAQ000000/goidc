# NOKVM 原生交付

Go API 与 worker 原生实现 NOKVM 协议。旧 PHP 仅用于核对协议，不需要导入旧数据。覆盖固定规格商品的开通、暂停、恢复、终止、资源同步、五种电源操作、系统密码重置、系统重装、资源监控及快照/备份；供应商真实环境尚未验收，本地 HTTP 协议 fixture 与独立 PostgreSQL 18 用于验收。

## 配置与商品

供应商 URL/token 仅放部署环境，数据库、前端和商品配置不存 token。API 与 worker 使用同一份配置。环境文件示例（均为占位值）：

```sh
NOKVM_PROFILES='[{"id":"primary","name":"主供应商","url":"https://supplier.example","token":"REPLACE_ME"}]'
PROVISION_ENCRYPTION_KEY=REPLACE_WITH_RANDOM_64_HEX_CHARACTERS
```

独立 AES-256-GCM 密钥可用 `openssl rand -hex 32` 生成，必须与数据库一起备份；当前不提供自动密钥轮换。配置不完整时进程拒绝启动，空配置保持人工/模拟模式可用。只允许 HTTPS 或回环 HTTP fixture，验证 TLS，禁止跟随重定向，不使用环境代理转发供应商请求，单次 HTTP 超时 30 秒、响应最多 1 MiB。

后台商品基本资料选择“NOKVM 自动交付”，选择供应商及填写固定规格。区域/模板/节点使用供应商实际 ID，内存 MB、数据盘 GB、带宽 Mbps、流量 GB。登录用户名明确选择 root 或 administrator，密码由 worker 安全生成；VNC 密码遵循旧模块的八字符约束。商品复制包含交付配置。下单时按每件服务保存不可变交付快照，后续修改目录不影响已购服务。

当前不把任意商品选项猜测为供应商资源参数：NOKVM 商品不能启用可配置选项，已有启用配置组的商品不能切换此模式。资源升降配及跨商品升级在报价时明确拒绝；单纯账期变更在本地处理，不发送资源变更请求。VNC 页面、用量计费及其余供应商模块继续待实现。

## 协议依据

参考：`/www/wwwroot/zjmf-manger-decoded-副本/public/plugins/servers/nokvm/nokvm.php` 的 `nokvm_CreateSign`、`nokvm_CreateAccount`、`nokvm_SuspendAccount`、`nokvm_UnsuspendAccount`、`nokvm_TerminateAccount`、`nokvm_Sync`、`nokvm_Status`，及 `docs/12-server-modules.md`。

签名将 token、Unix 秒字符串、随机六字符按 PHP `SORT_STRING` 字典序排序，拼接并 MD5，转大写十六进制；query 只发送 time/random/signature。请求 body 为表单，响应必须具有整数 code，0 表示成功。上游原始 message/body、签名 URL 及 token 不写任务结果或客户错误。

| 动作 | 请求 |
| --- | --- |
| 开通 | POST `/api/virtual` |
| 暂停 | GET `/api/virtual_pause/{id}` |
| 恢复 | GET `/api/virtual_restore_pause/{id}` |
| 终止 | DELETE `/api/virtual/{id}` |
| 同步 | GET `/api/virtual/{id}`，GET `/api/virtual_run_status/{id}` |
| 开机 power_on | GET `/api/virtual_power/{id}/start` |
| 关机 power_off | GET `/api/virtual_power/{id}/shut` |
| 重启 reboot | GET `/api/virtual_power/{id}/restart` |
| 强制关机 force_off | GET `/api/virtual_power/{id}/compel_shut` |
| 强制重启 force_reboot | GET `/api/virtual_power/{id}/compel_restart` |
| 系统密码 reset_password | PUT `/api/virtual_reset_password/{id}`，表单 type=system/password |
| 系统重装 reinstall | GET `/api/virtual_reset_system/{id}/{template_id}`，无表单/密码参数 |

电源端点依据旧文件 `nokvm_On/Off/Reboot/HardOff/HardReboot`（1043–1131 行）。执行前读取真实电源状态，只有 on/off 可操作；已开机不能再开机，已关机不能关机或重启。处理中、上游暂停或流量限制均拒绝。预读失败时没有发送命令，任务失败；发送后的超时、非200或含重复键（含大小写别名）的 JSON 进入待核验。响应只用于确认命令受理，不证明虚拟机已完成动作。

成功受理后只更新缓存电源为 processing，保留 IP、名称、本地 active 计费状态、账期、余额和库存；原子新增延迟5秒的 sync 任务。同步仍返回 processing 时需要再刷新，当前不无限轮询。人工核验已完成同样触发一次只读同步。新电源动作共享30秒冷却和服务任务互斥，缓存 processing 也拒绝新电源动作；相同幂等键返回原任务，不再发送命令。

## 系统密码重置

依据旧模块 `nokvm_CrackPassword`（1231–1252 行）。客户本人及具有 services.view/manage 与客户范围的管理员，通过专用 `POST /api/v1[/admin]/services/{id}/reset-password` 提交 `{ "password": "..." }` 和 Idempotency-Key。仅 active NOKVM 服务支持；人工/模拟、财务暂停、上游处理中/暂停/流量限制、配置变更或已有任务均拒绝。密码限制为12至128位非空格可见ASCII字符；前端两次输入、随机生成、显隐与关闭清空，不放浏览器持久存储。

排队时使用服务ID与任务幂等键绑定的AES-GCM保存待发送密码，指纹用部署密钥HMAC绑定完整请求。普通详情/任务/audit不返回明文或待发送密文/指纹。相同键及相同密码可重放，改密码冲突；重放在60秒冷却前判断。数据库保护请求绑定和指纹不可变，完成/失败/取消时自动清除待发送密文。

worker 在事务外先读取电源状态，再发送 PUT。code0 后读取 `/api/virtual/{id}` 的 sys_pwd，常量时间匹配待发送密码才将新系统密码重新加密为当前凭据；原VNC密码、电源信息、计费状态、账期、余额与库存保持不变。这只确认供应商记录，不能替代实际客户机登录验收，通用sync不会从响应覆盖本地凭据。

明确拒绝保留原密码。发送后未知、确认读取失败/缺失/不匹配、取消或本地完成审计回滚，均进入/恢复到待核验，不自动再发PUT；pending/running/waiting_verification重置任务期间，凭据读取409。管理员确认供应商实际结果后登记 applied 才提升待发送密码，not_applied保留旧凭据；两种核验均清除待发送密文并审计，不再发送命令。核验依据包含本次新密码时拒绝，避免写入审计。前端提交报错会刷新任务状态，成功历史显示“已确认”，只读管理员无重置按钮且写API403。

开通字段包含 users_id、稳定本地账号标识 username、areas_id、core、cpu_mode、memory、data_disk_size、net_out/net_in、snapshoot（原协议拼写）、backups、templates_id、sys_pwd/vnc_pwd、ip_num、flow_limit、nat_acl_limit/nat_web_limit，以及可选 nodes_id。expire_time 沿用旧协议的 `2999-01-01 00:00:00`，本地账期仍由财务/终止队列控制。服务 resource_info 只保存经筛选的资源 ID、名称、有效 IP 与电源状态；普通关机不等同于财务暂停，同步不擅自改变本地计费状态。

## 系统重装

依据旧模块 `nokvm_Reinstall`（1190–1228行）、`app/openapi/controller/HostController.php:getReinstall` 与 `app/common/logic/Host.php:reinstall`。旧镜像列表来自商品配置子项，选项映射供应商镜像ID；没有已确认的上游镜像目录或安装完成接口。本实现使用后台商品的 `delivery_config.templates` 作为可重装白名单，不请求虚构目录端点。每项包含 id/name/group/login，最多100项，ID为1至2147483647且唯一，登录用户名显式选择root或administrator；默认开通模板仍由resources.templates_id控制，不要求加入重装目录。

客户本人或有服务查看权限及客户范围的管理员通过 GET `/api/v1[/admin]/services/{id}/reinstall-templates` 读取当前目录。目录为空、商品已切换交付模式或供应商profile不同于服务购买快照时不能重装；原服务继续保留购买时的供应商。管理员可增删改镜像，复制商品携带目录；目录调整不改已排队的任务。

POST `/api/v1[/admin]/services/{id}/reinstall` 接收 `{ "template_id": 9, "data_loss_acknowledged": true }` 和 Idempotency-Key。管理端写入要求services.view/manage与客户范围；仅active NOKVM服务支持，受限电源、其他任务或配置变更均拒绝。镜像必须在当前目录内，完整所选镜像快照保存为不可变request_payload；HMAC指纹绑定服务、幂等键及模板ID。相同键同镜像可重放，改镜像冲突，重放优先于5分钟冷却及目录变化。通用actions端点不能绕过专用确认流程。前端需选择镜像、输入准确服务名称、勾选备份与数据清除风险，提交报错后刷新任务以恢复响应丢失状态。

worker在事务外预读上游电源，只有on/off可发送旧签名GET。明确拒绝使任务failed并保留旧系统与凭据；code0只表示受理，缓存标为processing，任务保持waiting_verification，不宣称系统安装完成。发送后超时、异常JSON、重复键或迟到响应沿用保守恢复规则，禁止自动重发。pending/running/waiting_verification期间读取凭据返回409。

管理员先在供应商控制台及实际客户机确认所选系统安装完成，再登记applied及12至128位已核验的新系统密码；前端需两次输入一致。新密码仅写入服务绑定的AES-GCM当前凭据，不存入任务结果/audit；核验指纹用部署密钥HMAC绑定含密码的完整请求，核验依据不得含新密码。原VNC密文内容保留，但本批不保证重装后实际VNC密码有效性，VNC功能待单独实现与验收。

核验成功独立更新服务installed_os与username，不改购买交付快照、财务状态、账期、库存或余额；自动延迟5秒执行一次只读同步，sync不覆盖installed_os或凭据。相同核验请求可重放，不同内容冲突，审计失败回滚凭据及状态。not_applied必须确认上游未执行重装且原系统和凭据仍有效，保留旧记录并触发状态同步；安装仍在进行时须保留待核验任务。两种核验均不再次发重装请求。真实供应商重装和客户机登录尚未验收。

## 资源监控

依据旧模块 `nokvm_ChartData`（156–269行）的签名 GET `/api/virtual_monitoring/{id}`，解析 CreatedAt 及 Cpu/Network/Disk 中的二层 JSON。API 支持 period=1h/6h/24h/7d，省略时为1h，其余值拒绝；供应商没有已确认的范围查询参数，Go 获取后在服务端筛选、排序并去重。相同时间的冲突样本拒绝，最多12000行且沿用1MiB响应上限。CPU保留供应商百分比（含多核合计），网络与磁盘速率沿用旧模块KB/s，仅暴露vda/vdb；缺失值为null，图表断开且CSV留空，不伪造零。

带时区时间按RFC3339解析；无时区时间按profile的 `monitoring_timezone` IANA名称解析，默认Asia/Shanghai。这个默认值是部署约定，接入前需向供应商确认，不能据此认定其实际时区。无效时区使配置加载失败。API统一UTC时间，浏览器按本机时区展示，CSV导出UTC原值。

再按旧 `nokvm_Sync` 的 GET `/api/virtual/{id}` 读取flow累计入站/出站bytes和flow_limit GiB。累计统计周期由供应商决定，与图表选择范围独立；Go不据此收费或自动暂停。计数及限额用十进制字符串返回，前端BigInt换算GiB，避免JavaScript整数精度损失；总和溢出或累计读取失败时usage为null/usage_available=false，已取得的图表仍返回。限额0显示不限，缺失限额显示暂不可用。

客户限本人已审核的active/suspended NOKVM服务；管理员只需services.view并满足客户范围。每分钟30次限制、no-store，网络调用在事务外，结束后重新检查账号、权限、范围、归属、资源/profile和服务状态，HTTP层再检查会话/API key撤销。查询不修改服务、凭据、账期、余额、库存或开通任务。前端提供四种范围、CPU/网络/磁盘切换、vda/vdb选择、提示值、刷新、CSV及加载/错误重试/空数据状态。证据见 [监控验收](archive/README.md#f-0546e04e5252)。

## VNC 与后续范围

快照/备份已接入API、任务及操作页面，详见 [快照与备份](nokvm-storage.md)。VNC、流量计费与超额自动暂停继续待完成。旧VNC实现使用含通用供应商签名的链接，该签名没有绑定资源路径；直接返回浏览器会扩大客户的上游访问能力。目前没有已确认的安全WebSocket端点及授权协议，不能虚构端点或交付该通用签名。后续需依据供应商实际协议实现服务绑定的短期授权及代理，再验收浏览器连接、隔离与过期行为。

## 请求结果与恢复

数据库事务先锁服务与任务、检查租约与审批状态，保存 external_started_at 和加密凭据，再提交事务。网络调用不持行锁。完成事务再次检查 status/attempts/租约，原子更新服务、任务、审计及终止库存。任何已发送任务均不能由第二个 worker 再次执行。

NOKVM 开通接口没有可确认的幂等键契约，不发送虚构的幂等参数。超时、断网、非 200、缺少 code、异常 JSON、无资源 ID、旧 worker 租约到期，均保留 `waiting_verification`。若上游成功而本地完成事务回滚，external_started_at 仍保留；租约恢复进入待核验，不能自动重新开通。明确 code 非 0 才作为供应商拒绝失败，不自动重试；修复后须显式提交新动作。

待核验任务占用同一服务的唯一处理中任务槽位，阻止重复开通、其他服务动作、配置/账期变更、取消审核及未核验订单退款。退款触发真实删除资源，删除成功后才归还有限库存。

管理员必须先到供应商控制台核对实际资源及操作，再通过后台服务详情登记核验结果：

- 已完成：开通必须登记有效资源 ID，其余动作使用已关联资源；只完成本地状态，不再次请求供应商。
- 未完成：结束为 failed。开通服务恢复为失败并清除未交付凭据，后续重试须显式提交新动作。
- 同步：结束原任务后重新同步，人工核验不能捏造同步响应。

重复登记相同内容可重放，不同结果冲突。资源 ID 在供应商 profile 内唯一；核验与密码读取同时验证后台权限、客户归属范围并写审计。

## API 与凭据

| API | 权限 |
| --- | --- |
| GET `/api/v1/admin/provision-profiles` | products.view，只返回 id/name/type |
| POST `/api/v1/admin/services/{id}/delivery-verification` | services.view/manage 与客户范围 |
| POST `/api/v1/services/{id}/credentials` | 服务所有人，active/suspended |
| POST `/api/v1/admin/services/{id}/credentials` | services.view/manage 与客户范围 |
| POST `/api/v1/services/{id}/actions` | 本人 NOKVM 服务；sync 允许 active/suspended，五种电源动作仅 active |
| POST `/api/v1/admin/services/{id}/actions` | services.view/manage 与客户范围；同样执行状态和模式校验 |
| POST `/api/v1/services/{id}/reset-password` | 服务所有人，active NOKVM |
| POST `/api/v1/admin/services/{id}/reset-password` | services.view/manage 与客户范围，active NOKVM |
| GET `/api/v1/services/{id}/reinstall-templates` | 服务所有人，NOKVM |
| GET `/api/v1/admin/services/{id}/reinstall-templates` | services.view 与客户范围，NOKVM |
| POST `/api/v1/services/{id}/reinstall` | 服务所有人，active NOKVM、目录白名单与数据风险确认 |
| POST `/api/v1/admin/services/{id}/reinstall` | services.view/manage 与客户范围，同样执行重装校验 |
| GET `/api/v1/services/{id}/monitoring` | 服务所有人，已审核active/suspended NOKVM |
| GET `/api/v1/admin/services/{id}/monitoring` | services.view 与客户范围，已审核active/suspended NOKVM |
| GET `/api/v1/services/{id}/storage/{kind}` | 服务所有人，已审核active/suspended NOKVM |
| GET `/api/v1/admin/services/{id}/storage/{kind}` | services.view 与客户范围 |
| POST `/api/v1/services/{id}/storage/{kind}` | 服务所有人，active NOKVM，风险确认及幂等键 |
| POST `/api/v1/admin/services/{id}/storage/{kind}` | services.view/manage 与客户范围，其余条件同客户 |

客户和有管理权限的管理员可以刷新资源；只读管理员仅查看资源和任务历史，电源/刷新/凭据写控件隐藏，写 API 返回403。人工/模拟模式不支持电源操作，客户也不能同步这两种模式。关机/重启需确认，强制操作另需勾选数据风险；成功任务显示“已受理”，实际电源单独展示。

密码采用随机 nonce 的 AES-GCM，绑定本地服务 ID，不能把另一服务的密文移来使用。服务详情/任务结果不返回密码。只有显式读取凭据接口返回用户名与系统密码，Cache-Control 为 no-store，前端对话框一分钟自动隐藏；终止成功清除密文。不向客户返回供应商 token、VNC 签名或原始系统密码字段。
