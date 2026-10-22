# 后台前端开发与复用

状态：当前实施规范。页面范围、视觉及业务动作分别遵循[页面目录](frontend-pages.md)、[设计总契约](../admin-frontend-design.md)和对应领域 API；本文规定代码如何复用，不增加业务能力。

## 从已有样板开始

新增常规列表先检查以下组件，不另起近似的表格、分页或整页请求状态框架。

| 组件或模块 | 职责 | 调用方保留的职责 |
| --- | --- | --- |
| `AdminListPage` | 页头、动作区、筛选区、持续反馈及弹窗插槽 | 权限、筛选内容、业务操作和对话框状态 |
| `AdminListRegion` | 数据区标题、加载、刷新、错误、无权限、无效输入及分页位置 | 是否允许读取、响应校验、重试函数和空态含义 |
| `ResourceTable<T>` | 声明式列、稳定行键、表头/单元格样式、表格名称、移动卡片和空态 | 链接、金额、状态文案、业务动作及受控选择 |
| `AdminPagination` | 服务端分页的页码、页大小和跳页交互 | URL 解析、参数白名单和 GET 请求 |
| 领域读取及 mutation | 真实接口、响应类型、身份范围、版本和未知结果恢复 | 不由展示组件猜测 CRUD 接口 |

公共实现位于 [admin-list-page.tsx](../../web/src/components/admin-list-page.tsx)、[resource-table.tsx](../../web/src/components/resource-table.tsx)及[admin-pagination.tsx](../../web/src/components/admin-pagination.tsx)。已有 `ResourceTable` 的 `headers/renderRow` 调用继续兼容；新列表优先使用带稳定 `id` 的 `columns`。

样板一是[客户目录](../../web/src/components/admin-customers.tsx)：严格 URL 查询、高级及自定义字段、分权候选接口、返回上下文和移动卡片。样板二是[人工库存](../../web/src/components/admin-manual-resources.tsx)：库存与占用双状态、严格响应检查、编辑/释放抽屉及未知结果恢复。复制布局后仍须逐项对照领域 API，不复制另一领域的状态含义或写入协议。

## 列表组合示例

下面是可复用的展示组件形态。父级负责取得经校验的数据与权限，并将领域的 URL 导航函数传入分页；组件不发写请求。

```tsx
"use client";

import type { ReactNode } from "react";
import { AdminListPage, AdminListRegion } from "@/components/admin-list-page";
import { AdminPagination } from "@/components/admin-pagination";
import { ResourceTable, type ResourceColumn } from "@/components/resource-table";

type Row = { id: number; name: string };
type Page = { items: Row[]; total: number; limit: number; offset: number };
type Props = {
  allowed: boolean;
  resource: { data: Page | null; error: Error | null; isLoading: boolean };
  filters: ReactNode;
  refresh: () => void;
  navigate: (limit: number, offset: number) => void;
};
const columns: ResourceColumn<Row>[] = [
  { id: "id", header: "编号", render: row => `#${row.id}` },
  { id: "name", header: "名称", render: row => row.name },
];

export function Directory({ allowed, resource, filters, refresh, navigate }: Props) {
  return <AdminListPage title="记录目录" filters={filters}>
    <AdminListRegion title="查询结果" allowed={allowed}
      resource={resource} retry={refresh}
      pagination={page => <AdminPagination total={page.total}
        page={Math.floor(page.offset / page.limit)} pageSize={page.limit}
        onPageChange={index => navigate(page.limit, index * page.limit)}
        onPageSizeChange={limit => navigate(limit, 0)} />}>
      {page => <ResourceTable items={page.items} rowKey={row => row.id}
        columns={columns} ariaLabel="记录目录"
        renderMobile={row => <article className="p-4">#{row.id} · {row.name}</article>}
        empty="当前查询没有记录" />}
    </AdminListRegion>
  </AdminListPage>;
}
```

可选的 `actions`、`feedback`、`dialogs` 分别放页头动作、持续结果和领域弹窗。`AdminListRegion.invalid` 接收非法 URL 或非法响应的就地提示；禁止把非法响应当空数组。`summary` 是真实结果摘要；没有读取权限、请求失败或输入无效时不显示已有数据或旧总数。刷新只可保留相同查询已有的成功数据。

## 成品模板与业务接线

后台外壳采用 [Next Shadcn Dashboard Starter 原生 Radix 版](https://github.com/Kiranism/next-shadcn-dashboard-starter/tree/21848bd2512523f7b1ecd78b217b08e9ea007d5e)，固定 v1.0.0 / `21848bd2512523f7b1ecd78b217b08e9ea007d5e`，不跟随上游自动更新。选用该版可复用当前 Radix、Next、React 和 Tailwind，不引入 Base UI 或更换技术栈。

| 上游源码 | 本地实现及适配 |
| --- | --- |
| `ui/sidebar.tsx`、`ui/sheet.tsx`、`ui/tooltip.tsx`、`ui/dropdown-menu.tsx`、`ui/breadcrumb.tsx`、`ui/separator.tsx`、`ui/card.tsx` | 复用到 `web/src/components/ui/`，Radix独立包改用既有bundle，图标改用既有Lucide，交互文案中文化；骨架宽度固定以适应React纯度检查 |
| `hooks/use-mobile.tsx` | 使用同一768px断点，改为 `useSyncExternalStore` 订阅，适应React19及服务端水合 |
| `app/dashboard/layout.tsx`、`layout/app-sidebar.tsx`、`layout/header.tsx` | [后台layout](../../web/src/app/admin/layout.tsx)使用真实会话和模板SidebarProvider；[AdminShell](../../web/src/components/admin-shell.tsx)使用原Sidebar/Header/账户菜单结构，导航由现有权限配置提供，账户及退出接现有接口 |
| `layout/page-container.tsx`、`ui/heading.tsx` | [AdminPageContainer](../../web/src/components/admin-page-container.tsx)保留页面头和动作插槽，标题使用h1，支持手机换行；外边距统一由外壳管理，去掉演示信息栏及整页请求状态 |
| `styles/themes/vercel.css` | [后台主题](../../web/src/app/admin-dashboard-template.css)采用亮色中性令牌，作用于整个后台及其弹窗；公开站点不使用后台令牌，未提供暗色切换 |

已删除原顶部业务域导航及对应旧外壳样式；业务域统一进入侧边栏折叠菜单，底部账户菜单保留个人设置、官网和退出。展开状态使用独立的 `goidc_admin_sidebar_state` 外观Cookie，不参与认证。手机使用模板Sheet，链接跳转关闭菜单，Escape关闭后焦点回到触发按钮；展开态及手机端不挂载隐藏Tooltip层，避免吞掉Escape。

客户列表、详情使用模板页面容器、卡片和页签展示；常规列表继续组合 `AdminListPage`、`AdminListRegion`、`ResourceTable` 与服务端分页，不另起CRUD或查询框架。已有其他业务页现在使用新外壳，但内部复杂表单和业务交互仍保留领域实现，不能据此外推为全部页面重写完成。

服务列表使用同一公共列表骨架，列定义及手机服务卡片集中在 [AdminServiceList](../../web/src/components/admin-service-list.tsx)，沿用原有查询参数、固定每页20项和轮询；搜索提交及状态变更回第一页，详情返回保留列表查询。状态筛选使用完整受支持状态集合，不同时保留两套近似状态栏。服务详情使用模板页头、Card和五个Radix页签；人工交付、资源凭据、管理动作和结果恢复仍由既有领域组件负责。本批不增加批量续费入口，不将后台页面适配外推为客户前台已重写。

新购订单列表（含共享的供应商订单视图）使用公共列表区域和 [AdminOrderResults](../../web/src/components/admin-order-results.tsx)：桌面归并订单/客户/时间、商品/域名/IP、净应付/付款、订单/审核状态；完整备注、销售及供应商成本快照在逐行展开区，手机改为订单卡片，既有选择和领域动作由父级提供。详情的 [AdminOrderDetailContent](../../web/src/components/admin-order-detail-content.tsx)仅负责模板卡片排版：桌面右侧摘要/建单信息，手机摘要在前；商品配置、折扣快照、关联服务和原备注表单保留。查询、提成请求、审核/取消/删除/延后开票及备注版本协议继续留在领域控制器；续费订单和代下单页不计入本批内页适配。

账单列表沿用公共列表区域及固定每页20项分页，[AdminInvoiceResults](../../web/src/components/admin-invoice-results.tsx)负责账单/客户/来源、金额、状态和到期时间列及手机卡片；导入待复核、支付待确认和关单异常与账单状态分别显示。详情的 [AdminInvoiceDetailContent](../../web/src/components/admin-invoice-detail-content.tsx)只组合模板卡片，桌面右侧摘要和允许操作、左侧项目/资金/异常/外退，手机摘要在前。项目、优惠快照、付款流水和原有财务组件通过领域插槽复用，合并仍使用原版本及幂等请求；客户前台保留原布局。人工账单新建/编辑及发票审核内页不计入账单批次适配。

交易流水沿用原有URL筛选和独立的分页/汇总API，使用公共列表区域、模板Card及 [AdminTransactionResults](../../web/src/components/admin-transaction-results.tsx) 的列定义和手机卡片；按 `event_key` 标识跨来源记录。现金收入/实际外部退款/现金净额直接读取后端全筛选汇总，展开区分别展示网关、人工线下和历史信用还款来源，不把当前页、钱包内转或账单结算当作现金合计。20/50/100条分页、金额转整数分、客户链接权限及详情返回上下文保留；汇总和明细各自失败/重试，无权限时不显示旧数据。本批不增加资金写入或导出。

模板的Clerk、演示数据/API、主题选择、快捷搜索及无对应业务接口的控件不纳入产品。MIT版权全文见 [许可证](../../web/licenses/next-shadcn-dashboard-starter.MIT)。后续页面沿用这些原模板组件和布局，仅补领域数据、权限及操作接线，不再逐页重设计骨架。

## 读取、URL 与权限

- 继续通过现有 `api()` 读取：它自动添加 `/api/v1`、门户、Cookie 和 CSRF 相关标头；客户分页目录是 `/admin/customers/page`，兼容的 `/admin/customers` 只返回数组。
- URL schema、字段白名单、重复参数检查和动态字段类型属于领域。筛选草稿提交后回第一页，浏览器前进/后退及详情返回可恢复同一查询；不把任意组件 filter/sort 配置直接发给 API。
- 服务端分页只呈现本页；不把本页排序或过滤伪装为全量结果。选择和批量操作的范围必须可见且得到 API 支持。
- 当前样板继续使用 `useResource`，保留取消、旧响应保护、按实际管理员及权限/范围重挂和权限控制的候选读取。新查询缓存应另批实现门户、实际身份、权限/范围及查询隔离，且不得持久化敏感缓存。
- 新页面必须区分初次加载、同查询刷新、无数据、筛选无结果、非法查询、401、403及可重试错误。后台401进入 `/admin-login`；持续的授权失败不伪装为空数据。

## 表单与写入

表单值、字段错误和脏状态可以由统一字段或后续的 React Hook Form/Zod 管理；本批未安装这些库。新增表单先复用现有 Input、Label、Select、Dialog 及领域编辑器，字段必须有 label、约束说明、错误关联及提交中状态。不为普通字段再实现一套表单框架。

复杂动态选配、报价和付款确认继续遵循领域协议。字段 schema 只校验输入，不负责权限、服务端金额、CAS 版本、幂等键或请求恢复；空字符串、未传、显式清除及整数分要分别按 DTO 处理。

写入继续调用 `useCustomerMutation`、`useManualResourceMutation` 等领域实现。写前及回执后即时身份检查不得由缓存 `/me` 代替；未知结果保持原 body/key，不因刷新、表单 reset 或新输入解除锁定。密码与敏感请求仅存内存，刷新后的安全核验标记不包含原文。退款、库存释放和服务操作不抽象成通用删除。

## Agent 实施约束与验收

新增常规业务列表使用上述组合；特殊合并表头、汇总或展开行可以保留明确的领域渲染，但需说明为何公共组件不适用。`ui/Table` 等底层原语可用原生表格；筛选草稿、跳页及弹窗允许局部 `useState`。不以禁用 Hook、文件行数或生成速度替代设计审查。

颜色使用已有语义令牌。短暂且已确认的成功提示可以后续接统一 Toast；字段错误、403/409、结果未知和核对按钮必须持续可见，不用自动消失提示代替。

运行 Web lint、typecheck、相关公共和领域组件测试，以及独立目录的生产构建；组件、API、浏览器及部署证据分别登记于[领域验收矩阵](../architecture/contract-verification-matrix.md)。浏览器至少检查桌面/手机、筛选和前进后退、分页、无权限、错误重试以及样板原有写入恢复。公共组件测试必须进入 CI，不能仅依赖字符串搜索或截图。
