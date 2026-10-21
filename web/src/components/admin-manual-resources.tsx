"use client";

import { useState } from "react";
import Link from "next/link";
import { Plus, RefreshCw, Server } from "lucide-react";
import { date, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { Workspace, useAdminIdentity, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { AdminPagination } from "./admin-pagination";
import { manualResourceAuthority, useManualResourceMutation, validManualResource, type ManualResource, type ManualResourceRequest } from "./admin-manual-resource-mutation";

type Page = { items: ManualResource[]; total: number; limit: number; offset: number };
type Fields = { name: string; resource_type: string; provider: string; region: string; address: string; upstream_id: string; cost: string; expiry: string; status: string; notes: string; username: string; password: string; changeUsername: boolean; changePassword: boolean };
type Drawer = { kind: "create" } | { kind: "detail" | "edit" | "release"; id: number };
type Recovery = { resource: ManualResource | null; body: Record<string, unknown> };
const statuses = ["available", "disabled", "retired"];
const labels: Record<string, string> = { available: "可用", disabled: "停用", retired: "已退役" };
const defaults = { binding: "", limit: "25" };
const selectClass = "h-10 w-full rounded border bg-background px-3 text-sm";
const blank = (): Fields => ({ name: "", resource_type: "server", provider: "", region: "", address: "", upstream_id: "", cost: "0.00", expiry: "", status: "available", notes: "", username: "", password: "", changeUsername: false, changePassword: false });
const money = (cents: number) => { const exact = BigInt(cents); return `¥${exact / BigInt(100)}.${String(exact % BigInt(100)).padStart(2, "0")}`; };
const can = (actor: User, permission: string) => actor.role === "super_admin" || actor.role === "admin" && actor.permissions.includes(permission);
const allCustomers = (actor: User) => actor.role === "super_admin" || actor.customer_scope === "all";
function fromResource(resource: ManualResource): Fields {
  const shanghai = resource.upstream_expires_at ? new Date(new Date(resource.upstream_expires_at).getTime() + 8 * 3600000).toISOString().slice(0, 16) : "";
  return { ...blank(), ...resource, cost: `${Math.floor(resource.cost_cents / 100)}.${String(resource.cost_cents % 100).padStart(2, "0")}`, expiry: shanghai };
}
function recoveryFields(resource: ManualResource | null, body?: Record<string, unknown>): Fields {
  const fields = resource ? fromResource(resource) : blank();
  if (!body) return fields;
  for (const key of ["name", "resource_type", "provider", "region", "address", "upstream_id", "status", "notes"] as const) if (typeof body[key] === "string") fields[key] = body[key];
  if (typeof body.cost_cents === "number") fields.cost = `${Math.floor(body.cost_cents / 100)}.${String(body.cost_cents % 100).padStart(2, "0")}`;
  if (typeof body.upstream_expires_at === "string") fields.expiry = body.upstream_expires_at ? new Date(Date.parse(body.upstream_expires_at) + 8 * 3600000).toISOString().slice(0, 16) : "";
  return fields;
}
export function manualResourceCost(value: string) {
  if (!/^\d+(?:\.\d{1,2})?$/.test(value)) throw new Error("成本须为非负金额，最多两位小数。");
  const [whole, fraction = ""] = value.split(".");
  const cents = BigInt(whole) * BigInt(100) + BigInt(fraction.padEnd(2, "0"));
  if (cents > BigInt("9000000000000000")) throw new Error("成本超出允许范围。");
  return Number(cents);
}
function prepareFields(fields: Fields, resource: ManualResource | null) {
  const max: Record<string, number> = { name: 120, resource_type: 40, provider: 120, region: 120, address: 256, upstream_id: 256, notes: 4000 };
  const body: Record<string, unknown> = {};
  for (const [field, length] of Object.entries(max)) {
    const value = fields[field as keyof Fields] as string, normalized = value.trim();
    if (Array.from(normalized).length > length || normalized.includes("\0")) throw new Error(`${field}字段长度或内容无效。`);
    body[field] = normalized;
  }
  if (!body.name || !body.resource_type || /\s/.test(String(body.address))) throw new Error("请填写名称、资源类型，地址不能包含空白。");
  body.cost_cents = manualResourceCost(fields.cost);
  if (fields.expiry && (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(fields.expiry) || Number(fields.expiry.slice(0, 4)) < 2000 || Number(fields.expiry.slice(0, 4)) > 2200 || !Number.isFinite(Date.parse(`${fields.expiry}:00+08:00`)))) throw new Error("请填写2000至2200年的有效上游时间。");
  body.upstream_expires_at = fields.expiry ? `${fields.expiry}:00+08:00` : "";
  if (!statuses.includes(fields.status)) throw new Error("库存状态无效。");
  body.status = fields.status;
  if (resource?.status === "retired" && fields.status !== "retired") throw new Error("退役库存不可恢复。");
  if (resource?.binding_id) {
    for (const key of ["name", "resource_type", "provider", "region", "address", "upstream_id", "status"]) delete body[key];
  } else {
    if (fields.changeUsername) body.username = fields.username.trim();
    if (fields.changePassword) body.password = fields.password;
    if (new TextEncoder().encode(String(body.username ?? "")).length > 256 || new TextEncoder().encode(String(body.password ?? "")).length > 4096) throw new Error("账号最多256字节，密码最多4096字节。");
  }
  if (resource) {
    body.expected_version = resource.version;
    // Send only changed public metadata. Credentials are explicit write-only updates.
    for (const key of Object.keys(body)) {
      if (["expected_version", "username", "password"].includes(key)) continue;
      if (key === "upstream_expires_at") {
        const before = resource.upstream_expires_at;
        if (fields.expiry === fromResource(resource).expiry || (!before && !body[key]) || before && body[key] && Date.parse(before) === Date.parse(String(body[key]))) delete body[key];
      } else if (body[key] === resource[key as keyof ManualResource]) delete body[key];
    }
    if (Object.keys(body).length === 1) throw new Error("没有需要保存的修改。");
  }
  return body;
}

export function AdminManualResourcesView() {
  const actor = useAdminIdentity(), view = useAdminPermission("resources.view");
  return <Workspace title="人工库存" admin>{!actor || !view ? <p role="status" className="p-5 text-sm">无权限查看人工库存。</p> : <ManualResourceWorkspace key={manualResourceAuthority(actor)} actor={actor} />}</Workspace>;
}
function ManualResourceWorkspace({ actor }: { actor: User }) {
  const nav = useAdminListNavigation(true, statuses, "", defaults);
  const mutation = useManualResourceMutation(actor);
  const manage = can(actor, "resources.manage"), full = allCustomers(actor);
  const [drawer, setDrawer] = useState<Drawer | null>(null), [error, setError] = useState(""), [success, setSuccess] = useState("");
  const [recovery, setRecovery] = useState<Recovery | null>(null), [revision, setRevision] = useState(0);
  const binding = ["bound", "unbound"].includes(nav.extra.binding) ? nav.extra.binding : "";
  const limit = [10, 25, 50, 100].includes(Number(nav.extra.limit)) ? Number(nav.extra.limit) : 25;
  const params = new URLSearchParams({ limit: String(limit), offset: String(nav.page * limit) });
  if (nav.query) params.set("q", nav.query); if (nav.status) params.set("status", nav.status); if (binding) params.set("binding", binding);
  const resource = useResource<Page>(nav.ready ? `/admin/manual-resources?${params}` : null, false);
  const page = resource.data && Array.isArray(resource.data.items) && resource.data.items.every(validManualResource) && Number.isSafeInteger(resource.data.total) ? resource.data : null;
  function open(next: Drawer) { if (!mutation.pending) { setRecovery(null); setDrawer(next); setSuccess(""); } }
  function created(id: number) { resource.refresh(); setRecovery(null); setDrawer({ kind: "detail", id }); setSuccess("操作回执已确认；列表和详情正在重新读取当前事实。释放回执不表示资源当前空闲。"); }
  function close() { if (mutation.busy) return; if (drawer && ["create", "edit"].includes(drawer.kind) && !mutation.pending && !window.confirm("确认关闭编辑？未保存的输入将丢失。")) return; setDrawer(null); }
  return <div className="min-w-0 space-y-5">
    <header className="flex flex-wrap items-start justify-between gap-3"><div><h1 className="page-title">人工库存</h1><p className="mt-2 text-sm text-muted-foreground">登记独立资源，通过服务的人工交付流程绑定。库存状态与占用分别管理。</p></div><div className="flex gap-2"><Button variant="outline" onClick={resource.refresh}><RefreshCw size={15} />刷新库存</Button>{manage && full && <Button disabled={mutation.frozen} onClick={() => open({ kind: "create" })}><Plus size={15} />新建库存</Button>}</div></header>
    <nav aria-label="资源视图" className="flex flex-wrap gap-2"><Button asChild variant="outline"><Link href="/admin/resources">已购服务资源</Link></Button><Button aria-current="page">人工库存</Button></nav>
    {!full && <p className="text-sm text-muted-foreground">当前客户范围仅可查看范围内已占用库存；未分配库存不可见，不能新建。</p>}
    <form className="flex flex-wrap items-end gap-3 rounded border bg-card p-4" onSubmit={event => { event.preventDefault(); const q = nav.draft.trim(); if (Array.from(q).length > 120) { setError("关键词最多120字。"); return; } setError(""); nav.setQuery(q); nav.setPage(0); }}>
      <label className="min-w-0 flex-1 basis-64 space-y-2 text-sm"><span>关键词</span><Input aria-label="搜索人工库存" value={nav.draft} onChange={event => nav.setDraft(event.target.value)} placeholder="名称、地址、上游标识或提供方" /></label>
      <label className="space-y-2 text-sm"><span>库存状态</span><select aria-label="库存状态筛选" className={selectClass} value={nav.status} onChange={event => { nav.setStatus(event.target.value); nav.setPage(0); }}><option value="">全部状态</option>{statuses.map(status => <option key={status} value={status}>{labels[status]}</option>)}</select></label>
      <label className="space-y-2 text-sm"><span>占用</span><select aria-label="库存占用筛选" className={selectClass} value={binding} onChange={event => { nav.setExtra({ ...nav.extra, binding: event.target.value }); nav.setPage(0); }}><option value="">全部占用</option><option value="bound">已占用</option><option value="unbound">未占用</option></select></label><Button type="submit">查询</Button><Button type="button" variant="outline" onClick={() => { nav.setDraft(""); nav.setQuery(""); nav.setStatus(""); nav.setExtra(defaults); nav.setPage(0); }}>重置</Button>
    </form>
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}{success && <p role="status" className="text-sm text-primary">{success}</p>}
    {mutation.error && <p role="alert" className="break-words text-sm text-destructive">{mutation.error}</p>}
    {mutation.pending && <section className="space-y-3 rounded border bg-muted/30 p-4 text-sm"><h2 className="font-semibold">{mutation.pending.phase === "rejected" ? "原库存请求已拒绝" : "原库存请求结果待核验"}</h2><p>{mutation.pending.kind === "create" ? "创建库存" : `库存 #${mutation.pending.target}`} · 请求键 {mutation.pending.key}</p>
      {!mutation.pending.body && <p>原请求含只写凭据，刷新后仅保留安全标记，无法自动重试。请联系管理员核对原请求键与库存事实，当前账户的新操作已阻止。不要清空标记后重新创建。</p>}
      {mutation.pending.phase === "uncertain" && mutation.pending.body && <Button variant="outline" disabled={mutation.busy || !manage} onClick={() => void mutation.run().then(result => { if (result) created(result.id); })}>使用原库存请求重试</Button>}
      {mutation.pending.phase === "rejected" && <><Button variant="outline" disabled={mutation.busy || !manage} onClick={() => void mutation.review()}>读取当前库存事实</Button>{mutation.facts && <div className="space-y-3"><p>{mutation.facts.resource ? `当前版本 ${mutation.facts.resource.version} · ${labels[mutation.facts.resource.status]} · ${mutation.facts.resource.binding_id ? `占用服务 #${mutation.facts.resource.service_id}` : "未占用"}` : `当前有权库存 ${mutation.facts.total} 条；原创建已被确定拒绝。`}</p><Button variant="outline" disabled={mutation.busy || !manage} onClick={() => {
        const original = mutation.pending;
        if (original && window.confirm("已核对当前事实，确认结束被拒绝的原请求并重新编辑？")) void mutation.endRejected().then(facts => { if (facts) {
          const body = original.body ? JSON.parse(original.body) as Record<string, unknown> : {};
          delete body.username; delete body.password;
          resource.refresh(); setRecovery({ resource: facts.resource, body }); setRevision(value => value + 1);
          setDrawer(facts.resource ? { kind: original.kind === "release" ? "release" : "edit", id: facts.resource.id } : { kind: "create" });
        } });
      }}>已核对，结束旧请求并重新编辑</Button></div>}</>}
    </section>}
    {resource.error ? <ReadFailure error={resource.error.message} retry={resource.refresh} /> : !resource.data || resource.isLoading ? <p role="status">正在读取人工库存…</p> : !page ? <ReadFailure error="库存列表响应无效，请重新读取。" retry={resource.refresh} /> : <>
      {!page.items.length ? <p className="rounded border p-10 text-center text-sm text-muted-foreground">暂无符合条件的人工库存。</p> : <><div className="hidden overflow-x-auto rounded border lg:block"><Table className="min-w-[1200px]"><TableHeader><TableRow>{["资源 / 类型", "地址 / 上游标识", "提供方 / 区域", "库存状态", "占用", "成本 CNY", "上游到期", "凭据 / 版本", "操作"].map(text => <TableHead key={text}>{text}</TableHead>)}</TableRow></TableHeader><TableBody>{page.items.map(item => <TableRow key={item.id}><TableCell><strong>{item.name}</strong><p className="text-xs text-muted-foreground">#{item.id} · {item.resource_type}</p></TableCell><TableCell className="max-w-48 whitespace-normal break-all">{item.address || "未填写地址"}<p className="text-xs text-muted-foreground">{item.upstream_id || "未填写标识"}</p></TableCell><TableCell>{item.provider || "—"}<p className="text-xs text-muted-foreground">{item.region || "—"}</p></TableCell><TableCell>{labels[item.status]}</TableCell><TableCell><Binding resource={item} actor={actor} /></TableCell><TableCell className="tabular-nums">{money(item.cost_cents)}</TableCell><TableCell>{item.upstream_expires_at ? date(item.upstream_expires_at) : "未设置"}</TableCell><TableCell>{item.has_credentials ? "已配置" : "未配置"}<p className="text-xs text-muted-foreground">版本 {item.version}</p></TableCell><TableCell><RowActions item={item} manage={manage} disabled={!!mutation.pending} open={open} /></TableCell></TableRow>)}</TableBody></Table></div>
      <div className="space-y-3 lg:hidden">{page.items.map(item => <article key={item.id} className="space-y-3 rounded border bg-card p-4"><h2 className="flex items-center gap-2 font-semibold"><Server size={16} />{item.name} · #{item.id}</h2><p className="break-all text-sm">{item.address || "未填写地址"} · {item.resource_type}</p><p className="text-sm">库存：{labels[item.status]} · {item.binding_id ? "已占用" : "未占用"}</p><Binding resource={item} actor={actor} /><p className="text-sm">成本 {money(item.cost_cents)} · 上游到期 {item.upstream_expires_at ? date(item.upstream_expires_at) : "未设置"}</p><p className="text-xs text-muted-foreground">凭据{item.has_credentials ? "已配置" : "未配置"} · 版本 {item.version}</p><RowActions item={item} manage={manage} disabled={!!mutation.pending} open={open} /></article>)}</div></>}
      <AdminPagination total={page.total} page={nav.page} pageSize={limit} pageSizes={[10, 25, 50, 100]} onPageChange={nav.setPage} onPageSizeChange={value => { nav.setExtra({ ...nav.extra, limit: String(value) }); nav.setPage(0); }} />
    </>}
    <Dialog open={drawer !== null} onOpenChange={open => { if (!open) close(); }}><DialogContent showCloseButton={false} className="inset-y-0 right-0 left-auto h-dvh max-h-dvh w-full max-w-full translate-x-0 translate-y-0 overflow-y-auto rounded-none sm:max-w-2xl"><DialogHeader><DialogTitle>{drawer?.kind === "create" ? "新建人工库存" : drawer?.kind === "edit" ? "编辑人工库存" : drawer?.kind === "release" ? "核实回收并释放" : "人工库存详情"}</DialogTitle><DialogDescription>成本仅管理元数据，不入账；上游期限独立于客户服务期限，账号密码只写不回显。</DialogDescription></DialogHeader>{drawer && <ResourceDrawer key={`${revision}:${drawer.kind}:${"id" in drawer ? drawer.id : "new"}`} drawer={drawer} recovery={recovery} actor={actor} mutation={mutation} onSuccess={created} />}<Button type="button" variant="outline" disabled={mutation.busy} onClick={close}>关闭抽屉</Button></DialogContent></Dialog>
  </div>;
}
function Binding({ resource, actor }: { resource: ManualResource; actor: User }) { return resource.binding_id ? <div className="space-y-1 text-sm">{can(actor, "services.view") ? <Link href={`/admin/services/${resource.service_id}`} className="text-primary hover:underline">服务 #{resource.service_id}</Link> : <span>服务 #{resource.service_id}</span>}<p className="text-xs text-muted-foreground">客户 #{resource.customer_id} · 绑定 #{resource.binding_id}</p></div> : <span className="text-sm">未占用</span>; }
function RowActions({ item, manage, disabled, open }: { item: ManualResource; manage: boolean; disabled: boolean; open: (value: Drawer) => void }) { return <div className="flex flex-wrap gap-2"><Button size="sm" variant="outline" disabled={disabled} onClick={() => open({ kind: "detail", id: item.id })}>详情</Button>{manage && <><Button size="sm" variant="outline" disabled={disabled} onClick={() => open({ kind: "edit", id: item.id })}>编辑</Button>{item.binding_id && <Button size="sm" variant="outline" disabled={disabled} onClick={() => open({ kind: "release", id: item.id })}>核实回收并释放</Button>}</>}</div>; }
function ReadFailure({ error, retry }: { error: string; retry: () => void }) { return <div role="alert" className="space-y-2 text-sm"><p className="text-destructive">{error}</p><Button variant="outline" onClick={retry}>重新读取本区域</Button></div>; }

function ResourceDrawer({ drawer, recovery, actor, mutation, onSuccess }: { drawer: Drawer; recovery: Recovery | null; actor: User; mutation: ReturnType<typeof useManualResourceMutation>; onSuccess: (id: number) => void }) {
  const resource = useResource<ManualResource>(drawer.kind === "create" ? null : `/admin/manual-resources/${drawer.id}`, false);
  if (drawer.kind === "create") return <ResourceEditor resource={null} initialBody={recovery?.body} mutation={mutation} onSuccess={onSuccess} />;
  if (resource.error) return <ReadFailure error={resource.error.message} retry={resource.refresh} />;
  if (!resource.data || resource.isLoading) return <p role="status">正在读取库存详情…</p>;
  if (!validManualResource(resource.data) || resource.data.id !== drawer.id) return <ReadFailure error="库存详情响应无效。" retry={resource.refresh} />;
  const item = recovery?.resource?.id === drawer.id ? recovery.resource : resource.data;
  if (drawer.kind === "edit") return <ResourceEditor resource={item} initialBody={recovery?.body} mutation={mutation} onSuccess={onSuccess} />;
  if (drawer.kind === "release") return <ReleaseResource resource={item} initialNote={typeof recovery?.body.note === "string" ? recovery.body.note : ""} actor={actor} mutation={mutation} onSuccess={onSuccess} />;
  return <section className="space-y-4 text-sm"><h2 className="font-semibold">{item.name} · #{item.id}</h2><dl className="grid grid-cols-[7rem_minmax(0,1fr)] gap-3">{[["资源类型", item.resource_type], ["地址", item.address], ["上游标识", item.upstream_id], ["提供方 / 区域", `${item.provider} / ${item.region}`], ["库存状态", labels[item.status]], ["成本 CNY", money(item.cost_cents)], ["上游到期", item.upstream_expires_at ? date(item.upstream_expires_at) : "未设置"], ["交付凭据", item.has_credentials ? "已配置；账号密码不提供读取" : "未配置"], ["版本", item.version], ["备注", item.notes || "未填写"]].map(([label, value]) => <div className="contents" key={label}><dt className="text-muted-foreground">{label}</dt><dd className="whitespace-pre-wrap break-words">{value}</dd></div>)}</dl><Binding resource={item} actor={actor} /></section>;
}
function ResourceEditor({ resource, initialBody, mutation, onSuccess }: { resource: ManualResource | null; initialBody?: Record<string, unknown>; mutation: ReturnType<typeof useManualResourceMutation>; onSuccess: (id: number) => void }) {
  const [fields, setFields] = useState<Fields>(() => recoveryFields(resource, initialBody));
  const [preview, setPreview] = useState<ManualResourceRequest | null>(null), [error, setError] = useState("");
  const bound = !!resource?.binding_id;
  function field<K extends keyof Fields>(key: K, value: Fields[K]) { setFields(previous => ({ ...previous, [key]: value })); setPreview(null); }
  function prepare(event: React.FormEvent) { event.preventDefault(); if (mutation.frozen) return; try { setPreview({ kind: resource ? "update" : "create", target: resource?.id ?? null, body: prepareFields(fields, resource) }); setError(""); } catch (error) { setError((error as Error).message); } }
  return <form onSubmit={prepare} className="min-w-0 space-y-5"><fieldset disabled={mutation.frozen} className="min-w-0 space-y-5">
    {bound && <p className="rounded border p-3 text-sm">当前已占用，仅允许修改成本、上游期限和备注。交付身份、凭据和状态须终止服务并显式释放后处理。</p>}
    <section className="space-y-3"><h2 className="font-semibold">资源身份</h2><div className="grid gap-3 sm:grid-cols-2">{([["name", "名称"], ["resource_type", "资源类型"], ["provider", "提供方"], ["region", "区域"], ["address", "IP 或主机名"], ["upstream_id", "实际资源标识"]] as const).map(([key, label]) => <label key={key} className="space-y-2 text-sm"><span>{label}</span><Input aria-label={label} disabled={bound} value={fields[key]} onChange={event => field(key, event.target.value)} /></label>)}</div></section>
    <section className="space-y-3"><h2 className="font-semibold">成本与上游期限</h2><label className="block space-y-2 text-sm"><span>成本 CNY</span><Input aria-label="库存成本" inputMode="decimal" value={fields.cost} onChange={event => field("cost", event.target.value)} /></label><p className="text-xs text-muted-foreground">精确到分，仅记录库存成本，不生成经营费用或付款。</p><label className="block space-y-2 text-sm"><span>上游到期时间（上海时间）</span><Input aria-label="上游到期时间" type="datetime-local" value={fields.expiry} onChange={event => field("expiry", event.target.value)} /></label><p className="text-xs text-muted-foreground">留空清除，不改变客户服务到期日，也不发起续费。</p></section>
    {!bound && <section className="space-y-3"><h2 className="font-semibold">只写交付凭据</h2><p className="text-xs text-muted-foreground">{resource?.has_credentials ? "已有凭据，未勾选的字段保持原值。" : "凭据未配置，可选择本次写入。"}账号密码不回显、不写浏览器存储。含凭据的未知请求仅本页保留，关闭后仅剩核对标记。</p>{([ ["changeUsername", "username", "写入账号"], ["changePassword", "password", "写入密码"] ] as const).map(([checked, key, label]) => <div key={key} className="space-y-2"><label className="flex items-center gap-2 text-sm"><Checkbox checked={fields[checked]} onCheckedChange={value => field(checked, value === true)} />{label}</label>{fields[checked] && <Input aria-label={label} type="password" autoComplete="new-password" value={fields[key]} onChange={event => field(key, event.target.value)} placeholder="空值明确清除此字段" />}</div>)}</section>}
    <label className="block space-y-2 text-sm"><span>库存状态</span><select aria-label="编辑库存状态" className={selectClass} disabled={bound || resource?.status === "retired"} value={fields.status} onChange={event => field("status", event.target.value)}>{statuses.map(status => <option key={status} value={status}>{labels[status]}</option>)}</select></label><label className="block space-y-2 text-sm"><span>备注</span><Textarea aria-label="库存备注" rows={4} value={fields.notes} onChange={event => field("notes", event.target.value)} /></label>
    </fieldset>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    {preview ? <section className="space-y-3 rounded border p-4 text-sm"><h3 className="font-semibold">确认{resource ? `修改库存 #${resource.id} · 版本 ${resource.version}` : "新建库存"}</h3><p>名称：{fields.name} · 状态：{labels[fields.status]} · 成本：{money(Number(preview.body.cost_cents ?? resource?.cost_cents ?? 0))}</p><p>凭据：{Object.hasOwn(preview.body, "username") || Object.hasOwn(preview.body, "password") ? "本次写入，不显示内容" : "保持原值"}。成本和期限仅保存元数据。</p><div className="flex gap-2"><Button type="button" variant="outline" disabled={mutation.frozen} onClick={() => setPreview(null)}>返回修改</Button><Button type="button" disabled={mutation.frozen} onClick={() => void mutation.run(preview).then(result => { if (result) { setFields(blank()); onSuccess(result.id); } })}>确认保存库存</Button></div></section> : <Button type="submit" disabled={mutation.frozen}>核对库存变更</Button>}
  </form>;
}
function ReleaseResource({ resource, initialNote = "", actor, mutation, onSuccess }: { resource: ManualResource; initialNote?: string; actor: User; mutation: ReturnType<typeof useManualResourceMutation>; onSuccess: (id: number) => void }) {
  const viewServices = can(actor, "services.view");
  const service = useResource<{ service_id: number; status: string }>(viewServices && resource.service_id ? `/admin/services/${resource.service_id}/management` : null, false);
  const [note, setNote] = useState(initialNote), [confirmed, setConfirmed] = useState(false), [preview, setPreview] = useState(false), [error, setError] = useState("");
  const terminated = service.data?.service_id === resource.service_id && service.data?.status === "terminated" && !service.isLoading && !service.error;
  return <section className="space-y-4 text-sm"><p className="font-semibold">库存 #{resource.id} · 版本 {resource.version} · 原服务 #{resource.service_id}</p><p>释放仅结束占用，不终止服务、不清除原交付历史。请先在真实资源侧回收机器；页面无法替代实际回收。</p>{!resource.binding_id ? <p role="alert">当前库存没有有效占用，请刷新核对。</p> : !viewServices ? <p role="status">无服务查看权限，无法核对真实终止状态，不提供释放确认。</p> : service.error ? <ReadFailure error={service.error.message} retry={service.refresh} /> : !service.data || service.isLoading ? <p role="status">正在核对绑定服务真实状态…</p> : <p>当前服务状态：{service.data.status === "terminated" ? "已终止" : `${service.data.status}；仅已终止服务允许释放`}</p>}
    {terminated && <><label className="block space-y-2"><span>实际回收说明（3–2000字）</span><Textarea aria-label="库存回收说明" disabled={mutation.frozen} value={note} onChange={event => { setNote(event.target.value); setPreview(false); }} /></label><label className="flex items-start gap-2"><Checkbox checked={confirmed} disabled={mutation.frozen} onCheckedChange={value => { setConfirmed(value === true); setPreview(false); }} /><span>我已在资源侧核实机器实际回收，当前操作不替代服务终止。</span></label>{error && <p role="alert" className="text-destructive">{error}</p>}{preview ? <div className="space-y-3 rounded border p-3"><p>确认释放库存 #{resource.id} 与服务 #{resource.service_id} 的绑定 #{resource.binding_id}，使用库存版本 {resource.version}。</p><Button disabled={mutation.frozen || !confirmed} onClick={() => void mutation.run({ kind: "release", target: resource.id, serviceId: resource.service_id!, bindingId: resource.binding_id!, body: { expected_version: resource.version, note: note.trim() } }).then(result => { if (result) onSuccess(result.id); })}>确认显式释放库存</Button></div> : <Button disabled={mutation.frozen} onClick={() => { if (!confirmed || Array.from(note.trim()).length < 3 || Array.from(note.trim()).length > 2000) { setError("请填写3至2000字原因并确认实际回收。"); return; } setError(""); setPreview(true); }}>核对释放影响</Button>}</>}
  </section>;
}
