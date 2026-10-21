"use client";

import { useState } from "react";
import { Play, RefreshCw } from "lucide-react";
import { type Service } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { useAdminIdentity, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Failure, Loading } from "./common";
import { manualActorIdentity, manualDeliveryBlocked, positiveManualID, useManualDeliveryIntent, validManualInventory, type ManualDeliveryBody, type ManualInventoryChoice } from "./admin-manual-delivery-intent";

type Page = { items: ManualInventoryChoice[]; total: number; limit: number; offset: number };
type Preview = { body: ManualDeliveryBody; action: string; resource: ManualInventoryChoice | null; serviceVersion: number };
export function ManualDelivery({ service, refresh }: { service: Service; refresh: () => void }) {
  const actor = useAdminIdentity(); const manage = useAdminPermission("services.manage");
  if (!actor || !manage || service.provision_mode !== "manual" || !positiveManualID(service.id)) return null;
  return <ManualDeliveryForActor key={`${manualActorIdentity(actor)}:${service.id}`} service={service} refresh={refresh} actor={actor} />;
}
function ManualDeliveryForActor({ service, refresh, actor }: { service: Service; refresh: () => void; actor: NonNullable<ReturnType<typeof useAdminIdentity>> }) {
  const viewResources = useAdminPermission("resources.view"), manageResources = useAdminPermission("resources.manage");
  const job = service.jobs?.find(item => item.status === "waiting_manual");
  const [mode, setMode] = useState<"manual" | "inventory">("manual"), [upstream, setUpstream] = useState(""), [username, setUsername] = useState(""), [note, setNote] = useState("");
  const [query, setQuery] = useState(""), [draftQuery, setDraftQuery] = useState(""), [page, setPage] = useState(0), [selected, setSelected] = useState<ManualInventoryChoice | null>(null), [preview, setPreview] = useState<Preview | null>(null), [error, setError] = useState("");
  const inventoryAllowed = !!job && job.action === "create" && viewResources && manageResources;
  const inventory = useResource<Page>(mode === "inventory" && inventoryAllowed ? `/admin/manual-resources?${new URLSearchParams({ q: query, status: "available", binding: "unbound", limit: "10", offset: String(page * 10) })}` : null, false);
  const mutation = useManualDeliveryIntent(actor, service, () => { setPreview(null); setSelected(null); setUpstream(""); setUsername(""); setNote(""); inventory.refresh(); refresh(); });
  const blocked = !!job && manualDeliveryBlocked(service, job.action);
  const frozen = !mutation.ready || !!mutation.storageError || mutation.busy || !!mutation.pending;
  const choices = (Array.isArray(inventory.data?.items) ? inventory.data.items : []).filter(item => validManualInventory(item) && item.status === "available" && item.service_id === null && item.binding_id === null);
  const invalidInventory = !!inventory.data && (!Array.isArray(inventory.data.items) || !Number.isSafeInteger(inventory.data.total) || inventory.data.total < 0 || choices.length !== inventory.data.items.length);
  function prepare(event: React.FormEvent) {
    event.preventDefault(); if (frozen || !job || blocked) return;
    const cleanNote = note.trim(); const bytes = new TextEncoder().encode(cleanNote).length;
    if (bytes < 3 || bytes > 2000 || cleanNote.includes("\0")) { setError("请填写3–2000字节的核验说明，勿填写账号密码。"); return; }
    if (mode === "inventory" && (!inventoryAllowed || !selected || inventory.isLoading || invalidInventory)) { setError("请读取并选择可用且未占用的库存。"); return; }
    if (mode === "manual" && (job.action === "create" && !upstream.trim() || new TextEncoder().encode(upstream.trim()).length > 256 || new TextEncoder().encode(username.trim()).length > 256)) { setError("开通须填写实际资源标识；资源标识及账号各不超过256字节。"); return; }
    const body: ManualDeliveryBody = mode === "inventory" && selected ? { job_id: job.id, manual_resource_id: selected.id, resource_expected_version: selected.version, expected_version: service.version, note: cleanNote } : { job_id: job.id, upstream_id: upstream.trim(), username: username.trim(), note: cleanNote };
    setPreview({ body, action: job.action, resource: mode === "inventory" ? selected : null, serviceVersion: service.version }); setError("");
  }
  async function confirm() {
    if (frozen || !preview || !job || blocked) return;
    if (preview.body.job_id !== job.id || preview.action !== job.action || preview.serviceVersion !== service.version) { setError("当前服务或任务已变化，原预览不能提交。请返回修改并重新核对。"); return; }
    await mutation.execute(preview);
  }
  if (mutation.ready && !mutation.pending && !job) return null;
  return <section className="data-surface mt-5 space-y-4 p-4 sm:p-6" aria-label="人工交付确认">
    <div className="flex flex-wrap items-start justify-between gap-3"><div><h2 className="font-semibold">人工交付确认</h2><p className="mt-1 text-xs text-muted-foreground">服务 #{service.id} · {service.name} · 版本 {service.version}{job ? ` · 待处理任务 #${job.id}（${job.action}）` : " · 当前无待人工任务"}</p></div><Button type="button" variant="outline" size="sm" disabled={mutation.busy} onClick={refresh}><RefreshCw size={14} />刷新服务与任务</Button></div>
    {!mutation.ready && !mutation.storageError && <Loading />}
    {mutation.storageError && <p role="alert" className="text-sm text-destructive">{mutation.storageError}</p>}
    {mutation.error && <p role="alert" className="break-words text-sm text-destructive">{mutation.error}</p>}
    {error && <p role="alert" className="break-words text-sm text-destructive">{error}</p>}
    {mutation.pending ? <div className="space-y-3 rounded border p-4 text-sm" aria-label="原交付请求核对"><p className="font-medium">{mutation.pending.phase === "rejected" ? "原交付请求已被明确拒绝，请读取现状后核对。" : "原交付结果待核验，任务、库存、版本与载荷已冻结。"}</p><p>固定服务 #{mutation.pending.serviceId} · 原任务 #{mutation.pending.jobId}（{mutation.pending.action}） · 原服务版本 {mutation.pending.serviceVersion}</p>{mutation.pending.resource && <p>库存 #{mutation.pending.resource.id} · {mutation.pending.resource.name} · {mutation.pending.resource.address} · 原版本 {mutation.pending.resource.version}</p>}<p className="break-all text-xs text-muted-foreground">原请求标识：{mutation.pending.key}；服务端按原任务和完整交付内容核对。</p>{!mutation.pending.body && <p>原账号及交付内容仅保留在原页面内存；刷新后不恢复私密内容。请回原页面重试，或读取原任务结果人工核对。</p>}<div className="flex flex-wrap gap-2"><Button type="button" disabled={mutation.busy || !mutation.pending.body || mutation.pending.phase === "rejected"} onClick={() => void mutation.execute()}>使用原交付请求核验</Button><Button type="button" variant="outline" disabled={mutation.busy} onClick={() => void mutation.readLatest()}>读取服务任务与库存现状</Button>{mutation.facts?.canEnd && <Button type="button" variant="outline" disabled={mutation.busy} onClick={() => void mutation.endReviewed()}>已核对，结束原请求</Button>}</div>{mutation.facts && <p className="text-muted-foreground">{mutation.facts.text}</p>}</div> : job && <>
      {blocked && <p role="alert" className="text-sm text-destructive">当前资金、退款冻结或服务状态不允许此任务交付，请刷新核实；后台会再次核验付款、审核及权益。</p>}
      {!preview ? <form className="space-y-4" onSubmit={prepare}><fieldset disabled={frozen || blocked} className="min-w-0 space-y-4">
        {job.action === "create" && <div className="flex flex-wrap gap-3 text-sm"><label className="flex items-center gap-2"><input type="radio" name={`delivery-mode-${service.id}`} checked={mode === "manual"} onChange={() => { setMode("manual"); setSelected(null); }} />手工登记资源</label>{inventoryAllowed ? <label className="flex items-center gap-2"><input type="radio" name={`delivery-mode-${service.id}`} checked={mode === "inventory"} onChange={() => setMode("inventory")} />从独立库存交付</label> : <span className="text-muted-foreground">库存交付需要资源查看与管理权限。</span>}</div>}
        {mode === "inventory" && inventoryAllowed ? <div className="space-y-3" aria-label="可用库存选择器"><div className="flex min-w-0 gap-2"><Input aria-label="搜索可用库存" value={draftQuery} maxLength={120} placeholder="名称、IP、资源标识、提供方" onChange={event => setDraftQuery(event.target.value)} /><Button type="button" variant="outline" onClick={() => { setQuery(draftQuery.trim()); setPage(0); if (query === draftQuery.trim() && page === 0) inventory.refresh(); }}>搜索库存</Button></div>{inventory.error ? <Failure error={inventory.error} retry={inventory.refresh} /> : inventory.isLoading || !inventory.data ? <Loading /> : invalidInventory ? <p role="alert">库存响应无效，请重新读取后核对。</p> : <>{selected && <p role="status" className="rounded border p-3 text-sm">已选择 #{selected.id} · {selected.name} · {selected.address} · 版本 {selected.version}</p>}<div className="divide-y rounded border">{choices.length ? choices.map(item => <label key={item.id} className="flex min-h-14 cursor-pointer items-center gap-3 p-3 text-sm"><input type="radio" name={`delivery-resource-${service.id}`} aria-label={`选择库存 ${item.id}`} checked={selected?.id === item.id} onChange={() => setSelected(item)} /><span className="min-w-0"><span className="block break-words font-medium">{item.name} · #{item.id}</span><span className="block break-words text-xs text-muted-foreground">{item.address || "未填IP"} · {item.provider || "未填提供方"} · {item.region || "未填区域"} · 可用 / 未占用 · 版本 {item.version} · {item.has_credentials ? "已配置凭据" : "无凭据"}</span></span></label>) : <p className="p-4 text-sm text-muted-foreground">暂无可用且未占用库存。</p>}</div><nav aria-label="可用库存分页" className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground"><span>共 {inventory.data.total} 项</span><div className="flex gap-2"><Button type="button" variant="outline" size="sm" disabled={page === 0} onClick={() => setPage(value => value - 1)}>上一页库存</Button><Button type="button" variant="outline" size="sm" disabled={(page + 1) * 10 >= inventory.data.total} onClick={() => setPage(value => value + 1)}>下一页库存</Button></div></nav></>}</div> : <div className="grid gap-3 md:grid-cols-2"><Input value={upstream} onChange={event => setUpstream(event.target.value)} placeholder="实际资源标识" aria-label="实际资源标识" maxLength={256} /><Input value={username} onChange={event => setUsername(event.target.value)} placeholder="登录用户名（可选）" aria-label="登录用户名" maxLength={256} /></div>}
        <label className="block space-y-2 text-sm"><span>交付核验说明</span><Textarea aria-label="交付核验说明" value={note} onChange={event => setNote(event.target.value)} placeholder="记录实际交付核验结果；勿填写密码或私钥" maxLength={2000} rows={3} /></label><Button type="submit"><Play size={14} />核对交付内容</Button>
      </fieldset></form> : <div className="space-y-4 rounded border p-4 text-sm" aria-label="人工交付预览"><h3 className="font-medium">确认人工交付</h3><p>服务 #{service.id} · {service.name} · 预览版本 {preview.serviceVersion}</p><p>固定任务 #{preview.body.job_id} · {preview.action}</p>{preview.resource ? <><p>库存 #{preview.resource.id} · {preview.resource.name} · {preview.resource.address} · 版本 {preview.resource.version}</p><p>确认后原子占用库存并完成此服务开通；库存账号将由后端安全交付，不能手工覆盖。成本和上游到期日不会替代客户账期。</p></> : <><p className="break-all">实际资源：{preview.body.upstream_id || "原任务资源"} · 账号：{preview.body.username || "未提供"}</p><p>请确认已经在资源端完成对应动作；本操作只登记当前任务完成。</p></>}<p className="whitespace-pre-wrap break-words">核验说明：{preview.body.note}</p><div className="flex flex-wrap gap-2"><Button type="button" disabled={frozen || blocked} onClick={() => void confirm()}>确认交付</Button><Button type="button" variant="outline" disabled={frozen} onClick={() => setPreview(null)}>返回修改</Button></div></div>}
    </>}
    <p className="text-xs text-muted-foreground">独立库存只用于人工开通任务；停止服务不自动释放库存。付款、审核、资金冻结及任务状态均由后台再次校验。</p>
  </section>;
}
