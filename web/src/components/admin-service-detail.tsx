"use client";

import { useEffect, useState, type ReactNode } from "react";
import Link from "next/link";
import { Tabs } from "radix-ui";
import { ArrowLeft, ExternalLink, Eye, EyeOff, FileText, RefreshCw, UserRound } from "lucide-react";
import { api, date, money, type Service } from "@/lib/api";
import { PermissionGate, useAdminPermission } from "./workspace";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Status } from "./common";
import { SupplierServiceDelivery } from "./supplier-delivery";
import { NOKVMServiceDelivery, isPowerAction, serviceActionLabels } from "./nokvm-delivery";
import { ServiceActivityPanel } from "./service-activity";
import { ServiceConfigurationChanges } from "./configuration-upgrades";
import { PurchaseFieldDetails } from "./purchase-fields";
import { RenewalBenefitDetails } from "./renewal-benefit";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { useAdminListReturnHref } from "@/hooks/use-admin-list-navigation";

const serviceStates = ["pending_payment", "queued", "provisioning", "active", "suspended", "failed", "cancelled", "terminated", "import_review_required"] as const;

const cycles:Record<string,string>={monthly:"月付",quarterly:"季付",annually:"年付",once:"一次性"};
const modes:Record<string,string>={manual:"人工交付",simulation:"模拟交付",nokvm:"NOKVM",supplier:"上游供应商"};

function Field({label,value,children}:{label:string;value?:string|number|null;children?:ReactNode}) {
  return <div className="min-w-0 space-y-2"><p className="text-xs text-muted-foreground">{label}</p>{children??<Input aria-label={label} readOnly value={value??"未提供"} className="bg-muted/30 text-sm"/>}</div>;
}

function Band({title,actions,children}:{title:string;actions?:ReactNode;children:ReactNode}) {
  return <section className="bg-white border-b"><div className="service-section-heading"><h2 className="font-medium text-sm">{title}</h2><div className="flex flex-wrap items-center gap-2">{actions}</div></div><div className="p-4 sm:p-5">{children}</div></section>;
}

function CredentialsField({service}:{service:Service}) {
  const allowed=useAdminPermission("services.manage");
  const [password,setPassword]=useState<string|null>(null);
  const [busy,setBusy]=useState(false),[error,setError]=useState("");
  const blocked=service.jobs?.some(job=>["reset_password","reinstall","rescue_system","exit_rescue_system","snapshot_restore","backup_restore"].includes(job.action)&&["pending","running","waiting_verification"].includes(job.status));
  useEffect(()=>{if(!password)return;const timer=window.setTimeout(()=>setPassword(null),60000);return ()=>window.clearTimeout(timer);},[password]);
  async function reveal() {
    if (password) {setPassword(null);return;}
    setBusy(true);setError("");
    try {const result=await api<{password:string}>(`/admin/services/${service.id}/credentials`,{method:"POST",body:"{}"});setPassword(result.password);} catch(e) {setError((e as Error).message);} finally {setBusy(false);}
  }
  return <Field label="系统密码"><div className="flex min-w-0 gap-2"><Input aria-label="系统密码" readOnly autoComplete="off" type={password?"text":"password"} value={password??"************"} className="min-w-0 bg-muted/30"/>{allowed&&<Button size="icon" variant="outline" title={password?"隐藏密码":"查看密码"} aria-label={password?"隐藏密码":"查看密码"} disabled={busy||!!blocked||!["active","suspended"].includes(service.status)||!["nokvm","supplier"].includes(service.provision_mode)} onClick={()=>void reveal()}>{password?<EyeOff size={16}/>:<Eye size={16}/>}</Button>}</div>{error&&<p role="alert" className="text-xs text-destructive mt-2 break-words">{error}</p>}</Field>;
}

export function AdminServiceDetail({service,refresh,manualDelivery,resourcePanels}:{service:Service;refresh:()=>void;manualDelivery?:ReactNode;resourcePanels?:ReactNode}) {
  const backHref = useAdminListReturnHref("/admin/services", serviceStates, "");
  const [tab,setTab]=useState("product");
  const jobs=service.jobs??[];
  const pending=jobs.filter(job=>["pending","running","waiting_manual","waiting_verification"].includes(job.status));
  const resource=service.resource_info;
  const primaryIP=resource?.dedicated_ip||resource?.ips?.[0];
  const otherIPs=resource?.assigned_ips||resource?.ips?.slice(1).join(", ");
  const lastSync=jobs.find(job=>job.action==="sync"&&job.status==="succeeded");
  const due=service.next_due_at?date(service.next_due_at):service.billing_cycle==="once"?"一次性服务":"尚未起算";
  return <div className="service-detail min-w-0">
    <div className="flex flex-wrap items-center justify-between gap-3 mb-5">
      <Button variant="ghost" size="sm" asChild><Link href={backHref}><ArrowLeft size={16}/>服务列表</Link></Button>
      <div className="flex flex-wrap gap-2"><PermissionGate permission="customers.view"><Button variant="outline" size="sm" asChild><Link href={`/admin/customers/${service.user_id}?tab=services`}><UserRound size={15}/>{service.customer_name||`客户 #${service.user_id}`}</Link></Button></PermissionGate><Button variant="outline" size="icon-sm" onClick={refresh} aria-label="刷新服务详情" title="刷新服务详情"><RefreshCw size={16}/></Button></div>
    </div>
    <header className="admin-page-heading flex flex-wrap items-start justify-between gap-4 mb-5">
      <div className="min-w-0"><h1 className="text-xl font-semibold break-words">{service.name}</h1><p className="text-sm text-muted-foreground mt-2 break-all">服务 #{service.id}<span className="mx-2">·</span>{service.domain}<span className="mx-2">·</span>{service.service_group_name??"未分组"}</p></div>
      <div className="flex items-center gap-2"><Status value={service.status}/>{pending.length>0&&<button type="button" className="text-xs text-amber-700 bg-amber-50 border border-amber-200 px-2 py-1 rounded" onClick={()=>setTab("jobs")}>{pending.length} 个待处理任务</button>}</div>
    </header>
    <Tabs.Root value={tab} onValueChange={setTab}>
      <Tabs.List aria-label="服务详情" className="service-detail-tabs">
        {[{id:"product",label:"产品/服务"},{id:"configuration",label:"配置与变更"},{id:"jobs",label:"任务记录"},{id:"history",label:"操作历史"}].map(item=><Tabs.Trigger key={item.id} value={item.id} className="service-detail-tab">{item.label}{item.id==="jobs"&&<span className="ml-2 text-xs text-muted-foreground">{jobs.length}</span>}</Tabs.Trigger>)}
      </Tabs.List>
      <Tabs.Content value="product" className="outline-none space-y-5">
        <Band title="产品" actions={<PermissionGate permission="orders.view"><Button variant="outline" size="sm" asChild><Link href={`/admin/orders/${service.order_id}`}><FileText size={15}/>查看订单</Link></Button></PermissionGate>}>
          <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-x-6 gap-y-5">
            <Field label="订单号" value={service.order_no||`#${service.order_id}`}/>
            <Field label="商品/服务" value={service.product_name||service.name}/>
            <Field label="接口 / 供应商" value={service.supplier_name||modes[service.provision_mode]}/>
            <Field label="IP 地址 / 端口" value={primaryIP?`${primaryIP}${resource?.port?` : ${resource.port}`:""}`:null}/>
            <Field label="登录用户名" value={service.username||null}/>
            <CredentialsField key={`${service.id}:${service.version}`} service={service}/>
            <Field label="系统" value={service.installed_os?.name||null}/>
            <Field label="其他 IP" value={otherIPs||null}/>
            <Field label="主机名" value={service.domain}/>
            <Field label="上游资源 ID" value={service.upstream_id||null}/>
            <Field label="上游商品 ID" value={service.upstream_product_id}/>
            <Field label="本地商品 ID" value={`#${service.product_id}`}/>
            <Field label="最近同步" value={lastSync?date(lastSync.completed_at||lastSync.created_at):"尚无同步记录"}/>
          </div>
          {service.last_error&&<p role="alert" className="text-sm text-destructive break-words mt-4">{service.last_error}</p>}
          {resource?.suspend_reason&&<p className="text-sm text-amber-700 mt-4 break-words">暂停原因：{resource.suspend_reason}</p>}
        </Band>
        {service.provision_mode==="supplier"&&<SupplierServiceDelivery service={service} admin refresh={refresh}/>}
        {service.provision_mode==="nokvm"&&<NOKVMServiceDelivery service={service} admin refresh={refresh}/>}
        {manualDelivery}
        <Band title="财务" actions={<><PermissionGate permission="customers.view"><PermissionGate permission="invoices.view"><Button variant="outline" size="sm" asChild><Link href={`/admin/customers/${service.user_id}?tab=transactions`}><ExternalLink size={15}/>交易流水</Link></Button></PermissionGate></PermissionGate>{service.invoice_id&&<PermissionGate permission="invoices.view"><Button variant="outline" size="sm" asChild><Link href={`/admin/invoices/${service.invoice_id}`}><FileText size={15}/>账单</Link></Button></PermissionGate>}</>}>
          <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-x-6 gap-y-5">
            <Field label="订购时间" value={date(service.created_at)}/>
            <Field label="首期金额" value={service.first_price_cents===undefined?null:money(service.first_price_cents)}/>
            <Field label="初装费" value={service.setup_fee_cents===undefined?null:money(service.setup_fee_cents)}/>
            <Field label="付款周期" value={cycles[service.billing_cycle]||service.billing_cycle}/>
            <Field label="周期金额" value={money(service.recurring_price_cents)}/>
            <Field label="到期时间" value={due}/>
            <Field label="原始账单" value={service.invoice_no}/>
            <Field label="账单状态">{service.invoice_status?<div className="h-9 flex items-center"><Status value={service.invoice_status}/></div>:<Input readOnly value="暂无账单"/>}</Field>
          </div>
          <RenewalBenefitDetails terms={service}/>
        </Band>
        {!!service.purchase_fields?.length&&<Band title="购买信息"><PurchaseFieldDetails fields={service.purchase_fields}/></Band>}
        {resourcePanels}
      </Tabs.Content>
      <Tabs.Content value="configuration" className="outline-none"><ServiceConfigurationChanges service={service} admin refresh={refresh}/>{!!service.config_snapshot?.length&&<Band title="已购配置"><dl className="grid sm:grid-cols-2 gap-4">{service.config_snapshot.map(option=><div key={option.id} className="text-sm"><dt className="text-muted-foreground">{option.group_name}</dt><dd className="mt-1 break-words">{option.label}{(option.quantity??1)>1?` × ${option.quantity}`:""}</dd></div>)}</dl></Band>}</Tabs.Content>
      <Tabs.Content value="jobs" className="outline-none bg-white">
        <div className="service-section-heading"><h2 className="text-sm font-medium">任务记录</h2><Button size="icon-sm" variant="ghost" onClick={refresh} aria-label="刷新任务记录" title="刷新任务记录"><RefreshCw size={16}/></Button></div>
        <div className="overflow-x-auto"><Table><TableHeader><TableRow><TableHead>任务</TableHead><TableHead>动作</TableHead><TableHead>状态</TableHead><TableHead>尝试次数</TableHead><TableHead>错误</TableHead><TableHead>时间</TableHead></TableRow></TableHeader><TableBody>{jobs.length?jobs.map(job=><TableRow key={job.id}><TableCell>#{job.id}</TableCell><TableCell>{serviceActionLabels[job.action]??job.action}</TableCell><TableCell><Status value={job.status==="succeeded"&&(isPowerAction(job.action)||["rescue_system","exit_rescue_system","reinstall"].includes(job.action))?"command_accepted":job.status==="succeeded"&&job.action==="reset_password"?"credentials_confirmed":job.status}/></TableCell><TableCell>{job.attempts}</TableCell><TableCell className="max-w-80 whitespace-normal break-words text-destructive">{job.last_error||"-"}</TableCell><TableCell>{date(job.completed_at||job.created_at)}</TableCell></TableRow>):<TableRow><TableCell colSpan={6} className="h-24 text-center text-muted-foreground">暂无任务记录</TableCell></TableRow>}</TableBody></Table></div>
      </Tabs.Content>
      <Tabs.Content value="history" className="outline-none"><ServiceActivityPanel serviceID={service.id} admin/></Tabs.Content>
    </Tabs.Root>
  </div>;
}
