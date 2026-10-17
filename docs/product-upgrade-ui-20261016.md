# 商品升级后台UI

独立升级管理组件挂载商品编辑器“升级选项”，URL `section=upgrades` 恢复分区并支持导航变化。保留原编辑器分区snapshot机制；升级目标与配置策略各持独立snapshot，保存一项只刷新自身，不覆盖另一项未保存草稿。其他分区revision变化仍通过显式重新加载核对。

目标名称/ID搜索、真实兼容性/上下架说明、最多100项、降级返还、保存/取消及无匹配状态均使用真实GET结果。manual/simulation可配置；supplier/NOKVM只显示不支持说明，不开放必败配置。后端仍负责拒绝不支持模式。

写前以 `zjmfgo:product-upgrade-write:{actor}:{product}:{targets|configuration}` 保存原path/method/body/key，存储失败停止写。刷新只提示原请求重试；未知结果不能取消或新写。409保留草稿并显示载入最新替换按钮，不自动重放。成功清原请求并单独GET当前策略最新；若成功后读取失败，明确提示已保存并锁定至显式最新，不重复写。取消恢复当前策略snapshot。按身份和products.manage周期重挂载，撤权禁止写/重试，只读可查看搜索及现策略。

未修改客户商品/配置变更组件、经济引擎、Go代码或其他管理分区能力。原独立的未挂载管理组件保持原代码，本次编辑器使用新管理组件以避免客户同文件回归。

浏览器集成步骤（未执行）：

- 四宽1440/1024/390/320进入section=upgrades，刷新/前进后退分区恢复，目标搜索、空、兼容与下架说明、取消。
- 修改目标草稿后保存配置策略：目标草稿保持；保存目标得到409时保留原选择，仅显式最新替换。
- 模拟响应丢失，记录上述key与原body，刷新无自动写，显式重试字节相同；成功后存储清除。
- 身份/权限切换清内存；只读无保存入口，撤权重试被禁。
- supplier/NOKVM不出现可提交配置；同时捕获pageerror及console.error。新组件无native pattern。

本次静态验收：typecheck PASS、两个修改组件专项ESLint PASS（0 warning）、独立webpack build PASS，日志 `/tmp/zjmfgo-upgrade-ui-build.log`。未调用生产API、未重启生产。

## 客户升级账单提交恢复

外层 `ProductChangeWriteBoundary` 通过真实 `/me` 核对customer身份与service.user_id，按actor/service重挂载，不依赖后台身份context。仅商品升级生成账单纳入 `zjmfgo:product-change-submit:{actor}:{service}`，存原path/method/body/key；读取完成前禁新动作，存储失败停止写。刷新无自动写，未知结果锁新报价/提交与其他配置/账期入口；即便历史已显示unpaid，服务配置区外层仍提供原请求回执核对按钮。可关闭弹窗访问外层恢复，不丢pending。确定400/409解除未知锁供重新核对；401/403/408/429保留原请求。成功才清pending并刷新服务。配置和账期自己的提交恢复仍不在本次修改范围。

定向静态验收：typecheck和三文件ESLint PASS（0 warning），按任务边界未重跑build。浏览器需补响应丢失→刷新→历史unpaid仍可原key重放、无/me或他人归属无新动作、unknown期间商品/配置/账期互斥。
