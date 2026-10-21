"use client";
import { ResourceTable } from "./resource-table";
import { NOKVMServiceDelivery, serviceActionLabels, isPowerAction } from "./nokvm-delivery";
import { SupplierServiceDelivery } from "./supplier-delivery";
import { AdminServiceDetail } from "./admin-service-detail";
import { AdminServiceList } from "./admin-service-list";
import { ManualDelivery } from "./admin-manual-delivery";
import { OrderRefundHoldLabel, orderRefundHold, orderRefundBlocksDelivery } from "./external-refund-order";
import { NOKVMMonitoringPanel } from "./nokvm-monitoring";
import { NOKVMStoragePanel } from "./nokvm-storage";
import { NOKVMNATPanel } from "./nokvm-nat";
import { NOKVMCDROMPanel } from "./nokvm-cdrom";
import { useEffect, useState } from "react";
import { ServiceDownloads } from "./service-downloads";
import { ServiceConfigurationChanges } from "./configuration-upgrades";
import { ServiceActivityPanel } from "./service-activity";
import { PurchaseFieldDetails } from "./purchase-fields";
import Link from "next/link";
import { Search, ArrowUpRight, Server, Pause, Play, Trash2, RotateCcw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/checkbox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "@/components/ui/table";
import { api, date, money, type Service } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Failure, Loading, Status, statusNames } from "./common";
import { Workspace, PermissionGate, useAdminPermission } from "./workspace";
import { ServiceNavigationSettings } from "./service-groups";
import { useServiceNavigation } from "@/hooks/use-service-navigation";
import type { ServicePage } from "@/lib/api";
import { catalogSelectStyle } from "./catalog-admin";
import { useAdminListNavigation } from "@/hooks/use-admin-list-navigation";
import { PageHeading } from "./resource-page";

const adminServiceStates = ["pending_payment", "queued", "provisioning", "active", "suspended", "failed", "cancelled", "terminated", "import_review_required"] as const;

export function ServicesView({ admin = false, groupId }: { admin?: boolean; groupId?: number }) {
  return <Workspace title={admin ? "产品服务" : "我的服务"} admin={admin}><ServiceList admin={admin} groupId={groupId}/></Workspace>;
}

function ServiceList({admin,groupId}:{admin:boolean;groupId?:number}){
  const { query, setQuery, draft, setDraft, status: state, setStatus: setState, page, setPage, ready, suffix } = useAdminListNavigation(admin, adminServiceStates, "");
  const offset = page * 20;
  const setOffset = (value: number | ((current: number) => number)) => setPage(current => Math.floor((typeof value === "function" ? value(current * 20) : value) / 20));
  const groups=useServiceNavigation(!admin);
  const resource=useResource<ServicePage>(ready ? `${admin?"/admin":""}/services/page?group_id=${groupId??""}&status=${state}&q=${encodeURIComponent(query)}&limit=20&offset=${offset}` : null,false);
  const {data,error,refresh}=resource;
  useEffect(()=>{
    if(!data?.items.some(s=>s.status==="queued"||s.status==="provisioning"))return;
    const timer=window.setInterval(refresh,2000);return ()=>window.clearInterval(timer);
  },[data,refresh]);
  const group=groups.data?.find(g=>g.id===groupId);
  const missingGroup=groupId!==undefined&&groupId!==0&&!!groups.data&&!group;
  if(admin) return <AdminServiceList resource={resource} states={adminServiceStates} status={state} setStatus={value=>{setState(value);setPage(0);}} draft={draft} setDraft={setDraft} search={()=>{setQuery(draft);setPage(0);}} page={page} setPage={setPage} suffix={suffix} renderActions={service=><ServiceActions service={service} admin refresh={refresh}/>}/>;
  return <>
    <PageHeading title={admin?"产品服务":groupId===undefined?"我的服务":groupId===0?"未分组服务":group?.name??"服务分组"} description={admin?"业务列表":"查看并管理已购买的服务"}>{!admin&&<ServiceNavigationSettings/>}</PageHeading>
    {!admin&&<div className="flex flex-wrap items-center gap-3 mb-5"><label className="text-sm flex gap-2 items-center min-w-0">查看分组<select aria-label="查看服务分组" className={`${catalogSelectStyle} sm:max-w-xs`} value={groupId??""} onChange={e=>{window.location.assign(e.target.value?`/console/services/group/${e.target.value}`:"/console/services");}}><option value="">全部服务</option><option value="0">未分组服务</option>{groups.data?.map(g=><option key={g.id} value={g.id}>{g.name}{!g.visible&&"（导航隐藏）"}</option>)}{missingGroup&&<option value={groupId}>已删除的分组</option>}</select></label>{groupId!==undefined&&<Button variant="link" asChild><Link href="/console/services">查看全部服务</Link></Button>}</div>}
    {missingGroup&&<p role="status" className="text-sm text-muted-foreground mb-5">该服务分组已删除，请从全部服务或其他分组继续查看。</p>}
    {!admin&&groups.error&&<Failure error={groups.error} retry={groups.refresh}/>}
    <form className="grid sm:grid-cols-[12rem_1fr_auto] gap-3 mb-6" onSubmit={e=>{e.preventDefault();setQuery(draft);setOffset(0);}}><select aria-label="服务状态筛选" className={catalogSelectStyle} value={state} onChange={e=>{setState(e.target.value);setOffset(0);}}><option value="">全部状态</option>{adminServiceStates.map(value=><option value={value} key={value}>{statusNames[value] ?? value}</option>)}</select><Input aria-label="搜索服务" value={draft} onChange={e=>setDraft(e.target.value)} placeholder="搜索服务名称或标识"/><Button variant="outline"><Search size={16}/>搜索</Button></form>
    {error?<Failure error={error} retry={refresh}/>:!data?<Loading/>:<><div className="admin-list-surface data-surface overflow-auto"><Table><TableHeader><TableRow><TableHead>服务</TableHead>{admin&&<TableHead>客户</TableHead>}<TableHead>主机名</TableHead>{admin&&<><TableHead>IP 地址</TableHead><TableHead>付款周期</TableHead><TableHead>周期金额</TableHead></>}<TableHead>到期时间</TableHead><TableHead>状态</TableHead><TableHead>操作</TableHead></TableRow></TableHeader><TableBody>{data.items.length===0?<TableRow><TableCell colSpan={admin?9:5} className="h-32 text-center text-muted-foreground">暂无服务</TableCell></TableRow>:data.items.map(service=><TableRow key={service.id}><TableCell><Link className="text-primary hover:underline" href={`${admin?"/admin":"/console"}/services/${service.id}${suffix}`}><div className="font-medium flex items-center gap-2"><Server size={15}/>{service.name}</div><div className="text-xs text-muted-foreground mt-1">#{service.id} · {service.service_group_name??"未分组"}</div></Link></TableCell>{admin&&<TableCell>{service.customer_name??"-"}</TableCell>}<TableCell className="font-mono text-xs">{service.domain}</TableCell>{admin&&<><TableCell className="font-mono text-xs">{service.resource_info?.dedicated_ip || service.resource_info?.ips?.[0] || "-"}</TableCell><TableCell>{({monthly:"月付",quarterly:"季付",annually:"年付",once:"一次性"} as Record<string,string>)[service.billing_cycle] || service.billing_cycle}</TableCell><TableCell className="money">{money(service.recurring_price_cents)}</TableCell></>}<TableCell>{serviceDueLabel(service)}</TableCell><TableCell><Status value={service.status}/>{admin && <OrderRefundHoldLabel value={service.external_refund_order_hold} />}</TableCell><TableCell><ServiceActions service={service} admin={admin} refresh={refresh}/></TableCell></TableRow>)}</TableBody></Table></div><div className="flex justify-between items-center flex-wrap gap-3 mt-5 text-sm"><span>{data.total} 个服务</span><div className="flex gap-2"><Button variant="outline" disabled={offset===0} onClick={()=>setOffset(v=>Math.max(0,v-20))}>上一页</Button><Button variant="outline" disabled={offset+20>=data.total} onClick={()=>setOffset(v=>v+20)}>下一页</Button></div></div></>}
  </>;
}

function serviceDueLabel(service: Service) {
  if (service.next_due_at) return date(service.next_due_at);
  return service.billing_cycle === "once" ? "一次性服务" : "尚未起算";
}

export function ServiceDetailView({ id, admin = false }: { id: number; admin?: boolean }) {
  const { data, error, refresh } = useResource<Service>(`${admin ? "/admin" : ""}/services/${id}`, !admin);
  useEffect(() => {
    if (!data?.jobs?.some((job) => job.status === "pending" || job.status === "running")) return;
    const timer = window.setInterval(refresh, 2000);
    return () => window.clearInterval(timer);
  }, [data, refresh]);
  if (admin) return <Workspace title="服务详情" admin>{error?<Failure error={error} retry={refresh}/>:!data?<Loading/>:<AdminServiceDetail key={`${data.id}:${data.user_id}:${data.owner_generation??"missing"}`} service={data} refresh={refresh} manualDelivery={data.provision_mode==="manual"?<PermissionGate permission="services.manage"><ManualDelivery service={data} refresh={refresh}/></PermissionGate>:null} resourcePanels={data.provision_mode==="nokvm"&&["active","suspended"].includes(data.status)?<><NOKVMMonitoringPanel service={data} admin/><NOKVMStoragePanel service={data} admin refresh={refresh}/><NOKVMNATPanel service={data} admin refresh={refresh}/><NOKVMCDROMPanel service={data} admin refresh={refresh}/></>:null}/>}</Workspace>;
  return (
    <Workspace title="服务详情" admin={admin}>
      <Button variant="ghost" className="mb-5" asChild><Link href={admin ? "/admin/services" : "/console/services"}><ArrowUpRight size={16} />返回服务列表</Link></Button>
      {error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : (
        <>
          <PageHeading title={data.name} description={`服务 #${data.id} · ${data.domain}`}>
            <Status value={data.status} />
            {["active", "suspended"].includes(data.status) && <Button variant="outline" asChild><Link href={`/console/services/${id}/traffic`}>购买流量包</Link></Button>}
          </PageHeading>
          <div className="grid md:grid-cols-2 gap-5 mb-8">
            <section className="data-surface p-6"><p className="text-xs text-muted-foreground">上游标识</p><p className="font-mono mt-2 break-all">{data.upstream_id || "尚未分配"}</p><p className="text-xs text-muted-foreground mt-5">登录用户名</p><p className="mt-2">{data.username || "尚未分配"}</p></section>
            <section className="data-surface p-6"><p className="text-xs text-muted-foreground">到期时间</p><p className="mt-2">{serviceDueLabel(data)}</p><p className="text-xs text-muted-foreground mt-5">最近错误</p><p className="mt-2 text-destructive break-words">{data.last_error || "无"}</p></section>
          </div>
          <section className="data-surface overflow-auto"><div className="surface-head"><div><h2 className="font-semibold">开通任务</h2><p className="text-xs text-muted-foreground mt-1">任务状态与重试记录</p></div></div><ResourceTable
            items={data.jobs ?? []}
            rowKey={(job) => job.id}
            className=""
            empty={"暂无任务"}
            columns={[
              { header: "任务", render: (job) => <>#{job.id}</> },
              { header: "动作", render: (job) => serviceActionLabels[job.action] ?? job.action },
              {
                header: "状态",
                render: (job) => (
                  <>
                    <Status
                      value={
                        job.status === "succeeded"
                          ? isPowerAction(job.action)
                            ? "command_accepted"
                            : job.action === "reset_password"
                              ? "credentials_confirmed"
                              : job.status
                          : job.status
                      }
                    />
                  </>
                ),
              },
              { header: "尝试次数", render: (job) => job.attempts },
              {
                header: "错误",
                render: (job) => job.last_error || "-",
                className: "text-destructive max-w-80 whitespace-normal",
              },
              { header: "时间", render: (job) => date(job.completed_at || job.created_at) },
            ]}
          /></section>
          <ServiceActivityPanel serviceID={data.id} admin={admin} />
          {admin && data.provision_mode === "manual" && <PermissionGate permission="services.manage"><ManualDelivery service={data} refresh={refresh} /></PermissionGate>}
          {data.provision_mode === "nokvm" && <NOKVMServiceDelivery key={`${data.id}:${data.version}`} service={data} admin={admin} refresh={refresh} />}
          {data.provision_mode === "supplier" && <SupplierServiceDelivery key={`${data.id}:${data.version}`} service={data} admin={admin} refresh={refresh} />}
          {data.provision_mode === "nokvm" && ["active","suspended"].includes(data.status) && <NOKVMMonitoringPanel key={data.id} service={data} admin={admin}/>}
          {data.provision_mode === "nokvm" && ["active","suspended"].includes(data.status) && <NOKVMStoragePanel key={`${data.id}:${data.jobs?.map(j=>j.status).join("-")}`} service={data} admin={admin} refresh={refresh}/>}
          {data.provision_mode === "nokvm" && ["active","suspended"].includes(data.status) && <NOKVMNATPanel key={`${data.id}:${data.jobs?.map(j=>j.status).join("-")}`} service={data} admin={admin} refresh={refresh}/>}
          {data.provision_mode === "nokvm" && ["active","suspended"].includes(data.status) && <NOKVMCDROMPanel key={`cdrom:${data.id}:${data.jobs?.map(j=>j.status).join("-")}`} service={data} admin={admin} refresh={refresh}/>}
          <ServiceConfigurationChanges service={data} admin={admin} refresh={refresh} />
          {!!data.purchase_fields?.length && <section className="data-surface p-6 my-5"><h2 className="font-semibold mb-4">购买信息</h2><PurchaseFieldDetails fields={data.purchase_fields}/></section>}
          {!admin && <ServiceDownloads key={data.id} serviceID={data.id} />}
          {!admin && (data.provision_mode !== "supplier" || data.cancellation) && <CancellationPanel service={data} refresh={refresh} />}
          {!admin && <RenewalPanel service={data} />}
          {!admin && <AutoRenewPanel service={data} refresh={refresh} />}
        </>
      )}
    </Workspace>
  );
}

function CancellationPanel({ service, refresh }: { service: Service; refresh: () => void }) {
  const request = service.cancellation;
  const [type, setType] = useState<"immediate" | "end_of_billing">("end_of_billing");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true); setError("");
    try {
      await api(`/services/${service.id}/cancellation`, { method: "POST", body: JSON.stringify({ type, reason }), headers: { "Idempotency-Key": `web-cancellation-${service.id}-${crypto.randomUUID()}` } });
      setReason(""); refresh();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  async function withdraw() {
    setBusy(true); setError("");
    try { await api(`/services/${service.id}/cancellation`, { method: "DELETE" }); refresh(); }
    catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  if (request && request.status !== "withdrawn") {
    return <section className="data-surface p-6 mt-5"><div className="surface-head"><div><h2 className="font-semibold">取消申请</h2><p className="text-xs text-muted-foreground mt-1">{request.type === "immediate" ? "立即取消" : "当前账期结束时取消"}</p></div><Status value={request.status} /></div><p className="text-sm mt-4 whitespace-pre-wrap">{request.reason}</p>{request.resolution_note && <p className="text-xs text-muted-foreground mt-3">处理备注：{request.resolution_note}</p>}{request.status === "pending" && <Button className="mt-4" variant="outline" disabled={busy} onClick={withdraw}>撤回申请</Button>}{error && <p className="text-sm text-destructive mt-3">{error}</p>}</section>;
  }
  if (service.status !== "active" && service.status !== "suspended") return null;
  return <section className="data-surface p-6 mt-5"><div className="surface-head"><div><h2 className="font-semibold">申请取消服务</h2><p className="text-xs text-muted-foreground mt-1">提交后由管理员审核，服务不会立即中断。</p></div></div><form className="space-y-4 mt-4" onSubmit={submit}><div className="grid sm:grid-cols-2 gap-3"><label className="text-sm"><span className="block text-muted-foreground mb-2">取消时间</span><Select value={type} onValueChange={(value) => setType(value as typeof type)}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="end_of_billing">账期结束时取消</SelectItem><SelectItem value="immediate">立即取消</SelectItem></SelectContent></Select></label><label className="text-sm"><span className="block text-muted-foreground mb-2">申请原因</span><Input value={reason} onChange={(event) => setReason(event.target.value)} minLength={3} maxLength={2000} placeholder="请说明取消原因" required /></label></div><div className="flex items-center gap-3"><Button variant="destructive" disabled={busy || reason.trim().length < 3}>提交取消申请</Button>{error && <span className="text-sm text-destructive">{error}</span>}</div></form></section>;
}

function RenewalPanel({ service }: { service: Service }) {
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [invoiceID, setInvoiceID] = useState<number | null>(null);
  if ((service.status !== "active" && service.status !== "suspended") || service.billing_cycle === "once") return null;
  async function renew() {
    setBusy(true); setMessage("");
    try {
      const result = await api<{ invoice_id: number }>(`/services/${service.id}/renew`, { method: "POST", body: "{}", headers: { "Idempotency-Key": `web-renew-${service.id}-${crypto.randomUUID()}` } });
      setMessage(`已生成账单 #${result.invoice_id}`);
      setInvoiceID(result.invoice_id);
    } catch (e) { setMessage((e as Error).message); } finally { setBusy(false); }
  }
  return <section className="data-surface p-6 mt-5"><div className="surface-head"><div><h2 className="font-semibold">服务续费</h2>{service.provision_mode==="supplier"&&<p className="text-xs text-muted-foreground mt-1">付款后自动向上游续费，到期时间以上游确认结果为准。请在任务记录中查看处理状态。</p>}<p className="text-xs text-muted-foreground mt-1">当前到期时间：{service.next_due_at ? date(service.next_due_at) : "未设置"}</p></div></div><div className="flex flex-wrap items-center gap-3 mt-4"><Button variant="outline" disabled={busy} onClick={renew}><RotateCcw size={15} />{busy ? "生成中" : "生成续费账单"}</Button><Link className="text-sm text-primary underline underline-offset-4" href={invoiceID ? `/console/invoices/${invoiceID}` : "/console/invoices"}>查看账单</Link>{message && <span className="text-xs text-muted-foreground">{message}</span>}</div></section>;
}

function AutoRenewPanel({ service, refresh }: { service: Service; refresh: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if ((service.status !== "active" && service.status !== "suspended") || service.billing_cycle === "once") return null;
  async function change(enabled: boolean) {
    setBusy(true); setError("");
    try {
      await api(`/services/${service.id}/auto-renew`, { method: "PATCH", body: JSON.stringify({ enabled }) });
      refresh();
    } catch (e) { setError((e as Error).message); } finally { setBusy(false); }
  }
  return <section className="data-surface p-6 mt-5"><div className="surface-head"><div><h2 className="font-semibold">自动续费</h2><p className="text-xs text-muted-foreground mt-1">到期前 24 小时内尝试使用账户余额续费</p></div></div><label className="flex items-center gap-3 mt-4 text-sm"><Checkbox checked={service.auto_renew} disabled={busy} onCheckedChange={(value) => void change(value === true)} />启用自动续费</label>{service.auto_renew_error && <p className="text-xs text-destructive mt-3">最近一次尝试：{service.auto_renew_error}</p>}{error && <p className="text-xs text-destructive mt-3">{error}</p>}</section>;
}


function ServiceActions({ service, admin, refresh }: { service: Service; admin: boolean; refresh: () => void }) {
  const allowed = useAdminPermission("services.manage");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const actions: { action: "suspend" | "resume" | "terminate"; label: string; icon: typeof Pause; destructive?: boolean }[] =
    service.status === "active" ? [{ action: "suspend", label: "暂停", icon: Pause }, { action: "terminate", label: "终止", icon: Trash2, destructive: true }] :
    service.status === "suspended" ? [{ action: "resume", label: "恢复", icon: Play }, { action: "terminate", label: "终止", icon: Trash2, destructive: true }] : [];
  async function run(action: "suspend" | "resume" | "terminate") {
    setBusy(true);
    setError("");
    try {
      await api(`${admin ? "/admin" : ""}/services/${service.id}/actions`, {
        method: "POST",
        body: JSON.stringify({ action }),
        headers: { "Idempotency-Key": `web-service-${action}-${service.id}-${crypto.randomUUID()}` },
      });
      refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  if (admin && orderRefundBlocksDelivery(service.external_refund_order_hold)) return <span className="text-xs whitespace-normal">{orderRefundHold(service.external_refund_order_hold)?.status === "applied" ? "整单已退款，禁止交付" : "外部退款冻结中，禁止交付"}</span>;
  if (service.provision_mode==="supplier" || (admin && !allowed) || (!actions.length && !error)) return <span className="text-xs text-muted-foreground">-</span>;
  return <div className="flex flex-wrap items-center gap-2">{actions.map(({ action, label, icon: Icon, destructive }) => <Button key={action} size="sm" variant={destructive ? "destructive" : "outline"} disabled={busy} onClick={() => run(action)}><Icon size={14} />{label}</Button>)}{error && <span className="text-xs text-destructive max-w-40 whitespace-normal">{error}</span>}</div>;
}
