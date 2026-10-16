# 客户自定义字段后台还原验收（2026-10-16）

状态：本批后端、前端、HTTP 专项、隔离全量及浏览器有限范围验收完成；未部署、未推送。旧版 #28 由“部分完成”继续保持部分完成，原因是本批只关闭后台配置恢复缺口，不重写注册公开字段或客户资料域。

## 实现范围

依据[设计契约](customer-field-admin-restoration-contract-20261016.md)保留 `customers.manage` 配置权限和六种字段类型，新增显式字段详情 GET、15键 DTO、`no-store`、q 最多100个 Unicode 码点的字面搜索、查询参数白名单、同一 Repeatable Read 快照的 count/items、稳定排序及字段标识不可重命名的409保护。停用或删除不清理客户已有 `custom_fields` JSON；注册投影继续沿用已验收的公开白名单。

前端使用 `/admin/customer-fields` 稳定路由，筛选和分页写入 URL，空结果与越界页分开提示；列表展示注册标志与实际公开原因。新增、修改、删除均使用真实 API、版本和幂等键。草稿按员工隔离，输入先持久化；发送前实时核验 `/me`，未知回执保留原 method/path/key/body，删除后目标404仍可用原请求核实。409需重读并显式结束，不自动换键。实时身份失败时隐藏配置并保留恢复资料；清理被拒请求、丢弃或替换草稿前再次核验身份并与写请求互斥。

实现提交：后端 `f828f1b`，HTTP 专项与 fixture `49bd0f5`，前端 `33c3226`、身份修复 `76c1803`、草稿同步与清理修复 `894a266`、Navigation API 离页保护 `c6b2b8f`。均由独立 `gpt-6.1-sol` worktree 提交后主代理审查集成。

## 验证结果

| 检查 | 结果 |
| --- | --- |
| Go build / vet | PASS，主树集成后通过 |
| 客户字段及注册相关 race | PASS，新增5项及既有6项顶层回归；同快照25次采样，13次total=1、12次total=21，42次真实INSERT/DELETE提交，items长度始终等于total |
| HTTP 权限与恢复 | PASS：匿名401、view-only配置403、manage可读写、详情白名单/no-store、字面 `%`/`_`/反斜杠、100/101 Unicode、非法UTF-8/NUL、重复参数、类型/状态校验、版本/标识/幂等冲突、三类原请求及删除后重放、权限/会话撤销 |
| 强制隔离PG18全量 | PASS，集成基线 `49bd0f5`，HTTP1106.776s、store85.969s、legacy-import97.790s，总1158.03s，exit0；未跳过数据库 |
| 前端 typecheck / lint | PASS，0 error，7条既有 `<img>` warning |
| Next webpack build | PASS，主批最终提交 `c6b2b8f` 在隔离 worktree 使用 `npm run build --prefix web -- --webpack` 通过；未覆盖常驻 `.next` |
| 浏览器 fixture | PASS，真实62字段、六类型、内部/隐藏/停用冲突标志、53条分页字段及 manager/view-only 账户，34.811s smoke；最终 fixture 2129.094s，结束后精确清理本批 schema，剩余0 |
| 浏览器页面 | PASS（有限范围）：真实 API 列表、分页、冲突标志、添加草稿、员工隔离 sessionStorage、刷新恢复、取消确认、详情15键/no-store；1440/1024/390/320各3张，共12张严格摘要12/12，见[证据索引](customer-field-admin-browser-evidence-20261016.json) |

HTTP 专项和 fixture 使用随机 `_test` schema，不访问生产库或外部服务；结束后 `pg_stat_activity` 无本批连接、schema 数量为0。浏览器截图摘要只证明文件完整及当前布局采样，未把未实际完成的 UI 保存/删除点击回执当作浏览器 PASS；其真实 API、事务、版本、幂等及权限边界由 HTTP/race 覆盖。Navigation API 的浏览器返回拦截已集成，当前 Chromium 运行环境完成构建检查；不支持该 API 的旧浏览器仍依赖 `beforeunload`，资料本身会保留，正式兼容矩阵需另验。

本批没有真实通知、商户、供应商或业务库写入，不部署、不推送、不切流。保存/删除的真实事务与回执由 HTTP/race 专项覆盖；浏览器页面本轮保留草稿、读取与取消证据，后续可补齐保存/删除动作的四尺寸截图，再推进台账其他部分完成项。
