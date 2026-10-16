# 支付异常核验与责任退回浏览器验收

日期：2026-10-17。运行在隔离 PG18 `_test` schema 和真实 TCP HTTP API；支付网关为 loopback fixture，未调用真实商户、退款、短信、资源或生产数据库。浏览器前端基线为 `7d0440a`；后续过期查询恢复补丁 `ecbe2db` 和原键跨页修复 `f7c4f38` 不在本次 18 项/67 张截图的已验收范围内，须以独立专项证据为准。后端验收另覆盖 `c58563b`、`d5ebdfe`、`1f4cbac`、`38996fc`。

关闭状态核验已通过 18 个场景、67 张截图：空态、查询取消、可信 closed 与 paid、unknown/unsupported 冻结、独立确认/驳回、原请求丢回执恢复、同键幂等、409 版本冲突、身份撤权、客户范围隔离、加载/503/重试、URL 分页恢复、损坏 sessionStorage、setItem 失败零 POST，以及 320x568 弹窗底部可达。页面 JavaScript 异常为 0，截图 SHA-256 已在[索引](generated/payment-close-verification-browser-acceptance-20261017.json)核对。

迟到收款责任退回已通过 8 个场景、65 个去重截图路径（73 次捕获中重复路径取最终文件）：退回证据登记、独立确认/驳回、同交易号重复通知去重、新交易号继续冻结、空交易号禁止登记、12组分页、URL刷新恢复、加载/空/503/重试、丢回执原键恢复、403/404/409边界，四尺寸 1440/1024/390/320 和 320短屏均无横向溢出，JavaScript 异常为 0。异常 HTTP 失败均为验收注入或预期权限/版本拒绝，详见 `docs/generated/payment-anomaly-resolution-browser-acceptance-20261017.json`。

首次关闭丢回执脚本曾在弹窗仍打开时等待被 `aria-hidden` 的背景按钮，已排除该次自动化尝试；真实回执已提交且无重复业务写入，后续按先关闭弹窗、再读取原请求的顺序完成 PASS。该排除不代表产品失败。

浏览器测试进程通过 finish 文件正常清理；测试 schema、ready/manifest 和临时前端运行时均已删除。真实 MPAY 的标准查单 404、可信关单/公网通知/实际退款仍是外部待验收条件。
