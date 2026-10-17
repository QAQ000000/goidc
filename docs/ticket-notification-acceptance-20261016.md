# 工单公开事件通知集成验收（2026-10-16）

状态：store/HTTP/UI已在独立分支提交并集成，四尺寸页面与专项/race通过。邮件渲染边界补正后的最终全量回归通过；真实邮箱/手机收件未验收。总体核心替代目标仍进行中。

## 已实现范围

迁移145增加版本化本人工单邮件/SMS偏好、稳定工单事件、收件决策和创建/回复幂等回执。客户创建及客户公开回复通知当前合法客服；合法assignee优先，无合法assignee时选择启用、角色有效、具备tickets.manage且可访问工单部门的客服，排除操作人，NULL部门按既有部门权限判断。客服公开回复通知主客户及启用且订阅support_emails的联系人，联系人仅邮件。

主客户和客服邮件默认订阅但要求已验证邮箱；SMS默认关闭且要求已验证规范E.164手机号；停用收件人明确跳过。联系人沿用管理员维护及support_emails的信任规则，没有独立邮箱验证流程；不能把内部address_verified快照称为联系人已完成邮箱验证。

每次公开事件以ticket/reply稳定ID生成唯一键，回复INSERT RETURNING取得ID。同事件每身份每渠道只决策一次，规范化地址重复记录duplicate_of，不再排第二份。保存user/contact/所属客户、地址、偏好/模板/渠道版本及渲染正文快照。无模板、渠道、退订或无效渲染内容明确skipped，不阻断工单。DB、审计或加密故障仍整体回滚工单、版本、事件和outbox，不在持锁事务内外发。

邮件渲染后校验收件地址、主题长度/头字段和正文上限；合法240字节工单主题展开超长或长模板展开超过outbox上限时，记invalid_email_payload，不截断内容、不让工单500。短信按原队列大小和号码规则判断，idcsmart缺供应商模板ID明确跳过。新模板默认停用，启用需管理员配置；模板只使用当前公开正文和可信origin的固定console/admin链接，不读取备注或转交原因。

创建/回复支持header或body幂等键，同键同参返回原记录，异参409；兼容无键旧客户端，但不承诺其未知结果安全重放。重放前复核当前会话、归属及部门；工单已合并、owner变化或撤权后不返回原结果。已成功回复即使工单后来关闭，合法原作者仍可确认原reply ID。

迁移146为管理员代建的既有ticket_created邮件记录真实ticket_id，保留原模板、显式CC、去重和发送次数，不再新增一份公开创建通知。日志、旧email/SMS outbox列表与retry共用实时工单权限和部门范围；查看需tickets.view，重试需tickets.manage，并保留原通知权限。无法可信归属的历史工单邮件只允许启用的super_admin读取/重试，不从主题猜测回填。

接口为`GET/PATCH /api/v1/me/ticket-notification-preferences`（session-only、expected_version）和`GET /api/v1/tickets/{id}/notifications`（items/total/limit/offset）。摘要区分queued/skipped业务事实与实际outbox_status；客户只见本人user决策、不见其他收件地址或内部备注，管理员按实时部门查看。日志提供ticket_id/event_key/收件身份追溯。本人inbox、联系人短信、转交/关闭通知、员工代改他人偏好不在本增量。

## 检验结果

| 检查 | 结果和范围 |
| --- | --- |
| Go build/vet | PASS，邮件payload最终源码dc6716f再次build/vet通过 |
| 全量隔离PG18 | 最终后端dc6716f全量PASS：HTTP1297.311s、store107.949s、legacy-import136.913s；此前fd882fa也曾全量通过，最终结果优先 |
| 集成race | `fd882fa`相关store4.809s、HTTP29.039s PASS；包含同键并发、撤权/越部门、代建邮件和旧通知回归；邮件payload补正后最终专项PASS，store14.780s、HTTP75.919s |
| 本地传输 | TestTicketNotificationLoopbackTransport PASS race2.537s，公开回复→真实outbox→compiled SMTP loopback / HTTP SMS→Complete→sent；核对正文、原渠道/模板数据及客户console链接 |
| 事务与权限 | 隔离PG覆盖创建/回复回执、四并发同键、不同内容冲突、无模板/退订/验证/停用/无部门、合法assignee优先、联系人地址重复、审计/事件/加密失败回滚；日志及旧列表/retry均覆盖部门撤销和仅有通知权限的员工 |
| 前端 | typecheck/lint通过，0errors、7条既有warnings；隔离worktree webpack生产构建通过；邮件payload新增中文提示后的最终构建亦通过（preview8b33461，前端内容对应7315628） |
| 偏好浏览器 | 真实默认值、取消、保存、刷新持久化、真实409草稿保留并显式加载新版本后保存；1440/1024/390/320无页面整体溢出 |
| 创建与回复 | 真实写入后拦截返回503；原payload/key/version按用户隔离保存在当前标签页，刷新后只允许确认原请求。同一created ticket2，reply1同键返回；闭单后仍可重放reply7，确认后禁止新回复 |
| 工单页面与分页 | 客服公开回复与真实摘要四尺寸；本地sink处理后显示sent；超过20条真实翻页；客户摘要四尺寸仅本人记录且地址隐藏；注入503后刷新恢复 |
| 备注与订阅 | 内部备注前后摘要12条不变；最终DB退订联系人outbox0、备注泄漏0；未将备注计入任何公开模板 |
| 最终fixture数据库 | ticket2 closed/version9，reply7条、event8条、receipt8条（create1、reply7）；相同主题创建仅1条。关闭通过隔离DB模拟另一端操作，不称本次浏览器按关闭按钮 |
| 截图与收尾 | 28/28摘要匹配；fixture PASS2320.781s，已界面退出/关闭浏览器，停止独立Next/API并删除该preview构建；专用PG集群仍供供应商下一批隔离验收使用 |

[浏览器证据](ticket-notification-browser-evidence-20261016.json)记录具体脚本结果、原始DB核对和运行边界。截图保留忽略目录`output/playwright/ticket-notifications-20261016/`；摘要匹配只证明文件完整，不代表重新执行了全部交互。

```sh
node scripts/check-admin-browser-evidence.mjs --manifest docs/ticket-notification-browser-evidence-20261016.json --strict
```

浏览器API基线为82fd509，146代建邮件归属及后续渲染边界由独立PG/HTTP/race与最终全量验证，未把旧fixture当作这两个后端修复的浏览器证据。最终页面四尺寸截图使用eb16a30同内容构建；后续仅新增invalid_email_payload中文说明，构建另验。

## 发现与修正

主审修复客户链接误写为/tickets、停用客户未跳过、idcsmart缺模板；前端补未知回复在closed后仍能确认、工单版本变化更新摘要、真实outbox状态及中文原因。独立只读复核确认代建邮件无工单归属的权限漏洞，补迁移146及历史无归属的默认拒绝；修复后独立复核通过。最终额外发现渲染后超长邮件会阻断业务，已补skipped分支和PG回归，最终全量亦通过。

一轮早期全量因上述权限修复主动终止（exit143），不记为通过；后续fd882fa全量通过。loopback传输首次因fixture同时修改phone与phone_verified被原触发器清除验证标志，且未配置SMS实例而无任务；分步验证并创建仅本地测试实例后通过，没有改变产品验证规则。

首次preview缺运行时API_INTERNAL_URL，服务端读请求回退默认服务而重定向；显式设置独立API/WEB origin后验收，未修改业务配置。首次CLI把heading引用当input失败后纠正；create脚本在beforeunload处未返回聚合JSON，由截图、同键原单及数据库回执分别核对，没有补造原脚本Result。回复恢复采用独立两段脚本，有原请求和原reply ID的完整结果。

## 未验收边界

本地SMTP/HTTP和sink不证明真实第三方收件。sent表示原队列处理结果，不等于用户已读；网络已发送但确认丢失仍可能重送，不承诺外部exactly-once。新链路真实商户配置与邮箱/手机号收件须独立验证，不复用旧验收推断成功。

未在浏览器逐项构造所有受限员工/撤权、模板异常、慢响应及代建归属场景；对应HTTP/PG断言与运行证据分列，不称105页全状态验收完成。未部署、推送、改业务库或重启生产进程。105项44完成/61部分登记不因本批局部能力补齐自动改数。
