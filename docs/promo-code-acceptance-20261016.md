# 优惠码主页面深度还原验收

状态：本批实现及隔离验收通过；关闭旧规范 #36 已登记的主页面缺口。后端基线 `cffed75`，最终前端 `826878e`；预览提交 `c665f56` 的 web 源码与最终主分支一致。目标仍进行中，本批没有推送、部署或修改运行数据库。

## 页面与接口

依据[还原契约](promo-code-restoration-contract-20261016.md)及旧规范第36页，保留 `/admin/promo-codes`，独立组件为 `web/src/components/admin-promo-codes.tsx`。表格呈现旧九列及明确的状态列，支持全部/有效/过期标签、六种状态筛选、代码/备注搜索、真实分页、URL刷新恢复、新增与取消、启停/日期/次数/备注编辑。

`GET /api/v1/admin/promo-codes/page` 在一个 SQL 语句快照中返回 items/total/limit/offset。状态优先级为过期、停用、未开始、耗尽、有效；一次性优惠使用后也显示耗尽。原 active 和 order_eligible 参数保持兼容。空结果使用真实空列表。

POST/PATCH 接入现有幂等回执，当前权限先于回放核验；同键同参回原结果，同键异参返回 `idempotency_conflict`。PATCH 新增 expected_version，冲突为 `promo_version_conflict`；日期省略保留，null/空串清除，合并后校验起止顺序。迁移147增加版本和日期约束；日期约束 NOT VALID，保留历史非法记录供纠正，后续写入受约束。旧客户端仍兼容省略键/版本，不能声称所有调用方已被强制保护；本页发送两者。编辑不改变既有优惠经济条款和适用条件。

未知响应保存管理员独立的原始 path/method/body/key，刷新不自动重发；显式恢复直接重放原请求。未改日期保留原 RFC3339 小数秒和 offset，不经过 JS Date 截断。版本409保留草稿，显式加载最新逐页按ID查找；新增幂等冲突提供先核对、再放弃原请求重新编辑的入口，不自动分配新键或覆盖。身份变化按ID重新挂载表单；只读员工没有写入口，后端独立拒绝写入。

## 工程检查

| 检查 | 结果与范围 |
| --- | --- |
| 隔离 PG18 强制全量 `source /tmp/zjmfgo-refund-local.env; scripts/test-all.sh` | PASS；HTTP 1022.404s，store 80.283s，legacy-import 104.511s；日志 `/tmp/zjmfgo-promo-full-final.log`，进程退出0 |
| HTTP 专项 race | PASS 74.002s；`Test(PromoRestoration\|PromoCodesPage\|RecurringPromo\|UpgradePromo)`；日志 `/tmp/zjmfgo-promo-race-final.log` |
| Go build / vet | PASS；`/tmp/zjmfgo-promo-build-final.log`、`/tmp/zjmfgo-promo-vet-final.log`；未新增Go变更后无重复全量 |
| 最终前端 typecheck / lint | PASS；lint 0 errors、7条既有 warning；日志 `/tmp/zjmfgo-promo-{typecheck,lint}-final.log` |
| 最终独立 Next webpack build | PASS；`/tmp/zjmfgo-promo-preview-build-verified.log`；生产 `.next` 未覆盖 |
| 截图摘要严格复核 | PASS 37/37；[清单](promo-code-browser-evidence-20261016.json)，不替换其他批次的历史失配证据 |

数据库使用专用 `_test` 数据库中随机 schema。浏览器只连接 loopback Go API 和独立 Next 生产构建，fixture 没有 worker、外发通知、商户资金或供应商资源动作。迁移147已在该隔离环境运行，未应用到业务库。

## 浏览器与持久化证据

| 场景 | 结果 |
| --- | --- |
| 1440/1024/390/320 | PASS；最终构建37张截图覆盖列表、加载、空、读失败、创建弹窗、版本冲突、待确认恢复、新增幂等冲突、只读；整页无横向溢出 |
| 内部滚动与短屏 | PASS；320px表格容器 client294/scroll1120/scrollLeft826；320×640弹窗内部滚动后保存/取消可见可操作 |
| 真实状态/搜索/分页 | PASS；初始35条、五种状态各7条；第二页10条，刷新保留URL；筛选回首页，零结果正确 |
| 新增与取消、日期 | PASS；本地 Asia/Shanghai 日期保存往返一致，清空后数据库为null；取消没有创建记录 |
| 日期精度 | PASS；只改备注后 `2026-01-01 00:00:00.123456+08` / `2030-01-01 00:00:00.654321+08` 保留；单字段非法合并返回400，草稿及原日期保留 |
| 两编辑者与后页最新 | PASS；真实PATCH409保留草稿；插入105条新模糊匹配后，“加载最新”实际请求offset0及100，按原ID取得最新值 |
| 保存已提交但返回503 | PASS；代理先取得真实201，再向浏览器注入503。刷新无自动POST，人工恢复重放原body/key；数据库每个测试码仍只有1行、1次创建审计 |
| 跨时区恢复 | PASS；同一原请求从Asia/Shanghai恢复到Pacific/Honolulu，原body/key完全一致；原窗口再手动重放仍只1行、1次创建审计 |
| 新增幂等冲突 | PASS；同键预先真实创建另一请求，再提交返回409；草稿保留，显式放弃不发POST，后续人工保存才分配新键 |
| 权限与身份 | PASS；promos.view员工无新增/编辑/恢复原管理员草稿；完整CSRF写请求403；切回原管理员仍可显式恢复自己的草稿。原位身份key重挂载另经静态复核，不冒充浏览器热切换验收 |
| 读取错误恢复 | PASS；注入503后“重新加载”恢复真实数据；加载通过暂缓真实响应显式观察 |
| 输入与控制台 | PASS；修复Chrome HTML v模式正则后，实际new RegExp(pattern,'v')可编译，合法含横线/下划线代码通过，非法字符触发patternMismatch；最终捕获pageerror和非预期console.error均为空 |

最终浏览器的503为故障注入，403/409为预期拒绝；早期合并日期400也为预期。早期测试确发现了Chrome原生pattern错误，已在 `826878e` 修复、重建和重新截图；不能用只捕获pageerror的早期记录证明控制台干净。早期脚本的异步等待/日期fill/Fetch.status误用已修正，仅具有最终聚合Result和数据库核对的场景计入通过。

SQL核对还确认 `BROWSER-CANCEL`、`BROWSER-READER-FORBIDDEN`、`BROWSER-FINAL-CANCEL`、`BROWSER-FINAL-READER-FORBIDDEN` 均为0行。具体命令输出保留在 `/tmp/zjmfgo-promo-db-facts-verified.txt`。截图不提交Git，只提交去敏清单及摘要。

收尾：后台退出登录到 `/admin-login`，关闭测试浏览器；fixture PASS 1734.795s，随机schema清理后查询计数0，API34421和Next3093均已停止，仅删除独立预览 `.next`。专用PG18测试cluster保留供后续批次，不影响共享PG和常驻站点。

## 范围与后续

关闭的是 #36 规范主页面的字段、状态、分页和安全交互，不扩大为完整营销规则设计器；已有消费、循环和升级优惠计算继续由后端执行。没有增加删除、经济条款改写或运行时插件。真实生产业务、资金和上线切流不在本批验收内。

105项登记由44完成/61部分调整为45完成/60部分，不是系统整体完成率。下一步按独立契约补 #38 合同设置的模板检索、冲突恢复及身份隔离；PDF/外部签章、商品强制签订策略需另外定义，不添加无效配置项。可信关单、复杂退款、真实外部验收和发布切换仍未闭合。
