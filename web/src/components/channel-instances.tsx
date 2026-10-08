"use client";

import { useMemo, useState } from "react";
import { CheckCircle2, Copy, Info, Plus, RefreshCw, Send, Settings2, ToggleLeft, ToggleRight, Trash2, Wrench } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { api } from "@/lib/api";
import { Workspace } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Badge } from "./ui/badge";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

type Field = { key: string; label: string; type: string; required: boolean; secret: boolean; options?: string[] };
type Descriptor = { id: string; name: string; category: string; capabilities: string[]; fields: Field[] };
type Instance = { id: string; category: string; implementation_id: string; display_name: string; enabled: boolean; sort_order: number; config: Record<string, unknown>; secret_configured: boolean; version: number; created_at?: string; updated_at?: string };
type DirectoryItem = { descriptor: Descriptor; instance?: Instance };
export type ChannelCategory = "payment" | "mail" | "sms" | "oauth" | "certification" | "provision";

const categories: { id: ChannelCategory; label: string }[] = [
  { id: "payment", label: "支付接口" },
  { id: "mail", label: "邮件接口" },
  { id: "sms", label: "短信接口" },
  { id: "oauth", label: "第三方登录" },
  { id: "certification", label: "实名认证接口" },
];

const categoryText: Record<string, { title: string; description: string; empty: string }> = {
  payment: { title: "支付接口", description: "选择支付接口并完成商户配置，客户即可在结算时选择支付方式。", empty: "当前没有已安装的支付接口" },
  mail: { title: "邮件接口", description: "填写邮件接口配置后，系统可以发送验证码、账单和业务通知。", empty: "当前没有已安装的邮件接口" },
  sms: { title: "短信接口", description: "填写短信接口配置后，系统可以发送注册、登录和安全验证码。", empty: "当前没有已安装的短信接口" },
  oauth: { title: "第三方登录", description: "选择开启第三方登录方式，接口凭据由编译进 Go 的适配器提供。", empty: "当前没有已安装的第三方登录接口" },
  certification: { title: "实名认证接口", description: "配置实名认证服务商；人工审核和后续芝麻信用等第三方接口共用此目录。", empty: "当前没有已安装的实名认证接口" },
  provision: { title: "开通接口", description: "管理上游开通适配器实例。", empty: "当前没有已安装的开通接口" },
};

export function ChannelInstancesView({ initialCategory = "payment", title = "接口设置" }: { initialCategory?: ChannelCategory; title?: string }) {
  const [category, setCategory] = useState<ChannelCategory>(initialCategory);
  const catalogue = useResource<Descriptor[]>(`/admin/channel-catalogue?category=${category}`, false);
  const instances = useResource<Instance[]>(`/admin/channel-instances?category=${category}`, false);
  const [editor, setEditor] = useState<{ descriptor: Descriptor; initial?: Instance } | null>(null);
  const [notice, setNotice] = useState("");
  const meta = categoryText[category] ?? categoryText.payment;

  const refresh = () => { catalogue.refresh(); instances.refresh(); };
  const rows = useMemo<DirectoryItem[]>(() => {
    if (!catalogue.data) return [];
    return catalogue.data.flatMap<DirectoryItem>(descriptor => {
      const configured = (instances.data ?? []).filter(item => item.implementation_id === descriptor.id);
      return configured.length ? configured.map(instance => ({ descriptor, instance })) : [{ descriptor, instance: undefined }];
    });
  }, [catalogue.data, instances.data]);

  function changeCategory(next: ChannelCategory) {
    setCategory(next);
    setEditor(null);
    setNotice("");
  }

  async function clone(instance: Instance, descriptor: Descriptor) {
    setNotice("");
    try {
      await api("/admin/channel-instances", { method: "POST", body: JSON.stringify({ category, implementation_id: descriptor.id, display_name: `${instance.display_name} 副本`, enabled: false, sort_order: instance.sort_order + 1, config: instance.config }) });
      setNotice("接口副本已创建，请打开配置补充密钥");
      instances.refresh();
    } catch (error) { setNotice((error as Error).message); }
  }

  async function testInstance(instance: Instance) {
    const recipient = window.prompt(category === "sms" ? "请输入测试手机号（含国家码）" : "请输入测试邮箱地址", "");
    if (!recipient?.trim()) return;
    setNotice("");
    try {
      const settingsPath = category === "sms" ? "/admin/sms-settings" : "/admin/smtp-settings";
      const testPath = category === "sms" ? "/admin/sms-settings/test" : "/admin/smtp-settings/test";
      const settings = await api<{ version: number; enabled: boolean }>(settingsPath);
      if (!settings.enabled) throw new Error(`${category === "sms" ? "短信" : "邮件"}发送尚未启用`);
      await api(testPath, { method: "POST", body: JSON.stringify({ recipient: recipient.trim(), expected_version: settings.version, channel_instance_id: instance.id }) });
      setNotice(`${category === "sms" ? "测试短信" : "测试邮件"}已加入发送队列`);
    } catch (error) { setNotice((error as Error).message); }
  }

  return <Workspace title={title} admin>
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div><h1 className="page-title">{meta.title}</h1><p className="text-sm text-muted-foreground mt-2">{meta.description}</p></div>
        <Button type="button" variant="outline" size="icon" title="刷新接口列表" aria-label="刷新接口列表" onClick={refresh}><RefreshCw /></Button>
      </div>
      <div className="flex flex-wrap gap-1 border-b" role="tablist" aria-label="接口类别">
        {categories.map(item => <button key={item.id} type="button" role="tab" aria-selected={category === item.id} className={`px-4 py-2 text-sm border-b-2 transition-colors ${category === item.id ? "border-primary text-primary font-medium" : "border-transparent text-muted-foreground hover:text-foreground"}`} onClick={() => changeCategory(item.id)}>{item.label}</button>)}
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border bg-muted/25 px-4 py-3">
        <div className="flex items-start gap-2 text-sm"><Info className="mt-0.5 size-4 text-muted-foreground" /><span>{meta.description}</span></div>
        <div className="flex flex-wrap gap-2"><Button type="button" variant="ghost" size="sm" disabled title="新增渠道适配器需要编译进 Go 服务"><Plus />获取更多接口</Button><Button type="button" variant="outline" size="sm" onClick={() => { const first = catalogue.data?.[0]; if (first) setEditor({ descriptor: first }); }} disabled={!catalogue.data?.length}><Plus />安装接口</Button></div>
      </div>
      {notice && <p role="status" className="text-sm text-emerald-700">{notice}</p>}
      {catalogue.error || instances.error ? <Failure error={catalogue.error ?? instances.error ?? new Error("加载失败")} retry={refresh} /> : !catalogue.data || !instances.data ? <Loading /> : <section className="data-surface overflow-hidden">
        <Table>
          <TableHeader><TableRow><TableHead className="w-14">ID</TableHead><TableHead>接口名称</TableHead><TableHead>标识</TableHead><TableHead className="hidden lg:table-cell">能力</TableHead><TableHead>版本</TableHead><TableHead>状态</TableHead><TableHead className="text-right">操作</TableHead></TableRow></TableHeader>
          <TableBody>
            {rows.map(({ descriptor, instance }, index) => <DirectoryRow key={`${descriptor.id}:${instance?.id ?? "available"}`} index={index + 1} category={category} descriptor={descriptor} instance={instance} onInstall={() => setEditor({ descriptor })} onEdit={() => instance && setEditor({ descriptor, initial: instance })} onClone={() => instance && void clone(instance, descriptor)} onTest={() => instance && void testInstance(instance)} onRefresh={refresh} />)}
            {!rows.length && <TableRow><TableCell colSpan={7} className="py-12 text-center text-muted-foreground">{meta.empty}</TableCell></TableRow>}
          </TableBody>
        </Table>
      </section>}
      <p className="text-xs text-muted-foreground">接口实现编译进 Go 二进制；此处只管理实例配置、启用状态和版本，不支持运行时上传代码。</p>
    </div>
    {editor && <ChannelEditorDialog category={category} descriptor={editor.descriptor} initial={editor.initial} onDone={() => { setEditor(null); instances.refresh(); }} onCancel={() => setEditor(null)} />}
  </Workspace>;
}

function DirectoryRow({ index, category, descriptor, instance, onInstall, onEdit, onClone, onTest, onRefresh }: { index: number; category: string; descriptor: Descriptor; instance?: Instance; onInstall: () => void; onEdit: () => void; onClone: () => void; onTest: () => void; onRefresh: () => void }) {
  const [busy, setBusy] = useState(false);
  async function remove() {
    if (!instance || !window.confirm("确认卸载此接口实例？历史业务记录仍会保留实例编号。")) return;
    setBusy(true);
    try { await api(`/admin/channel-instances/${instance.id}`, { method: "DELETE", body: JSON.stringify({ expected_version: instance.version }) }); onRefresh(); }
    catch (error) { window.alert((error as Error).message); }
    finally { setBusy(false); }
  }
  async function toggle() {
    if (!instance) return;
    setBusy(true);
    try { await api(`/admin/channel-instances/${instance.id}`, { method: "PUT", body: JSON.stringify({ category: instance.category, implementation_id: instance.implementation_id, display_name: instance.display_name, enabled: !instance.enabled, sort_order: instance.sort_order, config: instance.config, expected_version: instance.version }) }); onRefresh(); }
    catch (error) { window.alert((error as Error).message); }
    finally { setBusy(false); }
  }
  return <TableRow>
    <TableCell className="text-muted-foreground">{index}</TableCell>
    <TableCell><div className="font-medium">{instance?.display_name ?? descriptor.name}</div><div className="text-xs text-muted-foreground mt-1">{instance ? "已安装实例" : "可安装接口"}</div></TableCell>
    <TableCell><code className="text-xs">{descriptor.id}</code></TableCell>
    <TableCell className="hidden lg:table-cell"><span className="text-xs text-muted-foreground">{descriptor.capabilities.join(" / ") || "基础配置"}</span></TableCell>
    <TableCell><span className="text-xs">内置</span></TableCell>
    <TableCell>{instance ? <Badge variant={instance.enabled ? "default" : "outline"}>{instance.enabled ? <><CheckCircle2 />启用</> : "已禁用"}</Badge> : <Badge variant="outline">未安装</Badge>}</TableCell>
    <TableCell><div className="flex justify-end gap-1">{instance ? <><Button type="button" variant="ghost" size="sm" disabled={busy} onClick={onEdit}><Settings2 />配置</Button>{(category === "sms" || category === "mail") && <Button type="button" variant="ghost" size="sm" disabled={busy || !instance.enabled} onClick={onTest}><Send />测试发送</Button>}<Button type="button" variant="ghost" size="icon" title={instance.enabled ? "禁用接口" : "启用接口"} aria-label={instance.enabled ? "禁用接口" : "启用接口"} disabled={busy} onClick={() => void toggle()}>{instance.enabled ? <ToggleRight /> : <ToggleLeft />}</Button><Button type="button" variant="ghost" size="icon" title="复制接口实例" aria-label="复制接口实例" disabled={busy} onClick={onClone}><Copy /></Button><Button type="button" variant="ghost" size="icon" title="卸载接口" aria-label="卸载接口" disabled={busy} onClick={() => void remove()}><Trash2 /></Button></> : <Button type="button" variant="outline" size="sm" onClick={onInstall}><Wrench />安装</Button>}</div></TableCell>
  </TableRow>;
}

function ChannelEditorDialog({ category, descriptor, initial, onDone, onCancel }: { category: string; descriptor: Descriptor; initial?: Instance; onDone: () => void; onCancel: () => void }) {
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
      onDone();
    } catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  return <Dialog open onOpenChange={open => !open && onCancel()}><DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl"><DialogHeader><DialogTitle>{initial ? "配置接口" : "安装接口"} · {descriptor.name}</DialogTitle><DialogDescription>填写接口信息后保存。密钥字段不会回显，留空会保留现有值。</DialogDescription></DialogHeader><form onSubmit={save} className="space-y-5"><div className="grid gap-4 sm:grid-cols-2"><label className="grid gap-2 text-sm sm:col-span-2">接口名称<Input value={name} onChange={event => setName(event.target.value)} required maxLength={160} /></label><label className="flex items-center gap-2 text-sm sm:col-span-2"><Checkbox checked={enabled} onCheckedChange={value => setEnabled(value === true)} disabled={clear} />启用接口</label>{descriptor.fields.map(field => field.secret ? <label key={field.key} className="grid gap-2 text-sm sm:col-span-2">{field.label}<Input type="password" value={secret} onChange={event => setSecret(event.target.value)} placeholder={initial?.secret_configured ? "已配置，留空保留" : "请输入密钥"} autoComplete="new-password" required={field.required && !initial?.secret_configured} /><span className="text-xs text-muted-foreground"><Checkbox checked={clear} onCheckedChange={value => { setClear(value === true); if (value) setSecret(""); }} /> 清除已保存密钥</span></label> : <label key={field.key} className="grid gap-2 text-sm">{field.label}{field.key === "types" ? <div className="flex flex-wrap gap-3 rounded-md border px-3 py-2">{(field.options ?? []).map(option => <label key={option} className="flex items-center gap-2 text-sm"><Checkbox checked={Array.isArray(config[field.key]) ? (config[field.key] as unknown[]).includes(option) : true} onCheckedChange={value => { const current = Array.isArray(config[field.key]) ? (config[field.key] as string[]) : (field.options ?? []); setValue(field.key, value === true ? Array.from(new Set([...current, option])) : current.filter(item => item !== option)); }} />{option}</label>)}</div> : field.type === "select" ? <select className="h-9 rounded-md border bg-background px-3" value={String(config[field.key] ?? field.options?.[0] ?? "")} onChange={event => setValue(field.key, event.target.value)}>{field.options?.map(option => <option key={option} value={option}>{option}</option>)}</select> : <Input type={field.type === "number" ? "number" : "text"} value={String(config[field.key] ?? "")} onChange={event => setValue(field.key, field.type === "number" ? Number(event.target.value) : event.target.value)} required={field.required} />}</label>)}</div>{message && <p role="alert" className="text-sm text-destructive break-words">{message}</p>}<DialogFooter><DialogClose asChild><Button type="button" variant="outline">取消</Button></DialogClose><Button disabled={busy}><CheckCircle2 />{busy ? "保存中" : "保存更改"}</Button></DialogFooter></form></DialogContent></Dialog>;
}
