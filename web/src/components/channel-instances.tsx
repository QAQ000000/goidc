"use client";

import { useMemo, useState } from "react";
import { Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { api } from "@/lib/api";
import { Workspace } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";

type Field = { key: string; label: string; type: string; required: boolean; secret: boolean; options?: string[] };
type Descriptor = { id: string; name: string; category: string; capabilities: string[]; fields: Field[] };
type Instance = { id: string; category: string; implementation_id: string; display_name: string; enabled: boolean; sort_order: number; config: Record<string, unknown>; secret_configured: boolean; version: number };
const categories = [{ id: "payment", label: "支付" }, { id: "mail", label: "邮件" }, { id: "sms", label: "短信" }, { id: "provision", label: "开通" }];

export function ChannelInstancesView() {
  const [category, setCategory] = useState("payment");
  const catalogue = useResource<Descriptor[]>(`/admin/channel-catalogue?category=${category}`, false);
  const instances = useResource<Instance[]>(`/admin/channel-instances?category=${category}`, false);
  const selected = useMemo(() => catalogue.data?.[0], [catalogue.data]);
  const [open, setOpen] = useState(false);
  return <Workspace title="渠道实例" admin>
    <div className="flex flex-wrap items-center justify-between gap-3 mb-6"><div><h1 className="page-title">渠道实例</h1><p className="text-sm text-muted-foreground mt-2">渠道实现编译进 Go 二进制，数据库只保存实例配置和加密凭据。</p></div><Button variant="outline" size="icon" title="刷新渠道" aria-label="刷新渠道" onClick={() => { catalogue.refresh(); instances.refresh(); }}><RefreshCw /></Button></div>
    <div className="flex flex-wrap gap-2 border-b mb-5">{categories.map(item => <Button key={item.id} type="button" variant={category === item.id ? "default" : "ghost"} size="sm" onClick={() => { setCategory(item.id); setOpen(false); }}>{item.label}</Button>)}</div>
    {catalogue.error || instances.error ? <Failure error={catalogue.error ?? instances.error ?? new Error("加载失败")} retry={() => { catalogue.refresh(); instances.refresh(); }} /> : !catalogue.data || !instances.data ? <Loading /> : <>
      {open && selected && <ChannelEditor category={category} descriptor={selected} onDone={() => { setOpen(false); instances.refresh(); }} />}
      <div className="flex justify-end mb-3"><Button onClick={() => setOpen(true)} disabled={!selected}><Plus />新增实例</Button></div>
      <div className="grid gap-3">{instances.data.length === 0 ? <section className="data-surface p-8 text-center text-muted-foreground">暂无渠道实例</section> : instances.data.map(item => <ChannelEditor key={item.id} category={category} descriptor={(catalogue.data ?? []).find(d => d.id === item.implementation_id) ?? selected!} initial={item} onDone={() => instances.refresh()} />)}</div>
    </>}
  </Workspace>;
}

function ChannelEditor({ category, descriptor, initial, onDone }: { category: string; descriptor: Descriptor; initial?: Instance; onDone: () => void }) {
  const [name, setName] = useState(initial?.display_name ?? descriptor.name);
  const [enabled, setEnabled] = useState(initial?.enabled ?? false);
  const [config, setConfig] = useState<Record<string, unknown>>(initial?.config ?? {});
  const [secret, setSecret] = useState("");
  const [clear, setClear] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const setValue = (key: string, value: unknown) => setConfig(current => ({ ...current, [key]: value }));
  async function save(event: React.FormEvent) {
    event.preventDefault(); setBusy(true); setMessage("");
    try {
      const body = { category, implementation_id: descriptor.id, display_name: name, enabled, config, ...(secret ? { secret } : {}), clear_secret: clear, ...(initial ? { expected_version: initial.version } : {}) };
      if (initial) await api(`/admin/channel-instances/${initial.id}`, { method: "PUT", body: JSON.stringify(body) });
      else await api("/admin/channel-instances", { method: "POST", body: JSON.stringify(body) });
      setMessage("已保存"); setSecret(""); setClear(false); onDone();
    } catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  async function remove() {
    if (!initial || !window.confirm("删除该渠道实例？历史业务记录仍保留实例 ID。")) return;
    setBusy(true); setMessage("");
    try { await api(`/admin/channel-instances/${initial.id}`, { method: "DELETE", body: JSON.stringify({ expected_version: initial.version }) }); onDone(); }
    catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  return <form onSubmit={save} className="data-surface p-5 space-y-4"><div className="flex flex-wrap justify-between gap-3"><div><h2 className="font-semibold">{name || descriptor.name}</h2><p className="text-xs text-muted-foreground mt-1">{descriptor.name} · {descriptor.capabilities.join(" / ")}</p></div>{initial && <Button type="button" variant="ghost" size="icon" title="删除渠道实例" aria-label="删除渠道实例" disabled={busy} onClick={() => void remove()}><Trash2 /></Button>}</div><div className="grid md:grid-cols-2 gap-4"><label className="grid gap-2 text-sm">显示名称<Input value={name} onChange={event => setName(event.target.value)} required maxLength={160} /></label><label className="flex items-center gap-2 text-sm self-end"><Checkbox checked={enabled} onCheckedChange={value => setEnabled(value === true)} disabled={clear} />启用实例</label>{descriptor.fields.map(field => field.secret ? <label key={field.key} className="grid gap-2 text-sm md:col-span-2">{field.label}<Input type="password" value={secret} onChange={event => setSecret(event.target.value)} placeholder={initial?.secret_configured ? "已配置，留空保留" : "请输入密钥"} autoComplete="new-password" required={field.required && !initial?.secret_configured} /><span className="text-xs text-muted-foreground"><Checkbox checked={clear} onCheckedChange={value => setClear(value === true)} /> 清除已保存密钥</span></label> : <label key={field.key} className="grid gap-2 text-sm">{field.label}{field.type === "select" ? <select className="h-9 rounded-md border bg-background px-3" value={String(config[field.key] ?? field.options?.[0] ?? "")} onChange={event => setValue(field.key, event.target.value)}>{field.options?.map(option => <option key={option} value={option}>{option}</option>)}</select> : <Input type={field.type === "number" ? "number" : "text"} value={String(config[field.key] ?? "")} onChange={event => setValue(field.key, field.type === "number" ? Number(event.target.value) : event.target.value)} required={field.required} />}</label>)}</div><div className="flex items-center gap-3"><Button disabled={busy}><Save />保存</Button>{message && <span className="text-sm text-muted-foreground" role="status">{message}</span>}</div></form>;
}
