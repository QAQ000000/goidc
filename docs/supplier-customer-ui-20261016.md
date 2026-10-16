# 供应商客户运维 UI

客户与后台共用服务运维组件，分别请求 `/services/{id}` 与 `/admin/services/{id}` 下的动作、密码、镜像、重装、VNC 接口。服务详情的 `supplier_capabilities` 是唯一能力来源；字段缺失时不展示动作，提示刷新。客户始终不展示或提交 create/sync，也不进入供应商账户或上游配置管理。

电源动作按 power 能力显示；硬关机、硬重启要求风险确认。救援按 rescue 能力显示，需要选择系统并确认临时密码；取消或成功后清空密码。重装沿用真实镜像接口、服务名称校验及数据丢失确认；密码仅保存在组件内存，不写日志或 sessionStorage。VNC 使用后端返回的短期 URL，仅接受 HTTP/HTTPS，并隔离 opener。

任务进行中或结果待核验时暂停其他操作；错误后刷新状态，不自动重发。动作保留既有同参数幂等键，结果待核验由管理员核验流程处理。客户遇到共享账户阻塞时只显示联系管理员，不提供其他客户服务链接。

本次静态验收通过：`npm run typecheck`、`npm run lint`（0 errors，7 条既有 warning）、独立 worktree 的 `npm run build -- --webpack`。主分支已完成四尺寸弹窗/取消、VNC和未知冻结首增量检查，实际成功与未验收动作详见[集成验收](supplier-customer-acceptance-20261016.md)。未调用真实供应商，未写真实资源，未重启生产进程。
