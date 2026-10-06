# NOKVM 快照与备份验收

2026-10-06 完成 API、任务、管理员核验及 Next.js 页面接入并部署 schema071。旧 PHP 仅作协议参考；本批没有提取旧数据，也没有开发评论/评价互动。

## 自动验证

证据目录 `output/nokvm-storage-20261006/`，所有数据库测试在独立 PG18 端口55485、`zjmfgo_nokvm_test` 的随机schema运行，测试结束schema已清理。

| 项目 | 结果与证据 |
| --- | --- |
| 全量 Go 强制PG18回归 | PASS；`go-full.log`，HTTP API234.411秒，legacy-import29.787秒；未设置真实旧MySQL演练变量 |
| NOKVM 存储/重装/监控/电源/密码竞态检查 | PASS；`race.log`，HTTP56.206秒，provision1.185秒 |
| 最终全部存储专项含竞态 | PASS；`storage-final.log`，16.100秒；旧失败日志由该最终结果覆盖 |
| vet | PASS；`vet.log` |
| Next.js typecheck/lint | PASS；终端退出0，lint见`frontend-checks.log` |
| 独立 release/preview 生产构建 | PASS；`release-build.log`、`preview-build.log`，未在在线`.next`构建 |

集成测试覆盖六种写操作、同键重放/换目标冲突、6请求并发只产生1任务、精度大于JS安全整数的备份ID、客户归属/目标归属、只读管理员、额度及不可变任务、还原点元数据变更拒绝、通用actions绕过拒绝、风险未确认拒绝、预读期间logout/API key撤销/账户停用/撤权/客户范围及upstream变更、未知与明确拒绝、审计回滚、worker取消/重复执行/已发送租约过期、原购买额度修改保护。恢复期间凭据拒绝，核验后的系统/用户名/新密码更新，VNC保持，财务/账期/库存不变，无密码入审计。

## 真实浏览器

使用Playwright CLI驱动服务器Chrome，本地供应商夹具和独立Next生产预览端口3099。客户、超级管理员和services.view只读管理员分别使用独立浏览器会话。`browser-fixture.log` PASS（575.650秒），最终SQL断言：仅6次外部写指令、6个成功存储任务、0未结束任务；最终系统Debian13/root、核验后密码一致、电源on，VNC/财务/账期/库存保持，任务结果与审计无明文系统密码。

| 流程 | 结果 |
| --- | --- |
| 快照及备份创建/删除/恢复、管理员逐项核验 | PASS，六动作全部实际点击 |
| 未勾风险、错误/缺失服务名称 | PASS，确认按钮不可用 |
| 恢复期间旧密码读取 | PASS，控件禁用且HTTP集成测试409 |
| 两次核验密码不一致 | PASS，显示错误并保持任务待核验 |
| 两次恢复登记Windows/administrator、Debian/root | PASS；客户实际打开凭据弹窗校验新用户名及密码，截图前隐藏密码 |
| 创建备份已受理后丢弃响应 | PASS；页面刷新任务进入待核验，最终外部总写次数6，未重复发送 |
| 只读管理员 | PASS，查看两种列表，没有创建/删除/恢复或凭据按钮 |
| 390px客户列表/删除弹窗/待核验/只读管理员 | PASS，页面无水平溢出，截图检查通过 |

截图在`output/playwright/nokvm-storage-*.png`；其中`restore-risk`、`backup-mobile`、`delete-mobile`、`response-loss-mobile`、`final-mobile`、`reader`和`reader-mobile`保存对应状态。该存储夹具不模拟监控时间序列，因此独立监控面板显示409错误状态；响应丢失测试故意产生一次网络失败。这些控制台消息不代表存储操作失败。实际供应商监控已在前批使用独立监控夹具验收。

## 备份、升级及开发发布

备份 `bin/previous/20261006T072336Z-nokvm-storage/` 保存schema070 dump、四个旧binary、部署env、前端压缩构建及旧`.next`运行目录。SHA256校验通过；真实数据库恢复到独立`zjmfgo_storage_restore_20261006_test`，旧070 API启动/就绪/目录通过，执行071迁移后新API相同检查通过。原业务完整JSON摘要和目录响应一致，恢复库已删除。证据`restored070-*`、`upgraded071-*`、`restore-after.txt`及`release.log`。

开发实例已执行071，四个安装binary SHA与release一致，Next BUILD_ID `cbOQW8HMTanVUkHqagjp9`。API/worker/web active；API healthz/readyz、前端首页200。发布前后所有业务表摘要一致；用户2、商品4、订单2、服务2、账单2，无NOKVM商品/服务或存储测试任务。root真实会话读取身份、目录、服务、权限200，非NOKVM存储访问409，注销200，注销后401；见`live-check.json`。部署env与备份字节一致。

隔离fixture结束后随机测试schema为0，恢复测试库已删除；本批三个浏览器会话关闭，预览3099与独立PG55485停止，最终监听仅本项目开发API8088/Web3088。共享PG5432及旧PHP/MySQL未修改。

已生成存储任务时，发布脚本拒绝退回不识别该动作的旧worker。该批完成备份恢复和向前升级演练，没有执行全站流量切换或有存储任务的逆向回滚。

## 未验证

真实NOKVM未配置，实际磁盘快照、备份、数据恢复与客户机登录均未验证。没有确认的完成状态接口，仍需管理员实地核验；code0不代表完成。VNC安全授权/代理、流量计费、其他供应商及其他重构台账继续待完成。整体重构未完成，当前实例仍用于开发。
