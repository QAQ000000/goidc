"use client";

import Link from "next/link";
import { Boxes, Package, RefreshCw, Search, Settings2, ShieldAlert } from "lucide-react";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { useResource } from "@/hooks/use-resource";
import { ApiError } from "@/lib/api";
import { channelInstanceMode, type ChannelDescriptor } from "@/lib/channel-configuration";
import { AdminPagination } from "./admin-pagination";
import { Failure, Loading } from "./common";
import { Workspace, useAdminPermission } from "./workspace";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

type Category = "payment" | "oauth" | "sms" | "mail" | "bot" | "certification" | "provision";
type AppItem = {
  descriptor: ChannelDescriptor;
  factory_available: boolean;
  instance_count: number;
  enabled_instance_count: number;
  secret_configured_count: number;
  schema_incompatible_count?: number;
  unsupported_instance_count?: number;
  channel_instance_count?: number;
  supplier_account_count?: number;
  profile_count?: number;
  status: "enabled" | "disabled" | "unconfigured" | "implementation_missing" | "configuration_incompatible";
  configuration_url: string;
};
type AppsPage = {
  items: AppItem[];
  total: number;
  limit: number;
  offset: number;
  build: { version: string | null; revision: string | null; modified: boolean | null; go_version: string | null };
};

const categories: { id: Category; label: string }[] = [
  { id: "payment", label: "支付" },
  { id: "oauth", label: "第三方登录" },
  { id: "sms", label: "短信" },
  { id: "mail", label: "邮件" },
  { id: "bot", label: "机器人通知" },
  { id: "certification", label: "实名认证" },
  { id: "provision", label: "供应商开通" },
];
const statuses = ["all", "enabled", "disabled", "unconfigured", "implementation_missing", "configuration_incompatible"] as const;
const statusLabels = { all: "全部状态", enabled: "有已启用实例", disabled: "实例全部停用", unconfigured: "尚未配置", implementation_missing: "实现缺失", configuration_incompatible: "配置不兼容" };
const extraDefaults = { category: "", limit: "25" };
const pageSizes = [10, 20, 50, 100];
const capabilityLabels: Record<string, string> = {
  pay: "支付", payment: "支付", refund: "退款", notify: "回调通知", query: "查询", send: "发送", verify: "验证", login: "登录", authorize: "授权",
  catalog: "产品目录", product: "产品详情", create: "创建服务", sync: "同步服务", power: "电源控制", reset_password: "重置密码", rescue: "救援系统", vnc: "远程控制", reinstall: "重装系统", suspend: "暂停", resume: "恢复", terminate: "删除服务", renew: "续费",
};

export function AdminAppsView({ view }: { view: "configured" | "catalogue" }) {
  const allowed = useAdminPermission("@super");
  const list = useAdminListNavigation(true, statuses, "all", extraDefaults);
  const category = categories.some(item => item.id === list.extra.category) ? list.extra.category : "";
  const pageSize = pageSizes.includes(Number(list.extra.limit)) ? Number(list.extra.limit) : 20;
  const params = new URLSearchParams({ view, status: list.status, limit: String(pageSize), offset: String(list.page * pageSize) });
  if (category) params.set("category", category);
  if (list.query) params.set("q", list.query);
  const resource = useResource<AppsPage>(allowed && list.ready ? `/admin/apps?${params}` : null, false);
  const title = view === "configured" ? "我的应用" : "应用商店";
  const filtered = !!list.query || !!category || list.status !== "all";

  function reset() {
    list.setQuery("");
    list.setDraft("");
    list.setStatus("all");
    list.setExtra({ category: "", limit: String(pageSize) });
    list.setPage(0);
  }

  return <Workspace title={title} admin>
    <div className="min-w-0 space-y-5">
      <div className="admin-page-heading flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0"><h1 className="page-title">{title}</h1><p className="mt-2 text-sm text-muted-foreground">{view === "configured" ? "管理已配置的应用模块，查看实例启用与凭据配置状态。" : "浏览当前版本包含的应用模块，选择模块后前往配置。"}</p></div>
        <Button type="button" variant="outline" size="icon" aria-label="刷新应用列表" title="刷新应用列表" disabled={!allowed} onClick={resource.refresh}><RefreshCw size={16} /></Button>
      </div>
      <nav className="flex min-w-0 flex-wrap gap-5 border-b" aria-label="应用页面">
        {[{ href: "/admin/apps", label: "我的应用", active: view === "configured" }, { href: "/admin/apps/store", label: "应用商店", active: view === "catalogue" }].map(item => <Link key={item.href} href={item.href} aria-current={item.active ? "page" : undefined} className={`border-b-2 px-1 pb-3 text-sm ${item.active ? "border-primary font-medium text-primary" : "border-transparent text-muted-foreground hover:text-foreground"}`}>{item.label}</Link>)}
      </nav>
      {!allowed ? <AccessDenied /> : <>
        <section className="data-surface min-w-0 p-4 sm:p-5">
          <form className="grid min-w-0 items-end gap-3 sm:grid-cols-2 lg:grid-cols-[minmax(180px,1fr)_minmax(130px,180px)_minmax(150px,190px)_auto]" onSubmit={event => { event.preventDefault(); list.setQuery(list.draft.trim()); list.setPage(0); }}>
            <label className="grid min-w-0 gap-1.5 text-xs text-muted-foreground">搜索应用<Input className="min-w-0 text-sm" placeholder="搜索名称、标识或能力" value={list.draft} onChange={event => list.setDraft(event.target.value)} maxLength={200} /></label>
            <label className="grid min-w-0 gap-1.5 text-xs text-muted-foreground">应用类别<select className="h-9 w-full min-w-0 rounded-md border bg-background px-3 text-sm text-foreground" value={category} onChange={event => { list.setExtra({ ...list.extra, category: event.target.value }); list.setPage(0); }}><option value="">全部类别</option>{categories.map(item => <option key={item.id} value={item.id}>{item.label}</option>)}</select></label>
            <label className="grid min-w-0 gap-1.5 text-xs text-muted-foreground">配置状态<select className="h-9 w-full min-w-0 rounded-md border bg-background px-3 text-sm text-foreground" value={list.status} onChange={event => { list.setStatus(event.target.value); list.setPage(0); }}>{statuses.map(status => <option key={status} value={status}>{statusLabels[status]}</option>)}</select></label>
            <div className="flex flex-wrap gap-2"><Button type="submit"><Search size={14} />搜索</Button><Button type="button" variant="outline" onClick={reset}>重置</Button></div>
          </form>
        </section>
        {resource.error instanceof ApiError && resource.error.status === 403 ? <AccessDenied /> : resource.error instanceof ApiError && resource.error.status === 401 ? <section className="data-surface p-8 text-center" role="alert"><p className="mb-4 text-sm">管理员登录已失效，请重新登录。</p><Button asChild><Link href="/admin-login">管理员登录</Link></Button></section> : resource.error ? <Failure error={resource.error} retry={resource.refresh} /> : !resource.data ? <Loading /> : <>
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground"><span>共 {resource.data.total} 个{view === "configured" ? "已配置" : "编译"}模块{filtered ? "符合筛选条件" : ""}</span><span className="min-w-0 break-all">当前构建：{resource.data.build.version || "版本未提供"}</span></div>
          {resource.data.items.length ? <div className="grid min-w-0 gap-4 md:grid-cols-2 xl:grid-cols-3">{resource.data.items.map(item => <AppCard key={`${item.descriptor.category}:${item.descriptor.id}`} item={item} returnHref={`${view === "configured" ? "/admin/apps" : "/admin/apps/store"}${list.suffix}`} />)}</div> : <section className="data-surface min-w-0 space-y-3 px-4 py-12 text-center" role="status"><Boxes className="mx-auto size-9 text-muted-foreground/50" aria-hidden="true" /><h2 className="text-sm font-medium">{filtered ? "没有符合筛选条件的应用" : view === "configured" ? "暂无已配置应用" : "当前构建暂无应用模块"}</h2><p className="text-xs text-muted-foreground">{filtered ? "调整搜索关键词、类别或状态后重试。" : view === "configured" ? "前往应用商店选择模块并完成配置。" : "应用目录随系统版本更新。"}</p>{filtered ? <Button type="button" variant="outline" size="sm" onClick={reset}>清除筛选</Button> : view === "configured" && <Button variant="outline" size="sm" asChild><Link href="/admin/apps/store">浏览应用商店</Link></Button>}{list.page > 0 && <Button type="button" variant="ghost" size="sm" onClick={() => list.setPage(0)}>返回第一页</Button>}</section>}
          <div className="data-surface min-w-0 p-3 sm:p-4"><AdminPagination total={resource.data.total} page={list.page} pageSize={pageSize} pageSizes={pageSizes} onPageChange={list.setPage} onPageSizeChange={size => { list.setExtra({ ...list.extra, limit: String(size) }); list.setPage(0); }} /></div>
        </>}
      </>}
    </div>
  </Workspace>;
}

function AccessDenied() {
  return <section className="data-surface space-y-3 p-8 text-center" role="alert"><ShieldAlert className="mx-auto size-8 text-muted-foreground" aria-hidden="true" /><h2 className="text-sm font-medium">无权访问应用管理</h2><p className="text-xs text-muted-foreground">仅超级管理员可以查看应用目录与配置状态。</p></section>;
}

export function AppCard({ item, returnHref }: { item: AppItem; returnHref: string }) {
  const { descriptor } = item;
  const configuration = new URL(item.configuration_url, "https://admin.local");
  const external = channelInstanceMode(descriptor) !== "channel";
  const canConfigure = (item.factory_available || external || item.status === "implementation_missing") && configuration.origin === "https://admin.local" && ["/admin/channels", "/admin/suppliers", "/admin/products"].includes(configuration.pathname);
  configuration.searchParams.set("return_to", returnHref);
  return <article className="data-surface flex min-w-0 flex-col overflow-hidden">
    <div className="flex min-w-0 items-start gap-3 p-4 sm:p-5">
      <span className="flex size-11 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary"><Package size={24} aria-hidden="true" /></span>
      <div className="min-w-0 flex-1"><h2 className="break-words text-sm font-semibold">{descriptor.name}</h2><p className="mt-1 break-all font-mono text-xs text-muted-foreground">{descriptor.id}</p></div>
      <Badge variant="outline">{categories.find(category => category.id === descriptor.category)?.label ?? descriptor.category}</Badge>
    </div>
    <div className="min-w-0 flex-1 space-y-4 px-4 pb-4 sm:px-5">
      <div className="flex flex-wrap gap-2"><Badge variant="outline" className={item.status === "enabled" ? "border-emerald-200 bg-emerald-50 text-emerald-700" : "bg-muted/50 text-muted-foreground"}>{statusLabels[item.status]}</Badge><Badge variant="outline" className={external ? "bg-muted/50 text-muted-foreground" : item.factory_available ? "border-blue-200 bg-blue-50 text-blue-700" : "border-amber-200 bg-amber-50 text-amber-700"}>{external ? "独立配置入口" : `运行工厂：${item.factory_available ? "已注册" : "未注册"}`}</Badge></div>
      <p className="break-words text-xs text-muted-foreground">模块 {descriptor.version || "版本未提供"} · 配置版本 {descriptor.schema_version ?? "未提供"} · 契约版本 {descriptor.contract_version ?? "未提供"}</p>
      <dl className="grid min-w-0 grid-cols-3 gap-2 rounded-md bg-muted/30 px-3 py-3 text-center">{[["配置实例", item.instance_count], ["已启用", item.enabled_instance_count], ["已配置凭据", item.secret_configured_count]].map(([label, count]) => <div key={String(label)} className="min-w-0"><dt className="text-[11px] text-muted-foreground">{label}</dt><dd className="mt-1 text-lg font-semibold tabular-nums">{count}</dd></div>)}</dl>
      <div className="min-w-0"><h3 className="mb-2 text-xs text-muted-foreground">模块能力</h3><div className="flex min-w-0 flex-wrap gap-1.5">{descriptor.capabilities?.length ? descriptor.capabilities.map(capability => <span key={capability} title={capability} className="max-w-full break-all rounded bg-muted px-2 py-1 text-xs">{capabilityLabels[capability] ?? capability}</span>) : <span className="text-xs text-muted-foreground">未声明扩展能力</span>}</div></div>
      {!!item.schema_incompatible_count && <p className="text-xs leading-5 text-amber-700">{item.schema_incompatible_count} 个实例的配置版本不兼容，需迁移后使用。</p>}
      {!!item.unsupported_instance_count && <p className="text-xs leading-5 text-amber-700">{item.unsupported_instance_count} 个旧渠道配置未接入当前业务入口。<Link className="ml-1 underline" href={`/admin/channels?category=${encodeURIComponent(descriptor.category)}`}>查看并停用旧配置</Link></p>}
      <p className="text-xs leading-5 text-muted-foreground">启用数量不代表配置已验证，连接可用性需在配置入口测试。</p>
      {!item.factory_available && !external && <p className="text-xs leading-5 text-amber-700">{item.status === "implementation_missing" ? "当前构建缺少此实现，保留原配置供核对、停用或删除。" : "当前版本仅包含模块描述，尚未提供运行工厂。"}</p>}
    </div>
    <div className="flex min-w-0 flex-wrap items-center justify-between gap-2 border-t bg-muted/15 px-4 py-3 sm:px-5"><span className="text-xs text-muted-foreground">{descriptor.fields?.length ?? 0} 项配置字段</span>{canConfigure ? <Button variant="ghost" size="sm" asChild><Link href={configuration.pathname + configuration.search}><Settings2 size={14} />{item.instance_count ? "管理配置" : "前往配置"}<span className="sr-only">：{descriptor.name}</span></Link></Button> : <span className="text-xs text-muted-foreground">暂不可配置</span>}</div>
  </article>;
}
