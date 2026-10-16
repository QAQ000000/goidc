# 合同设置UI批次

独立组件 `contract-settings-admin.tsx` 承担系统设置与模板页签，`financial-settings.tsx` 仅替换合同设置入口；合同生命周期页面及原 useContractWrite 保持原实现。

模板查询真实使用 q/name/content 搜索、status=all|enabled|disabled、limit/offset。URL字段为 template_q、template_status、template_limit、template_page，切筛选和页大小回第一页，25/50/100分页，空越界提供第一页恢复。列表仍保留纯文本正文和六种既有变量，不新增PDF、签章或强制策略假能力。

设置和模板使用专用写hook，事务错误消息原样呈现，409锁住原草稿并提供明确“载入最新并替换草稿”；模板最新直接GET{id}，设置GET单例。新模板幂等冲突无id时，显式核对后放弃被拒绝请求再编辑，不自动新键。确定校验错误保留可编辑输入。

未确认请求存储原path/method/body/key，刷新只显示人工原请求重试；setItem失败在发API前停止写，不降级内存。新存储key：`zjmfgo:contract-settings-write:system:{actor}`、`zjmfgo:contract-settings-write:templates:{actor}`。未保存草稿单独存 `zjmfgo:contract-settings-draft:system:{actor}` / `:templates:{actor}`，由按钮显式恢复。按用户ID重挂载整个面板，隔离draft/dirty/pending；撤权后提交、原请求重试和草稿恢复均禁止，只读没有写按钮。旧 `zjmfgo:contract-write:*` 请求和旧模板草稿不迁移、不静默重发，保留原存储以便单独核对。

浏览器集成步骤（本分支尚未执行）：

- 四宽1440/1024/390/320：两页签、搜索/状态/页大小/多页/URL刷新、零结果与越界恢复，表格仅内部滚动；同时采集pageerror及console.error。
- 创建/编辑模板、取消、只读查看，验证纯文本变量及required/maxLength，无新增native pattern。
- 设置与模板双编辑者409：原草稿仍在，点击加载最新才替换；新模板idempotency_conflict用显式放弃入口恢复。
- 拦截POST/PUT响应模拟结果丢失：记录上述storage key，刷新后无写请求；点击原请求重试逐字节比较path/method/body/key。
- 账号A打开草稿/未确认请求，换B更新/me上下文：面板重挂载，不显示A内容；撤销settings.manage后，无保存/恢复写入口，直接事件触发也被allowed检查拒绝。
- 禁用sessionStorage或令setItem抛错：提示错误且上游API写调用为零。

本批未调用生产写接口、未外发消息、未重启生产。前端静态检查不能替代上述浏览器验收。

静态验收：`npm --prefix web run typecheck` PASS；两个修改组件专项ESLint PASS（0 errors / 0 warnings）；独立worktree `npm run build -- --webpack` PASS，日志 `/tmp/zjmfgo-contract-settings-ui-build.log`。未运行Go检查，未覆盖主项目.next。
