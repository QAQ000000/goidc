"use client";

import { useMemo, useState } from "react";
import Link from "next/link";
import { CheckCircle2, Copy, Info, RefreshCw, Send, Settings2, ToggleLeft, ToggleRight, Trash2, Wrench } from "lucide-react";
import { useResource } from "@/hooks/use-resource";
import { api } from "@/lib/api";
import {
  channelConfigurationErrors, channelConfigurationPath, channelDirectoryRows, channelDisableBody,
  channelFactoryAvailable, channelInstanceMode, channelMutationBody, channelSchemaCompatible,
  channelSchemaVersion, channelSecureConfigured, initialChannelConfig, isSupportedChannelField,
  type ChannelDescriptor as Descriptor, type ChannelField as Field, type ChannelInstance as Instance,
} from "@/lib/channel-configuration";
import { Workspace } from "./workspace";
import { Failure, Loading } from "./common";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Badge } from "./ui/badge";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "./ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";

export type ChannelCategory = "payment" | "mail" | "sms" | "bot" | "push" | "oauth" | "certification" | "provision";

const categories: { id: ChannelCategory; label: string }[] = [
  { id: "payment", label: "支付接口" }, { id: "mail", label: "邮件接口" },
  { id: "sms", label: "短信接口" }, { id: "oauth", label: "第三方登录" },
  { id: "bot", label: "机器人通知" }, { id: "push", label: "浏览器通知" },
  { id: "certification", label: "实名认证接口" }, { id: "provision", label: "开通接口" },
];
const categoryText: Record<string, { title: string; description: string; empty: string }> = {
  payment: { title: "支付接口", description: "选择支付接口并完成商户配置，客户即可在结算时选择支付方式。", empty: "当前没有可配置的支付接口" },
  mail: { title: "邮件接口", description: "填写邮件接口配置后，系统可以发送验证码、账单和业务通知。", empty: "当前没有可配置的邮件接口" },
  sms: { title: "短信接口", description: "填写短信接口配置后，系统可以发送注册、登录和安全验证码。", empty: "当前没有可配置的短信接口" },
  bot: { title: "机器人通知", description: "配置机器人通知渠道后，在通知中心配置业务通知规则。用户须先验证绑定并主动开启通知。", empty: "当前没有可配置的机器人通知模块" },
  push: { title: "浏览器通知", description: "配置 Web Push 公钥和私钥，再到通知中心启用业务规则。用户需主动授权并订阅每个浏览器。正式使用需要 HTTPS。", empty: "当前没有可配置的浏览器通知模块" },
  oauth: { title: "第三方登录", description: "选择开启第三方登录方式，并配置相应的接口凭据。", empty: "当前没有可配置的第三方登录接口" },
  certification: { title: "实名认证接口", description: "管理实名认证服务商配置和启用状态。", empty: "当前没有可配置的实名认证接口" },
  provision: { title: "开通接口", description: "查看开通能力；供应商账户和预置连接请在各自的业务入口管理。", empty: "当前没有可用的开通接口" },
};
const configurationStatusLabels: Record<string, string> = {
  valid: "配置有效", invalid: "配置待修正", implementation_missing: "实现缺失",
  schema_incompatible: "配置版本不兼容", external: "使用独立配置入口",
};

export function ChannelInstancesView({ initialCategory = "payment", title = "接口设置", returnHref }: { initialCategory?: ChannelCategory; title?: string; returnHref?: string }) {
  const [category, setCategory] = useState<ChannelCategory>(initialCategory);
  const catalogue = useResource<Descriptor[]>(`/admin/channel-catalogue?category=${category}`, false);
  const instances = useResource<Instance[]>(`/admin/channel-instances?category=${category}`, false);
  const [editor, setEditor] = useState<{ descriptor: Descriptor; initial?: Instance } | null>(null);
  const [notice, setNotice] = useState("");
  const meta = categoryText[category] ?? categoryText.payment;
  const refresh = () => { catalogue.refresh(); instances.refresh(); };
  const rows = useMemo(() => channelDirectoryRows(catalogue.data ?? [], instances.data ?? []), [catalogue.data, instances.data]);

  function changeCategory(next: ChannelCategory) {
    setCategory(next); setEditor(null); setNotice("");
  }

  async function clone(instance: Instance, descriptor: Descriptor) {
    setNotice("");
    try {
      const config = initialChannelConfig(descriptor, instance.config);
      const errors = channelConfigurationErrors(descriptor, config, false, {}, [], instance);
      if (errors.length) throw new Error(errors[0]);
      await api("/admin/channel-instances", { method: "POST", body: JSON.stringify({
        ...channelMutationBody(descriptor, { displayName: `${instance.display_name} 副本`, enabled: false, config, secureConfig: {}, clearSecureFields: [] }),
        sort_order: instance.sort_order + 1,
      }) });
      setNotice("接口副本已创建，请打开配置补充凭据"); instances.refresh();
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

  return <Workspace title={title} admin><div className="space-y-5">
    {returnHref && <Button type="button" variant="outline" size="sm" asChild><Link href={returnHref}>返回应用列表</Link></Button>}
    <div className="flex flex-wrap items-start justify-between gap-3"><div><h1 className="page-title">{meta.title}</h1><p className="mt-2 text-sm text-muted-foreground">{meta.description}</p></div><Button type="button" variant="outline" size="icon" title="刷新接口列表" aria-label="刷新接口列表" onClick={refresh}><RefreshCw /></Button></div>
    <div className="flex flex-wrap gap-1 border-b" role="tablist" aria-label="接口类别">{categories.map(item => <button key={item.id} type="button" role="tab" aria-selected={category === item.id} className={`border-b-2 px-4 py-2 text-sm transition-colors ${category === item.id ? "border-primary font-medium text-primary" : "border-transparent text-muted-foreground hover:text-foreground"}`} onClick={() => changeCategory(item.id)}>{item.label}</button>)}</div>
    <div className="flex items-start gap-2 rounded-md border bg-muted/25 px-4 py-3 text-sm"><Info className="mt-0.5 size-4 shrink-0 text-muted-foreground" /><span>{category === "provision" ? "供应商账户在供应商管理中配置；NOKVM 在商品交付配置中选择预置连接。本页保留旧实例供停用或删除。" : "选择目录中的接口新增配置。已启用与配置有效分别显示，连接可用性需通过相应测试确认。"}</span></div>
    {notice && <p role="status" className="text-sm text-emerald-700">{notice}</p>}
    {catalogue.error || instances.error ? <Failure error={catalogue.error ?? instances.error ?? new Error("加载失败")} retry={refresh} /> : !catalogue.data || !instances.data ? <Loading /> : <section className="data-surface overflow-hidden"><Table>
      <TableHeader><TableRow><TableHead className="w-14">ID</TableHead><TableHead>接口名称</TableHead><TableHead>标识</TableHead><TableHead className="hidden lg:table-cell">能力</TableHead><TableHead>版本</TableHead><TableHead>状态</TableHead><TableHead className="text-right">操作</TableHead></TableRow></TableHeader>
      <TableBody>{rows.map(({ descriptor, instance }, index) => <ChannelDirectoryRow key={`${descriptor?.id ?? instance?.implementation_id}:${instance?.id ?? "available"}`} index={index + 1} category={category} descriptor={descriptor} instance={instance} onInstall={() => descriptor && setEditor({ descriptor })} onEdit={() => descriptor && instance && setEditor({ descriptor, initial: instance })} onClone={() => descriptor && instance && void clone(instance, descriptor)} onTest={() => instance && void testInstance(instance)} onRefresh={refresh} />)}{!rows.length && <TableRow><TableCell colSpan={7} className="py-12 text-center text-muted-foreground">{meta.empty}</TableCell></TableRow>}</TableBody>
    </Table></section>}
    <p className="text-xs text-muted-foreground">新增接口实现需随系统版本发布；本页管理配置及启用状态。</p>
  </div>{editor && <ChannelEditorDialog descriptor={editor.descriptor} initial={editor.initial} onDone={() => { setEditor(null); instances.refresh(); }} onCancel={() => setEditor(null)} />}</Workspace>;
}

export function ChannelDirectoryRow({ index, category, descriptor, instance, onInstall, onEdit, onClone, onTest, onRefresh }: { index: number; category: string; descriptor?: Descriptor; instance?: Instance; onInstall: () => void; onEdit: () => void; onClone: () => void; onTest: () => void; onRefresh: () => void }) {
  const [busy, setBusy] = useState(false);
  const external = descriptor ? channelInstanceMode(descriptor) !== "channel" : category === "provision";
  const factoryAvailable = descriptor ? channelFactoryAvailable(descriptor, instance) : false;
  const compatible = descriptor ? channelSchemaCompatible(descriptor, instance) : false;
  const canEdit = !!descriptor && !external && compatible && factoryAvailable;
  const effectiveConfig = descriptor ? initialChannelConfig(descriptor, instance?.config) : {};
  const canEnable = canEdit && (!instance?.configuration_status || instance.configuration_status === "valid") && !!instance && !channelConfigurationErrors(descriptor!, effectiveConfig, true, {}, [], instance).length;
  const status = !descriptor ? "implementation_missing" : !compatible ? "schema_incompatible" : instance?.configuration_status;
  const disabledReason = !descriptor ? "当前构建缺少此实现，请恢复兼容模块后再配置。" : !compatible ? "配置版本不兼容，请先完成迁移。" : !factoryAvailable && !external ? "运行工厂未注册。" : "";
  async function remove() {
    if (!instance || !window.confirm("确认删除此接口实例？历史业务记录仍会保留实例编号。")) return;
    setBusy(true);
    try { await api(`/admin/channel-instances/${instance.id}`, { method: "DELETE", body: JSON.stringify({ expected_version: instance.version }) }); onRefresh(); }
    catch (error) { window.alert((error as Error).message); }
    finally { setBusy(false); }
  }
  async function toggle() {
    if (!instance || (!instance.enabled && !canEnable)) return;
    setBusy(true);
    try {
      const body = instance.enabled ? channelDisableBody(instance) : channelMutationBody(descriptor!, { displayName: instance.display_name, enabled: true, config: effectiveConfig, secureConfig: {}, clearSecureFields: [] }, instance);
      await api(`/admin/channel-instances/${instance.id}`, { method: "PUT", body: JSON.stringify(body) }); onRefresh();
    } catch (error) { window.alert((error as Error).message); }
    finally { setBusy(false); }
  }
  return <TableRow>
    <TableCell className="text-muted-foreground">{index}</TableCell>
    <TableCell><div className="font-medium">{instance?.display_name ?? descriptor?.name}</div><div className="mt-1 text-xs text-muted-foreground">{instance ? "已有配置" : external ? "独立配置入口" : "可新增配置"}</div>{disabledReason && <p className="mt-1 max-w-xs text-xs text-amber-700">{disabledReason}</p>}</TableCell>
    <TableCell><code className="text-xs">{descriptor?.id ?? instance?.implementation_id}</code></TableCell>
    <TableCell className="hidden lg:table-cell"><span className="text-xs text-muted-foreground">{(descriptor?.capabilities ?? []).join(" / ") || "—"}</span></TableCell>
    <TableCell><div className="text-xs">{descriptor?.version || "版本未提供"}</div><div className="mt-1 text-xs text-muted-foreground">配置 {instance?.schema_version ?? (descriptor ? channelSchemaVersion(descriptor) : "未知")}{instance && descriptor && !compatible ? ` → ${channelSchemaVersion(descriptor)}` : ""}</div></TableCell>
    <TableCell><div className="flex flex-col items-start gap-1">{instance ? <Badge variant={instance.enabled ? "default" : "outline"}>{instance.enabled ? <><CheckCircle2 />已启用</> : "已停用"}</Badge> : <Badge variant="outline">{external ? "独立管理" : "未配置"}</Badge>}{status && <Badge variant="outline" className={status === "valid" ? "text-emerald-700" : "text-amber-700"}>{configurationStatusLabels[status] ?? "状态未知"}</Badge>}{!factoryAvailable && descriptor && !external && <Badge variant="outline">运行工厂缺失</Badge>}</div></TableCell>
    <TableCell><div className="flex flex-wrap justify-end gap-1">{external && descriptor && <Button type="button" variant="outline" size="sm" asChild><Link href={channelConfigurationPath(descriptor)}><Settings2 />{channelInstanceMode(descriptor) === "profile" ? "商品交付配置" : "供应商管理"}</Link></Button>}{instance ? <>
      {!external && <Button type="button" variant="ghost" size="sm" disabled={busy || !canEdit} onClick={onEdit}><Settings2 />配置</Button>}
      {(category === "sms" || category === "mail") && <Button type="button" variant="ghost" size="sm" disabled={busy || !instance.enabled || !canEnable} onClick={onTest}><Send />测试发送</Button>}
      <Button type="button" variant="ghost" size="icon" title={instance.enabled ? "停用接口" : "启用接口"} aria-label={instance.enabled ? "停用接口" : "启用接口"} disabled={busy || (!instance.enabled && !canEnable)} onClick={() => void toggle()}>{instance.enabled ? <ToggleRight /> : <ToggleLeft />}</Button>
      {!external && <Button type="button" variant="ghost" size="icon" title="复制接口配置" aria-label="复制接口配置" disabled={busy || !canEdit || instance.configuration_status === "invalid"} onClick={onClone}><Copy /></Button>}
      <Button type="button" variant="ghost" size="icon" title="删除接口配置" aria-label="删除接口配置" disabled={busy} onClick={() => void remove()}><Trash2 /></Button>
    </> : !external && <Button type="button" variant="outline" size="sm" disabled={!canEdit} onClick={onInstall}><Wrench />新增配置</Button>}</div></TableCell>
  </TableRow>;
}

type FieldEditorProps = { field: Field; value: unknown; onChange: (value: unknown) => void; disabled?: boolean };

const configurationErrorText: Record<string, string> = {
  unknown_field: "不在当前字段定义中，已隐藏，重新保存时移除。",
  secret_in_config: "曾误存于普通配置，已隐藏；重新保存时清理旧值，需要保留的凭据请在密钥栏补充。",
  required: "缺少必填配置，请补全。",
  invalid_type: "原值类型无效，已隐藏，请重新填写。",
  invalid_option: "原选项无效，已隐藏，请重新选择。",
  duplicate_option: "原选项重复，已隐藏，请重新选择。",
  invalid_integer: "需要整数，原值已隐藏，请重新填写。",
  out_of_range: "原值超出允许范围，已隐藏，请重新填写。",
  invalid_smtp: "邮件配置不完整或取值无效，请检查服务器、发件人和认证配置。",
  invalid_sms_endpoint: "短信服务地址无效，请重新填写。",
  invalid_sms_api: "短信 API 地址无效，请重新填写。",
};

export function ChannelConfigurationIssues({ descriptor, instance }: { descriptor: Descriptor; instance?: Instance }) {
  if (!instance?.validation_errors?.length) return null;
  return <section className="space-y-2 rounded-md border border-amber-200 p-3 text-sm" role="status"><p className="font-medium text-amber-800">原配置需要处理</p><ul className="list-disc space-y-1 pl-5 text-xs text-amber-800">{instance.validation_errors.map((issue, index) => <li key={`${issue.field}:${issue.code}:${index}`}><span className="font-medium">{(descriptor.fields ?? []).find(field => field.key === issue.field)?.label || issue.field || "模块配置"}</span>：{configurationErrorText[issue.code] ?? "不符合当前模块要求，请核对后重新填写。"}<code className="ml-1 break-all">({issue.code})</code></li>)}</ul><p className="text-xs text-muted-foreground">已加密的凭据留空会保留；此处仅展示原配置的错误类型。</p></section>;
}

export function ChannelFieldEditor({ field, value, onChange, disabled }: FieldEditorProps) {
  const id = `channel-field-${field.key}`;
  const label = <>{field.label}{field.required && <span aria-hidden="true"> *</span>}</>;
  if (!isSupportedChannelField(field)) return <p role="alert" className="text-sm text-destructive sm:col-span-2">当前界面尚不支持 {field.label} 的字段类型（{field.type}），请升级界面后再保存。</p>;
  if (field.type === "boolean") return <label className="flex items-center gap-2 text-sm"><Checkbox id={id} checked={value === true} disabled={disabled} onCheckedChange={checked => onChange(checked === true)} />{label}</label>;
  if (field.type === "multiselect") return <fieldset className="space-y-2 sm:col-span-2" disabled={disabled}><legend className="text-sm">{label}</legend><div className="flex flex-wrap gap-3 rounded-md border px-3 py-2">{(field.options ?? []).map(option => <label key={option} className="flex items-center gap-2 text-sm"><Checkbox checked={Array.isArray(value) && value.includes(option)} onCheckedChange={checked => { const selected = Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : []; onChange(checked === true ? [...new Set([...selected, option])] : selected.filter(item => item !== option)); }} />{option}</label>)}</div></fieldset>;
  return <label htmlFor={id} className={`grid gap-2 text-sm ${field.type === "textarea" ? "sm:col-span-2" : ""}`}>{label}{field.type === "select" ? <select id={id} className="h-9 rounded-md border bg-background px-3" disabled={disabled} required={field.required} value={typeof value === "string" ? value : ""} onChange={event => onChange(event.target.value || undefined)}><option value="">请选择</option>{(field.options ?? []).map(option => <option key={option} value={option}>{option}</option>)}</select> : field.type === "textarea" ? <Textarea id={id} value={typeof value === "string" ? value : ""} required={field.required} disabled={disabled} rows={4} onChange={event => onChange(event.target.value)} /> : <Input id={id} type={field.type === "number" ? "number" : "text"} value={typeof value === "number" || typeof value === "string" ? value : ""} min={field.type === "number" ? field.min : undefined} max={field.type === "number" ? field.max : undefined} step={field.type === "number" ? field.integer ? 1 : "any" : undefined} disabled={disabled} required={field.required} onChange={event => onChange(field.type === "number" ? event.target.value === "" ? undefined : Number(event.target.value) : event.target.value)} />}</label>;
}

function ChannelEditorDialog({ descriptor, initial, onDone, onCancel }: { descriptor: Descriptor; initial?: Instance; onDone: () => void; onCancel: () => void }) {
  const [name, setName] = useState(initial?.display_name ?? descriptor.name);
  const [enabled, setEnabled] = useState(initial?.enabled ?? false);
  const [config, setConfig] = useState(() => initialChannelConfig(descriptor, initial?.config));
  const [secureConfig, setSecureConfig] = useState<Record<string, string>>({});
  const [clearSecureFields, setClearSecureFields] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const unsupported = (descriptor.fields ?? []).some(field => !isSupportedChannelField(field));
  const incompatible = !channelSchemaCompatible(descriptor, initial);
  const invalidConfigKeys = Object.keys(config).filter(key => !(descriptor.fields ?? []).some(field => field.key === key && !field.secret));
  const setValue = (key: string, value: unknown) => setConfig(current => ({ ...current, [key]: value }));
  async function save(event: React.FormEvent) {
    event.preventDefault();
    if (busy) return;
    const errors = channelConfigurationErrors(descriptor, config, enabled, secureConfig, clearSecureFields, initial);
    if (errors.length) { setMessage(errors.join(" ")); return; }
    setBusy(true); setMessage("");
    try {
      const body = channelMutationBody(descriptor, { displayName: name, enabled, config, secureConfig, clearSecureFields }, initial);
      await api(`/admin/channel-instances${initial ? `/${initial.id}` : ""}`, { method: initial ? "PUT" : "POST", body: JSON.stringify(body) });
      onDone();
    } catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  return <Dialog open onOpenChange={open => !open && !busy && onCancel()}><DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl" showCloseButton={!busy}><DialogHeader><DialogTitle>{initial ? "配置接口" : "新增接口配置"} · {descriptor.name}</DialogTitle><DialogDescription>密钥分别保存，留空保留原值；如需删除，请勾选对应密钥的清除选项。</DialogDescription></DialogHeader><form onSubmit={save} className="space-y-5">
    <ChannelConfigurationIssues descriptor={descriptor} instance={initial} />
    <fieldset disabled={busy || incompatible} className="grid gap-4 sm:grid-cols-2"><label className="grid gap-2 text-sm sm:col-span-2">接口名称<Input value={name} onChange={event => setName(event.target.value)} required maxLength={160} /></label><label className="flex items-center gap-2 text-sm sm:col-span-2"><Checkbox checked={enabled} onCheckedChange={value => setEnabled(value === true)} />启用接口</label>
      {(descriptor.fields ?? []).map(field => field.secret ? <div key={field.key} className="grid gap-2 text-sm sm:col-span-2"><label htmlFor={`channel-secret-${field.key}`}>{field.label}{field.required && " *"}</label>{field.type === "textarea" ? <Textarea id={`channel-secret-${field.key}`} value={secureConfig[field.key] ?? ""} disabled={clearSecureFields.includes(field.key)} onChange={event => setSecureConfig(current => ({ ...current, [field.key]: event.target.value }))} placeholder={channelSecureConfigured(descriptor, initial, field.key) ? "已配置，留空保留" : "请输入凭据"} autoComplete="off" rows={4} required={enabled && field.required && !channelSecureConfigured(descriptor, initial, field.key) && !clearSecureFields.includes(field.key)} /> : <Input id={`channel-secret-${field.key}`} type="password" value={secureConfig[field.key] ?? ""} disabled={clearSecureFields.includes(field.key)} onChange={event => setSecureConfig(current => ({ ...current, [field.key]: event.target.value }))} placeholder={channelSecureConfigured(descriptor, initial, field.key) ? "已配置，留空保留" : "请输入凭据"} autoComplete="new-password" required={enabled && field.required && !channelSecureConfigured(descriptor, initial, field.key) && !clearSecureFields.includes(field.key)} />}<label className="flex items-center gap-2 text-xs text-muted-foreground"><Checkbox checked={clearSecureFields.includes(field.key)} onCheckedChange={value => { setClearSecureFields(current => value === true ? [...new Set([...current, field.key])] : current.filter(key => key !== field.key)); if (value === true) { setSecureConfig(current => ({ ...current, [field.key]: "" })); if (field.required) setEnabled(false); } }} />清除{field.label}{channelSecureConfigured(descriptor, initial, field.key) ? "（已配置）" : "（尚未配置）"}</label></div> : <ChannelFieldEditor key={field.key} field={field} value={config[field.key]} onChange={value => setValue(field.key, value)} />)}
    </fieldset>
    {!!invalidConfigKeys.length && <section className="space-y-2 rounded-md border border-amber-200 p-3 text-sm"><p className="text-amber-700">原配置包含不受当前模块支持的普通字段。请核对后逐项移除；凭据请在对应密钥栏重新配置。</p>{invalidConfigKeys.map(key => <div key={key} className="flex items-center justify-between gap-3"><code className="break-all text-xs">{key}</code><Button type="button" variant="outline" size="sm" disabled={busy || incompatible} onClick={() => setConfig(current => Object.fromEntries(Object.entries(current).filter(([name]) => name !== key)))}>移除此字段</Button></div>)}</section>}
    {incompatible && <p role="alert" className="text-sm text-destructive">配置版本不兼容，请先完成迁移；可在目录中停用或删除原实例。</p>}
    {message && <p role="alert" className="break-words text-sm text-destructive">{message}</p>}
    <DialogFooter><DialogClose asChild><Button type="button" variant="outline" disabled={busy}>取消</Button></DialogClose><Button disabled={busy || unsupported || incompatible}><CheckCircle2 />{busy ? "保存中" : "保存更改"}</Button></DialogFooter>
  </form></DialogContent></Dialog>;
}
