# 后台配置批次验收

日期：2026-10-07。schema084/085，Go API与worker，Next.js生产构建。业务契约及后续配置缺口见 [后台配置](admin-configuration.md)。

- **PASS**：独立PG18端口19487，全量强制数据库`go test ./...`，HTTP API267.234s、旧导入42.019s；后续窄改另跑SMTP/预设/支付专项5.348s。
- **PASS**：支付、SMTP、通知和预设竞态测试；SMTP和分类删除最终改动再跑race。`go vet ./...`和diff-check通过。
- **PASS**：typecheck、lint、webpack生产构建。lint保留两条已有img警告。
- **PASS**：Playwright生产构建连接真实API及隔离schema，支付和SMTP连续保存、留空保留秘密、刷新不回显；数据库确认密文、版本和支付审计。
- **PASS**：模板真实POST，分类新增/排序/改名/停用，回复新增/改名/停用，空分类删除。最终接口加锁和非空409，防止级联删除回复。
- **PASS**：最终生产构建复跑隔离浏览器夹具286.391s，支付/SMTP密文与版本、审计、模板、停用分类及回复全部SQL断言通过；分类改名同步刷新回复列表。
- **PASS**：普通管理员不显示凭据导航，对应API403、页面重定向；退出后`/me`401。
- **PASS**：四页在1440/390/320宽度无全页横向溢出，截图为`output/playwright/configuration-*.png`；实际查看支付手机和SMTP桌面截图。表格内部滚动。
- **PASS**：修复浏览器模板类型pattern的Unicode v语法，新增数字PID校验；最终构建复核有效类型和无效PID。
- **PASS**：开发实例85条迁移，支付/SMTP均`configured=false,version=1`且无密文，没有写入本批测试凭据；三项服务、healthz/readyz与登录页正常。

没有真实收费、SMTP投递或供应商写动作。本批验证配置接通，外部端到端测试另行执行。NOKVM账号管理、任务策略、SMTP试发/其他TLS模式和多事件通知仍待完成。

首次夹具种子登录Origin错误，修正后通过；非数字测试PID被正确拒绝。最终夹具一次漏执行预设步骤导致断言失败，补全后复核；中途结束预览留下测试schema，已明确清理。构建前停止web并在完成后恢复，不创建额外构建备份；预览和隔离集群在结束后停止。
