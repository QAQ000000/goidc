# NOKVM 监控验收（2026-10-06）

本批已部署，沿用schema070，无数据库迁移。旧PHP仅用于协议参考，没有提取或导入旧数据。整体重构仍进行中。

## 自动验证

| 检查 | 结果与证据 |
| --- | --- |
| 强制独立PG18全量Go | PASS，HTTP176.830s；`output/nokvm-monitoring-20261006/go-full.log` |
| NOKVM竞态测试 | PASS，HTTP26.570s；`go-race.log` |
| 供应商解析 | PASS，两层JSON、时区、范围、排序/重复、null、非法值、流量溢出与累计读取失败 |
| 授权并发 | PASS，网络不持服务锁；请求中注销、停用、撤权、改客户范围/上游及终止服务后拒绝返回 |
| Go vet / 前端typecheck / lint | PASS |
| 独立发布及预览webpack构建 | PASS；`release-build.log`、`preview-build.log` |
| npm生产依赖审计 | PASS，0漏洞；`npm-production-audit.json` |

完整npm依赖审计仍有既有eslint-config-next经fast-glob/braces引入的5项high告警，未通过强制降级Next处理；生产依赖审计为0，recharts固定版本3.10.1。

## 真实浏览器与独立数据库

Playwright操作独立Next生产预览及独立Go HTTP fixture；供应商HTTP响应为协议夹具，不是实际虚拟机。客户验证CPU/网络/磁盘切换、vda/vdb选择、四种范围、刷新与no-store。1h/6h/24h/7d分别显示6/36/72/72条，累计入站2GiB、出站1GiB、总量3GiB、限额10GiB。提示值显示实际时间及CPU百分比；vdb缺失样本曲线断开。

真实CSV下载含UTC时间及原始CPU/KB/s数值，6行，缺失vdb的读取/写入单元格为空。浏览器拦截响应测试供应商409错误、重新加载恢复和空samples/usage；空状态显示暂无监测数据并禁用导出，解除拦截后恢复正常。

只读管理员services.view可查看四种范围与刷新，服务写按钮数量0；直接POST重装403。手机390px实际clientWidth/scrollWidth均390，无横向溢出。已目视检查桌面CPU、手机网络、空状态及只读磁盘截图；截图在 `output/playwright/monitor070-*.png`。其中 `monitor070-mobile-network.png` 实际是磁盘截图，网络应使用 `monitor070-mobile-network-final.png`。

fixture正常结束PASS861.603s，SQL核对财务、库存及账期不变，唯一原始开通任务保留，无新增任务；`browser-fixture.log`。控制台预览暂停产生两次connection refused、模拟409和权限403均已定位；fixture结束后预览一次site500来自已关闭API。浏览器与预览已关闭，fixture schema已清理，独立PG55485已停止，保留可恢复目录；项目三项服务及共享基础设施继续运行。

## 部署与恢复

备份 `bin/previous/20261006T063735Z-nokvm-monitoring/` 包含schema070 dump、四个旧二进制、环境、旧前端tar与实际构建。dump实际恢复到独立 `zjmfgo_monitoring_restore_20261006_test`，旧API就绪/目录读取、新迁移no-op及新API就绪/目录一致均通过；恢复库已清理。76张既有业务/配置表完整行摘要在备份、恢复及部署前后相同，只排除迁移、会话、审计和认证限流，无列排除。环境cmp及备份SHA256通过；部署脚本包含二进制和前端回滚，本批没有实际触发回滚，不能视为全站切换演练。

已发布BUILD_ID `zeEkrYXU4v-phyd1_3HBI`，schema70，API/worker/web均active。root经Next实际登录、服务/权限读取成功，人工服务监控409/monitoring_unavailable，非法范围409/invalid_monitoring_range，均no-store；注销后监控401。现场仍2用户/4商品/2订单/2服务/2账单，NOKVM服务0，没有浏览器测试数据。证据在 `output/nokvm-monitoring-20261006/` 的 `deploy.log`、`live-check.json`、`live-counts.json`、业务摘要及恢复API日志。

## 验证边界

真实供应商未配置，未验证实际采样、供应商本地时区、速率单位和累计统计周期。接入需确认profile的monitoring_timezone，默认Asia/Shanghai只是部署约定。累计流量不代表选定图表范围，不据此收费或自动暂停。VNC、快照/备份及其他平台业务继续待完成，VNC协议障碍见 [NOKVM交付](nokvm-delivery.md)。
