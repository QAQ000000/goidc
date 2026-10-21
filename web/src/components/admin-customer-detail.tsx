"use client";

import { useState } from "react";
import { useRouter,useSearchParams } from "next/navigation";
import Link from "next/link";
import { ArrowLeft,RefreshCw,Save,Trash2,Server,Receipt,MessageSquare,WalletCards,ArrowDownLeft } from "lucide-react";
import { ApiError,date,money,type Contact } from "@/lib/api";
import { transactionReturnHref } from "@/lib/transaction-return";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Checkbox } from "./ui/checkbox";
import { Select,SelectContent,SelectItem,SelectTrigger,SelectValue } from "./ui/select";
import { Table,TableHeader,TableHead,TableBody,TableRow,TableCell } from "./ui/table";
import { Loading,Status } from "./common";
import { Workspace,PermissionGate,useAdminPermission,useAdminIdentity } from "./workspace";
import { CustomerFieldInputs,customerFieldValues,useCustomerFieldDefinitions,useCustomerDraft,CustomerDraftNotice,CustomerMutationMessage } from "./customer-field-inputs";
import { CustomerFileManager } from "./customer-files";
import { CustomerImpersonationButton } from "./customer-impersonation-button";
import { CustomerActions } from "./admin-customers";
import { useCustomerDetailMutation } from "./admin-customer-detail-mutation";
import { CustomerTopupButton } from "./admin-customer-topup";
import { CustomerReferrerPanel } from "./admin-customer-referrer";
import { CustomerBusinessCreateActions } from "./admin-customer-business-actions";
import { AdminPageContainer } from "./admin-page-container";
import { Card, CardHeader, CardTitle, CardAction, CardContent } from "./ui/card";

type Row=Record<string,unknown>;
type Page={ items: Row[]; total: number; limit: number; offset: number };
type AdminCustomer={
  id: number; username: string; email: string; display_name: string; status: string; balance_cents: number; created_at: string;
  sales_owner_id: number|null; sales_owner_name: string|null; sales_version: number;
  group_id: number|null; group_name: string|null; company_name?: string|null; phone_number?: string|null; qq?: string|null;
  gender: string; country: string; province: string; address: string; postcode: string; source_channel: string; payment_method: string;
  language: string; admin_note: string; receive_marketing: boolean; custom_fields: Record<string,unknown>; profile_version: number;
};
type SummarySection={ available: boolean; reason: string|null; data: Row|null };
type CustomerSummary={ customer: Row; as_of: string; currency: string; sections: Record<string,SummarySection> };
type CustomerDetailTab="summary"|"profile"|"services"|"invoices"|"transactions"|"tickets"|"logs"|"notifications"|"files"|"affiliate";
const customerDetailTabLabel: Record<CustomerDetailTab,string>={ summary: "摘要",profile: "资料",services: "服务",invoices: "账单",transactions: "交易",tickets: "工单",logs: "日志",notifications: "通知",files: "文件",affiliate: "推广" };
const panelClass="min-w-0 rounded-xl border border-border bg-card p-4 sm:p-5";

export function AdminCustomerDetailView({ customerId }: { customerId: number }) {
  const actor=useAdminIdentity();
  return <Workspace title="客户详情" admin>{actor&&<CustomerDetailForActor key={JSON.stringify([actor.id,actor.role,actor.permissions,actor.customer_scope,actor.customer_ownerless,actor.ticket_scope,actor.ticket_unassigned,customerId])} customerId={customerId} />}</Workspace>;
}

function CustomerDetailForActor({ customerId }: { customerId: number }) {
  const search=useSearchParams();
  const requested=search.get("tab");
  const tab: CustomerDetailTab=requested==="contacts"? "profile":requested==="orders"? "invoices":requested&&Object.hasOwn(customerDetailTabLabel,requested)? requested as CustomerDetailTab:"summary";
  const back=new URLSearchParams(search.toString());
  back.delete("tab"); back.delete("section"); back.delete("return_to");
  const transactionBack=transactionReturnHref(search.get("return_to"));
  const returnHref=transactionBack??`/admin/customers${back.size? `?${back.toString()}`:""}`;
  const canView=useAdminPermission("customers.view");
  const customer=useResource<AdminCustomer>(canView? `/admin/customers/${customerId}`:null,false);
  const tabHref=(value: CustomerDetailTab) => { const params=new URLSearchParams(search.toString()); params.delete("section"); if(value==="summary") params.delete("tab"); else params.set("tab",value); return `/admin/customers/${customerId}${params.size? `?${params.toString()}`:""}`; };
  return <AdminPageContainer
    pageTitle={`客户 #${customerId}${customer.data? ` · ${customer.data.display_name||customer.data.username}`:""}`}
    pageDescription="查看客户资料，处理服务、账单与关联业务。"
    pageHeaderAction={<>
      <Button asChild variant="outline"><Link href={returnHref}><ArrowLeft size={16} />{transactionBack? "返回交易流水":"返回客户列表"}</Link></Button>
      <Button variant="outline" disabled={customer.isLoading} onClick={customer.refresh}><RefreshCw size={16} className={customer.isLoading? "animate-spin":""} />刷新资料</Button>
    </>}
  >
    {!canView? <NoAccess label="客户资料" />:customer.error? <DetailFailure error={customer.error} retry={customer.refresh} />:!customer.data? <Loading />:<>
      {/* Template TabsList presentation, keeping URL links and ordinary navigation semantics. */}
      <nav className="flex min-w-0 max-w-full overflow-x-auto" aria-label="客户详情页签"><div className="inline-flex w-max gap-1 rounded-lg bg-muted p-1">{(Object.keys(customerDetailTabLabel) as CustomerDetailTab[]).map(value => <Link key={value} href={tabHref(value)} prefetch={false} scroll={false} aria-current={tab===value? "page":undefined} className={`flex min-h-9 shrink-0 items-center justify-center rounded-md px-3 py-1.5 text-sm font-medium transition-colors focus-visible:outline-2 focus-visible:outline-ring ${tab===value? "bg-card text-foreground shadow-sm":"text-muted-foreground hover:text-foreground"}`}>{customerDetailTabLabel[value]}</Link>)}</div></nav>
      <div className="grid min-w-0 items-start gap-5 xl:grid-cols-[minmax(0,1fr)_320px]">
        <section className="min-w-0 space-y-4" aria-label={`${customerDetailTabLabel[tab]}内容`}>
          {tab==="summary"? <CustomerSummaryView customerId={customerId} />:tab==="profile"? <>
            <section className={panelClass}>
              <CustomerProfile key={`${customerId}:${customer.data.profile_version}`} customer={customer.data} saved={customer.refresh} cancelHref={tabHref("summary")} />
            </section>
            <section className={panelClass}>
              <ContactsPanel adminCustomerId={customerId} />
            </section>
          </>:tab==="files"? <PermissionSection permission="customer_files.view" label="客户文件">
            <section className={panelClass}>
              <CustomerFileManager customerId={customerId} />
            </section>
          </PermissionSection>:tab==="notifications"? <PermissionSection permission="notifications.view" label="通知历史">
            <CustomerNotifications customerId={customerId} />
          </PermissionSection>:<CustomerRelatedPanel key={`${tab}:${requested}`} customerId={customerId} customerName={customer.data.display_name||customer.data.username} tab={tab} initialOrders={requested==="orders"} />}
        </section>
        <Card className="order-first min-w-0 gap-4 py-5 xl:order-last" role="complementary" aria-label="当前客户身份与操作">
          <CardHeader className="px-5">
            <div className="mb-2 flex items-center gap-3"><span aria-hidden="true" className="flex size-11 shrink-0 items-center justify-center rounded-full bg-muted text-lg font-semibold">{Array.from(customer.data.display_name||customer.data.username)[0]}</span><div className="min-w-0"><CardTitle><h2 className="break-words text-base">{customer.data.display_name||customer.data.username}</h2></CardTitle><p className="mt-1 break-all text-xs text-muted-foreground">#{customerId} · {customer.data.username}</p></div></div>
            <Status value={customer.data.status} />
          </CardHeader>
          <CardContent className="space-y-4 px-5">
          <dl className="space-y-3 text-sm">
            <IdentityField label="姓名" value={customer.data.display_name} />
            <IdentityField label="邮箱" value={customer.data.email} />
            <IdentityField label="资料手机" value={customer.data.phone_number} />
            <IdentityField label="客户分组" value={customer.data.group_name||"未分组"} />
            <IdentityField label="销售归属" value={customer.data.sales_owner_name||"无归属"} />
            <IdentityField label="创建时间" value={date(customer.data.created_at)} />
          </dl>
          <div className="border-t pt-4">
            <CustomerImpersonationButton customer={customer.data} />
          </div>
          <details className="border-t pt-3">
            <summary className="cursor-pointer text-sm font-medium">更多客户操作</summary>
            <p className="my-3 text-xs text-muted-foreground">资料编辑、密码重置和余额动作分别提交，独立核验结果。</p>
            <div className="flex flex-wrap gap-2 [&>div]:flex-wrap [&>div]:justify-start">
              <CustomerActions row={customer.data} refresh={customer.refresh} />
            </div>
          </details>
          </CardContent>
        </Card>
      </div>
    </>}
  </AdminPageContainer>;
}
function IdentityField({ label,value }: { label: string; value: string|null|undefined }) {
  return <div className="min-w-0">
    <dt className="text-muted-foreground">{label}</dt>
    <dd className="mt-0.5 break-all">{value||"未填写"}</dd>
  </div>;
}
function NoAccess({ label }: { label: string }) { return <section className="rounded border bg-muted/30 p-5 text-sm" role="status">无权限查看{label}。请联系管理员核对授权。</section>; }
function PermissionSection({ permission,label,children }: { permission: string; label: string; children: React.ReactNode }) { const allowed=useAdminPermission(permission); return allowed? children:<NoAccess label={label} />; }
function DetailFailure({ error,retry }: { error: Error; retry: () => void }) {
  return <div role="alert" className="space-y-3 rounded border p-5 text-sm">
    <p className="break-words text-destructive">{error.message}</p>{error instanceof ApiError&&error.status===401? <Button asChild>
      <Link href="/admin-login">重新登录后台</Link>
    </Button>:<Button variant="outline" onClick={retry}>
      <RefreshCw size={16} />重新加载本区域</Button>}</div>;
}
function renderValue(value: unknown,monetary=false): string { if(value===null||value===undefined) return "未提供"; return monetary? money(Number(value)):String(value); }

const summaryIcons: Record<string,typeof Server>={ "服务": Server,"未付账单": Receipt,"工单": MessageSquare,"钱包流水": WalletCards,"外部现金": ArrowDownLeft };
function SummaryBlock({ label,section,fields,note }: { label: string; section?: SummarySection; fields: [string,string,boolean?][]; note?: string }) {
  const Icon=summaryIcons[label];
  return <Card className="min-w-0 gap-4 py-5">
    <CardHeader className="px-5"><CardTitle><h2 className="text-sm">{label}</h2></CardTitle>{Icon&&<CardAction><Icon size={17} aria-hidden="true" className="text-muted-foreground" /></CardAction>}</CardHeader>
    <CardContent className="px-5">{!section? <p className="text-sm text-muted-foreground">本次响应未提供此区域。</p>:!section.available? <p className="text-sm text-muted-foreground">{section.reason==="permission_denied"? "无权限查看":"当前不可用"}</p>:!section.data? <p className="text-sm text-muted-foreground">数据未提供，请刷新摘要核对。</p>:<>
      <dl className="grid grid-cols-2 gap-4">{fields.map(([key,title,monetary]) => <div key={key} className="min-w-0">
        <dt className="text-xs text-muted-foreground">{title}</dt>
        <dd className="mt-1.5 break-words text-xl font-semibold tracking-tight tabular-nums">{renderValue(section.data?.[key],monetary)}</dd>
      </div>)}</dl>{note&&<p className="mt-4 border-t pt-3 text-xs leading-5 text-muted-foreground">{note}</p>}</>}</CardContent>
  </Card>;
}
function CustomerSummaryView({ customerId }: { customerId: number }) {
  const summary=useResource<CustomerSummary>(`/admin/customers/${customerId}/summary`,false);
  return <div className="min-w-0 space-y-4">
    <div className="flex flex-wrap items-center justify-between gap-2">
      <p className="text-sm text-muted-foreground">{summary.data? `截至 ${date(summary.data.as_of)} · CNY`:"客户业务摘要"}</p>
      <Button size="sm" variant="outline" disabled={summary.isLoading} onClick={summary.refresh}>
        <RefreshCw size={14} />刷新摘要</Button>
    </div>{summary.error? <DetailFailure error={summary.error} retry={summary.refresh} />:!summary.data? <Loading />:<>
      <div className="grid min-w-0 gap-4 md:grid-cols-2">
        <SummaryBlock label="服务" section={summary.data.sections.services} fields={[["total","全部服务"],["active","使用中"],["suspended","已暂停"],["expiring_30_days","30天内到期"],["overdue","已到期"]]} />
        <SummaryBlock label="未付账单" section={summary.data.sections.invoices} fields={[["unpaid_count","未付账单数"],["unpaid_cents","未付金额",true],["overdue_count","逾期账单数"],["overdue_cents","逾期金额",true]]} />
        <SummaryBlock label="工单" section={summary.data.sections.tickets} fields={[["total","全部工单"],["open","处理中"],["pending","待处理"],["resolved","已解决"],["closed","已关闭"],["merged_sources","历史合并来源"]]} note="仅统计当前权限与部门范围；合并来源保留历史事实。" />
        <SummaryBlock label="钱包流水" section={summary.data.sections.wallet} fields={[["balance_cents","当前余额",true],["credits_cents","累计入钱包",true],["debits_cents","累计出钱包",true]]} note="钱包流入、消费与现金到账分别核算，不相加作为收入。" />
      </div>
      <SummaryBlock label="外部现金" section={summary.data.sections.cash} fields={[["receipts_cents","累计到账",true],["external_refunds_cents","已确认外部退款",true],["net_receipts_cents","净到账",true]]} note="按已确认的外部收退款统计，与钱包流水分开；不是利润。" />
      <div className="grid min-w-0 gap-4 md:grid-cols-2">
        <section className={panelClass}>
          <h2 className="text-base font-semibold">认证状态</h2>{!summary.data.sections.certification?.available? <p className="mt-3 text-sm text-muted-foreground">无权限查看认证状态</p>:<p className="mt-3 text-sm">{({ unverified: "未认证",pending: "待审核",approved: "已通过",rejected: "已驳回" } as Record<string,string>)[String(summary.data.sections.certification.data?.status)]||"未提供"}</p>}</section>
        <section className={panelClass}>
          <h2 className="text-base font-semibold">推荐关系</h2>{!summary.data.sections.referral?.available? <p className="mt-3 text-sm text-muted-foreground">无权限查看推荐关系</p>:!summary.data.sections.referral.data? <p className="mt-3 text-sm text-muted-foreground">关系数据未提供</p>:<p className="mt-3 break-words text-sm">{summary.data.sections.referral.data.attributed? summary.data.sections.referral.data.referrer_name? `推荐人：${String(summary.data.sections.referral.data.referrer_name)}`:"已有推荐归因；推荐人不在当前可见范围":"无当前推荐归因"}</p>}</section>
      </div>
      <section className={panelClass}>
        <h2 className="text-base font-semibold">登录信息</h2>
        <dl className="mt-3 grid gap-4 sm:grid-cols-2 text-sm">
          <IdentityField label="最后成功登录" value={summary.data.customer.last_login_at? date(String(summary.data.customer.last_login_at)):"暂无可关联的成功登录记录"} />
          <IdentityField label="账户邮箱验证" value={summary.data.customer.email_verified===true? "已验证":summary.data.customer.email_verified===false? "未验证":"未提供"} />
        </dl>
      </section>
    </>}</div>;
}
function CustomerProfile({
  customer,
  saved,
  cancelHref,
}: {
  customer: AdminCustomer;
  saved: () => void;
  cancelHref: string;
}) {
  const manage=useAdminPermission("customers.manage");
  const [name,setName]=useState(customer.display_name);
  const [email,setEmail]=useState(customer.email);
  const [status,setStatus]=useState(customer.status);
  const [profile,setProfile]=useState({
    gender: customer.gender,company_name: customer.company_name||"",phone: customer.phone_number||"",
    qq: customer.qq||"",country: customer.country,province: customer.province,address: customer.address,
    postcode: customer.postcode,source_channel: customer.source_channel,payment_method: customer.payment_method,
    language: customer.language,admin_note: customer.admin_note,receive_marketing: customer.receive_marketing,
  });
  const actor=useAdminIdentity();
  const definitions=useCustomerFieldDefinitions(true);
  const [customFields,setCustomFields]=useState<Record<string,unknown>>(customer.custom_fields||{});
  const [profileVersion,setProfileVersion]=useState(customer.profile_version);
  const mutation=useCustomerDetailMutation(actor?.id,customer.id,"profile");
  const { busy,uncertain }=mutation;
  const [message,setMessage]=useState("");
  const [latest,setLatest]=useState<AdminCustomer|null>(null);
  const snapshot={ name,email,status,profile,customFields };
  const [initial]=useState(JSON.stringify(snapshot));
  const dirty=initial!==JSON.stringify(snapshot);
  const draft=useCustomerDraft(manage&&actor? `customer-profile:${actor.id}:${customer.id}`:null,snapshot,dirty);
  const router=useRouter();
  function cancel() { if(mutation.frozen||dirty&&!window.confirm("确认取消更改并丢弃草稿？")) return; draft.leave(); router.push(cancelHref); }
  function restoreDraft() {
    if(!draft.draft) return;
    setName(draft.draft.name); setEmail(draft.draft.email); setStatus(draft.draft.status); setProfile(draft.draft.profile); setCustomFields(draft.draft.customFields); draft.clear();
  }
  async function updateVersion() {
    try { const current=await mutation.readLatest<AdminCustomer>(`/admin/customers/${customer.id}`); if(!current) return; setLatest(current); definitions.refresh(); setMessage("最新版本已读取；请核对服务器现值后明确确认覆盖范围，当前输入尚未保存。"); } catch(e) { mutation.setError((e as Error).message); }
  }
  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if(!manage||busy||!definitions.ready&&!uncertain||mutation.conflict&&!uncertain) return;
    setMessage("");
    if(uncertain) { const result=await mutation.run(`/admin/customers/${customer.id}`,"PATCH",{}); if(result) { draft.leave(); setMessage("原资料请求已核验"); saved(); } return; }
    try {
      const changedProfile: Record<string,unknown>={};
      const baseProfile={ gender: customer.gender,company_name: customer.company_name||"",phone: customer.phone_number||"",qq: customer.qq||"",country: customer.country,province: customer.province,address: customer.address,postcode: customer.postcode,source_channel: customer.source_channel,payment_method: customer.payment_method,language: customer.language,admin_note: customer.admin_note,receive_marketing: customer.receive_marketing };
      for(const field of Object.keys(profile) as (keyof typeof profile)[]) if(profile[field]!==baseProfile[field]) changedProfile[field]=profile[field];
      if(JSON.stringify(customFields)!==JSON.stringify(customer.custom_fields||{})) { changedProfile.custom_fields=customerFieldValues(definitions.fields,customFields); changedProfile.custom_fields_version=definitions.version; }
      const payload={ expected_version: profileVersion,...(name!==customer.display_name? { display_name: name }:{}),...(email!==customer.email? { email }:{}),...(status!==customer.status? { status }:{}),...(Object.keys(changedProfile).length? { profile: changedProfile }:{}) };
      const result=await mutation.run(`/admin/customers/${customer.id}`,"PATCH",uncertain? {}:payload);
      if(result) { draft.leave(); setMessage("客户资料已保存"); saved(); }
    } catch(e) { mutation.setError((e as Error).message); }
  }

  const fieldStyle="block text-sm text-muted-foreground min-w-0";
  const textFields=[
    ["company_name","所在公司"],["phone","手机"],["qq","QQ"],["country","国家"],
    ["province","省"],["address","地址"],["postcode","邮编"],["source_channel","了解途径"],
  ] as const;

  return (
    <form onSubmit={(event) => void save(event)} className="space-y-6">
      <CustomerDraftNotice available={!!draft.draft} restore={restoreDraft} discard={draft.clear} disabled={mutation.frozen} />
      <fieldset disabled={!manage||mutation.frozen} className="space-y-6">
        <section className="bg-card p-4 rounded border border-border">
          <h2 className="text-base font-semibold text-foreground mb-3">联系方式与资料</h2>
          <div className="grid sm:grid-cols-2 gap-4">
            {textFields.map(([key,label]) => <label key={key} className={fieldStyle}>{label}
              <Input className="mt-1.5 text-sm h-9 border-border" value={profile[key]} onChange={event => setProfile(previous => ({ ...previous,[key]: event.target.value }))} />
            </label>)}
            <label className={fieldStyle}>性别
              <Select value={profile.gender} onValueChange={value => setProfile(previous => ({ ...previous,gender: value }))}>
                <SelectTrigger className="mt-1.5 h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="unknown">未知</SelectItem>
                  <SelectItem value="male">男</SelectItem>
                  <SelectItem value="female">女</SelectItem>
                </SelectContent>
              </Select>
            </label>
            <label className={fieldStyle}>
              姓名
              <Input
                className="mt-1.5 text-sm h-9 border-border"
                value={name}
                required
                maxLength={120}
                onChange={(event) => setName(event.target.value)}
              />
            </label>
          </div>
        </section>



        <section className="bg-card p-4 rounded border border-border">
          <h2 className="text-sm font-semibold text-foreground mb-3">账户信息</h2>
          <div className="grid sm:grid-cols-2 gap-4">
            <label className={fieldStyle}>
              账户
              <Input
                className="mt-1.5 text-sm h-9 border-border bg-muted"
                value={customer.username}
                readOnly
                disabled
              />
            </label>
            <label className={fieldStyle}>
              邮箱
              <Input
                className="mt-1.5 text-sm h-9 border-border"
                value={email}
                type="email"
                required
                maxLength={254}
                onChange={(event) => setEmail(event.target.value)}
              />
            </label>
          </div>
        </section>

        <section className="bg-card p-4 rounded border border-border">
          <h2 className="text-sm font-semibold text-foreground mb-3">其他设置</h2>
          <div className="grid sm:grid-cols-2 gap-4">
            <label className={fieldStyle}>默认支付方式
              <Select value={profile.payment_method} onValueChange={value => setProfile(previous => ({ ...previous,payment_method: value }))}>
                <SelectTrigger className="mt-1.5 h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="bank">银行转账</SelectItem>
                  <SelectItem value="alipay">支付宝</SelectItem>
                  <SelectItem value="wechat">微信支付</SelectItem>
                </SelectContent>
              </Select>
            </label>
            <label className={fieldStyle}>语言
              <Select value={profile.language} onValueChange={value => setProfile(previous => ({ ...previous,language: value }))}>
                <SelectTrigger className="mt-1.5 h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="zh_CN">中文简体</SelectItem>
                  <SelectItem value="en_US">English</SelectItem>
                </SelectContent>
              </Select>
            </label>
            <label className={fieldStyle}>管理员备注
              <textarea className="mt-1.5 w-full rounded border border-border p-2" value={profile.admin_note} maxLength={4000} onChange={event => setProfile(previous => ({ ...previous,admin_note: event.target.value }))} />
            </label>
            <label className="flex items-center gap-2 text-xs">
              <Checkbox checked={profile.receive_marketing} onCheckedChange={value => setProfile(previous => ({ ...previous,receive_marketing: value===true }))} />接收营销信息</label>
            <label className={fieldStyle}>
              状态
              <Select value={status} onValueChange={setStatus} disabled={!manage||busy}>
                <SelectTrigger className="mt-1.5 text-sm h-9 border-border" aria-label="客户状态">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="active">正常</SelectItem>
                  <SelectItem value="disabled">已停用</SelectItem>
                </SelectContent>
              </Select>
            </label>
            <label className={fieldStyle}>
              客户分组
              <Input
                className="mt-1.5 text-sm h-9 border-border bg-muted"
                value={customer.group_name||"未分组"}
                disabled
              />
            </label>
            <label className={fieldStyle}>
              销售
              <Input
                className="mt-1.5 text-sm h-9 border-border bg-muted"
                value={customer.sales_owner_name||"无归属"}
                disabled
              />
            </label>
          </div>
        </section>
      </fieldset>

      <CustomerFieldInputs definitions={definitions} values={customFields} onChange={setCustomFields} disabled={!manage||mutation.frozen} />
      <CustomerMutationMessage mutation={mutation} />
      {message&&<p role="status" className="text-sm text-foreground">{message}</p>}
      {latest&&<section className="rounded border p-4 space-y-3">
        <h3 className="text-sm font-medium">服务器最新资料 · 版本 {latest.profile_version}</h3>
        <dl className="grid gap-3 sm:grid-cols-2 text-sm">
          <div>
            <dt className="text-muted-foreground">姓名 / 邮箱</dt>
            <dd className="break-all">{latest.display_name} / {latest.email}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">状态 / 公司</dt>
            <dd>{latest.status==="active"? "正常":"已停用"} / {latest.company_name||"未填写"}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">手机 / QQ</dt>
            <dd>{latest.phone_number||"未填写"} / {latest.qq||"未填写"}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">内部备注</dt>
            <dd className="whitespace-pre-wrap break-words">{latest.admin_note||"未填写"}</dd>
          </div>
        </dl>
        <p className="text-sm text-muted-foreground">继续保存会应用当前表单中已修改的字段；请先与现值核对。</p>
        <Button type="button" variant="outline" disabled={busy} onClick={async () => { if(!await mutation.acknowledgeConflict()) return; setProfileVersion(latest.profile_version); setLatest(null); setMessage("已确认使用最新版本；请再次核对表单后保存。"); }}>已核对，使用该版本</Button>
      </section>}
      {mutation.conflict&&<Button type="button" variant="outline" disabled={busy} onClick={() => void updateVersion()}>读取最新版本，保留当前输入</Button>}

      <div className="flex flex-wrap gap-3 pt-2">
        <PermissionGate permission="customers.manage">
          <Button
            disabled={!mutation.ready||busy||mutation.conflict||!dirty&&!uncertain||!definitions.ready&&!uncertain}
            size="sm"
            className="bg-primary hover:bg-primary/90 text-white text-xs"
          >
            <Save size={13} className="mr-1" />
            {uncertain? "使用原请求重试":busy? "保存中...":"保存更改"}
          </Button>
        </PermissionGate>
        <Button
          variant="outline"
          size="sm"
          type="button"
          disabled={mutation.frozen}
          onClick={cancel}
          className="text-xs border-border"
        >
          取消更改
        </Button>
      </div>
    </form>
  );
}


type RelatedTab=Exclude<CustomerDetailTab,"summary"|"profile"|"files"|"notifications">;
const relatedPermission: Record<RelatedTab,string>={ services: "services.view",invoices: "invoices.view",transactions: "invoices.view",tickets: "tickets.view",logs: "audit.view",affiliate: "customers.view" };
function CustomerRelatedPanel({ customerId, customerName, tab, initialOrders }: { customerId: number; customerName: string; tab: RelatedTab; initialOrders: boolean }) {
  if (tab === "invoices") return <section className={panelClass}><CustomerBilling customerId={customerId} customerName={customerName} initialOrders={initialOrders} /></section>;
  if (tab === "affiliate") return <CustomerAffiliate customerId={customerId} />;
  return <PermissionSection permission={relatedPermission[tab]} label={customerDetailTabLabel[tab]}>
    <section className={panelClass}>
      {tab==="tickets"&&<div className="mb-4 flex flex-wrap justify-end gap-2"><CustomerBusinessCreateActions customerId={customerId} kind="tickets" /></div>}
      <CustomerHistoryPage customerId={customerId} kind={tab} />
    </section>
  </PermissionSection>;
}
function CustomerAffiliate({ customerId }: { customerId: number }) {
  const [revision,setRevision]=useState(0);
  return <div className="min-w-0 space-y-4">
    <CustomerReferrerPanel customerId={customerId} onChanged={() => setRevision(value=>value+1)} />
    <PermissionSection permission="customers.view" label="推广活动"><section className={panelClass}>
      <CustomerHistoryPage key={revision} customerId={customerId} kind="affiliate" />
    </section></PermissionSection>
  </div>;
}
function CustomerBilling({ customerId, customerName, initialOrders }: { customerId: number; customerName: string; initialOrders: boolean }) {
  const viewInvoices = useAdminPermission("invoices.view");
  const viewOrders = useAdminPermission("orders.view");
  const [preferOrders, setPreferOrders] = useState(initialOrders);
  const [revision,setRevision]=useState(0);
  const orders = viewOrders && (!viewInvoices || preferOrders);
  if (!viewInvoices && !viewOrders) return <NoAccess label="账单与关联订单" />;
  return <div className="min-w-0 space-y-4">
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div className="flex gap-2" role="group" aria-label="账单关联记录">
        <Button variant={orders ? "outline" : "default"} disabled={!viewInvoices} aria-pressed={!orders} onClick={() => { if (viewInvoices) setPreferOrders(false); }}>账单</Button>
        <Button variant={orders ? "default" : "outline"} disabled={!viewOrders} aria-pressed={orders} onClick={() => { if (viewOrders) setPreferOrders(true); }}>关联订单</Button>
      </div>
      <div className="flex flex-wrap gap-2">
        <CustomerBusinessCreateActions customerId={customerId} kind="billing" />
        <CustomerTopupButton customerId={customerId} customerName={customerName} onCreated={() => { setPreferOrders(false); setRevision(value=>value+1); }} />
      </div>
    </div><CustomerHistoryPage key={`${orders ? "orders" : "invoices"}:${revision}`} customerId={customerId} kind={orders ? "orders" : "invoices"} /></div>;
}

type HistoryKind=RelatedTab|"orders"|"delivery"|"email"|"sms"|"inbox";
type HistoryFilters={ q: string; status: string; from: string; to: string; channel: string };
function CustomerHistoryPage({ customerId,kind }: { customerId: number; kind: HistoryKind }) {
  const [page,setPage]=useState(0);
  const [draft,setDraft]=useState<HistoryFilters>({ q: "",status: "",from: "",to: "",channel: "" });
  const [filter,setFilter]=useState(draft);
  const [filterError,setFilterError]=useState("");
  const params=new URLSearchParams({ customer_id: String(customerId) });
  for(const [key,value] of Object.entries(filter)) if(value) params.set(key,value);
  if(kind==="logs") params.set("kind","system");
  const filterQuery=params.toString();
  params.set("limit","25"); params.set("offset",String(page*25));
  const root=kind==="transactions"? "/admin/transactions/page":kind==="logs"? "/admin/logs":kind==="affiliate"? `/admin/customers/${customerId}/affiliate/page`:kind==="delivery"? "/admin/notification-deliveries/page":["email","sms","inbox"].includes(kind)? `/admin/logs/${kind}`:`/admin/${kind}/page`;
  // Affiliate's customer identity is bound in its path and its query accepts pagination only.
  const query=kind==="affiliate"? `limit=25&offset=${page*25}`:params.toString();
  const resource=useResource<Page>(`${root}?${query}`,false);
  const cash=useResource<Row>(kind==="transactions"? `/admin/transactions/summary?${filterQuery}`:null,false);
  const dateFilters=kind==="transactions"||kind==="logs"||["delivery","email","sms","inbox"].includes(kind);
  const statusOptions: [string,string][]=kind==="services"? [["active","使用中"],["suspended","已暂停"],["pending_payment","待支付"],["queued","排队中"],["provisioning","开通中"],["failed","失败"],["terminated","已终止"],["cancelled","已取消"],["import_review_required","待核验"]]:kind==="tickets"? [["open","处理中"],["pending","待处理"],["resolved","已解决"],["closed","已关闭"]]:kind==="delivery"? [["pending","待投递"],["sending","投递中"],["accepted","渠道已接受"],["failed","投递失败"],["unknown","待核验"],["skipped","已跳过"],["archived","已归档"]]:["email","sms"].includes(kind)? [["pending","待发送"],["sending","发送中"],["sent","已发送"],["expired","已过期"],["dead_letter","死信"]]:kind==="inbox"? [["sent","已发送"],["read","已读"],["failed","失败"]]:kind==="invoices"? [["unpaid","待支付"],["paid","已支付"],["cancelled","已取消"],["refunded","已退款"]]:kind==="orders"? [["pending","待支付"],["paid","已支付"],["active","已生效"],["cancelled","已取消"]]:[];
  function apply(event: React.FormEvent) { event.preventDefault(); if(draft.from&&draft.to&&draft.from>draft.to) { setFilterError("开始日期不能晚于结束日期"); return; } setFilterError(""); setFilter({ ...draft,q: draft.q.trim() }); setPage(0); }
  return <div className="min-w-0 space-y-4">
    <div className="flex flex-wrap items-center justify-between gap-3">
      <h2 className="text-base font-semibold">{historyLabel(kind)}</h2>
      <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={resource.isLoading} onClick={() => { resource.refresh(); cash.refresh(); }}>
          <RefreshCw size={14} />刷新本区域</Button>
      </div>
    </div>
    {kind!=="affiliate"&&<form onSubmit={apply} className="flex flex-wrap items-end gap-3 rounded border bg-muted/20 p-3">{kind!=="delivery"&&<label className="min-w-0 flex-1 basis-44 text-sm">关键词<Input className="mt-1" value={draft.q} maxLength={120} onChange={e => setDraft({ ...draft,q: e.target.value })} placeholder="按本区域支持的字段搜索" />
    </label>}{statusOptions.length>0&&<label className="w-full text-sm sm:w-36">状态<select aria-label="筛选状态" className="mt-1 h-9 w-full rounded border bg-background px-2" value={draft.status} onChange={e => setDraft({ ...draft,status: e.target.value })}>
      <option value="">全部状态</option>{statusOptions.map(([key,label]) => <option key={key} value={key}>{label}</option>)}</select>
    </label>}{kind==="delivery"&&<label className="w-full text-sm sm:w-36">渠道<select className="mt-1 h-9 w-full rounded border bg-background px-2" value={draft.channel} onChange={e => setDraft({ ...draft,channel: e.target.value })}>
      <option value="">全部渠道</option>{["email","sms","inbox","telegram","web_push"].map(channel => <option key={channel} value={channel}>{channelLabels[channel]}</option>)}</select>
    </label>}{dateFilters&&<>
      <label className="min-w-0 text-sm">开始日期<Input className="mt-1 w-full" type="date" value={draft.from} onChange={e => setDraft({ ...draft,from: e.target.value })} />
      </label>
      <label className="min-w-0 text-sm">结束日期<Input className="mt-1 w-full" type="date" value={draft.to} onChange={e => setDraft({ ...draft,to: e.target.value })} />
      </label>
    </>}<Button type="submit">查询</Button>
      <Button type="button" variant="outline" onClick={() => { const empty={ q: "",status: "",from: "",to: "",channel: "" }; setDraft(empty); setFilter(empty); setFilterError(""); setPage(0); }}>清空条件</Button>
    </form>}
    {filterError&&<p role="alert" className="text-sm text-destructive">{filterError}</p>}
    {kind==="transactions"&&<section className="rounded border p-4">
      <h3 className="text-sm font-medium">当前条件的现金合计 · CNY</h3>{cash.error? <DetailFailure error={cash.error} retry={cash.refresh} />:!cash.data? <Loading />:<>
        <dl className="mt-3 grid gap-4 sm:grid-cols-3">{[["cash_income_cents","外部到账"],["external_refund_cents","外部退款"],["net_cash_cents","净现金到账"]].map(([key,label]) => <div key={key}>
          <dt className="text-sm text-muted-foreground">{label}</dt>
          <dd className="mt-1 font-semibold tabular-nums">{renderValue(cash.data?.[key],true)}</dd>
        </div>)}</dl>
        <p className="mt-3 text-xs text-muted-foreground">与列表使用相同筛选；钱包消费单列，不加入现金合计。</p>
      </>}</section>}
    {kind==="delivery"&&<p className="text-sm text-muted-foreground">渠道已接受不代表客户已读；待核验不提供自动重发。此列表不含全部工单或营销历史。</p>}
    {kind==="logs"&&<p className="text-xs text-muted-foreground">按已知业务对象关联客户；空记录不表示客户从未发生任何活动。</p>}
    {resource.error? <DetailFailure error={resource.error} retry={resource.refresh} />:resource.isLoading||!resource.data? <Loading />:<>
      <HistoryTable kind={kind} rows={resource.data.items} />
      <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-3 text-sm">
        <span className="text-muted-foreground">共 {resource.data.total} 条 · 第 {page+1} 页</span>
        <div className="flex gap-2">
          <Button variant="outline" disabled={page===0} onClick={() => setPage(value => value-1)}>上一页</Button>
          <Button variant="outline" disabled={(page+1)*25>=resource.data.total} onClick={() => setPage(value => value+1)}>下一页</Button>
        </div>
      </div>
    </>}
  </div>;
}
const channelLabels: Record<string,string>={ email: "邮件",sms: "短信",inbox: "站内信",telegram: "Telegram",web_push: "浏览器推送" };
function historyLabel(kind: HistoryKind) { return ({ ...customerDetailTabLabel,orders: "关联订单",delivery: "统一业务通知",email: "历史邮件",sms: "历史短信",inbox: "历史站内信" } as Record<string,string>)[kind]; }
function HistoryTable({ kind,rows }: { kind: HistoryKind; rows: Row[] }) {
  const columns: [string,string][]=kind==="services"? [["name","服务"],["domain","域名"],["status","状态"],["next_due_at","下次到期"]]:kind==="orders"? [["order_no","订单号"],["product_names","商品"],["total_cents","净应付"],["status","状态"],["created_at","创建时间"]]:kind==="invoices"? [["invoice_no","账单号"],["invoice_kind","类型"],["amount_cents","应付"],["status","状态"],["due_at","到期时间"],["paid_at","支付时间"]]:kind==="tickets"? [["ticket_no","工单号"],["subject","主题"],["status","状态"],["updated_at","更新时间"]]:kind==="transactions"? [["source","流水来源"],["invoice_no","关联账单"],["channel","渠道"],["direction","方向"],["amount_cents","金额"],["occurred_at","时间"]]:kind==="logs"? [["created_at","时间"],["action","动作"],["entity_type","对象类型"],["entity_id","对象编号"],["actor_name","操作者"]]:kind==="delivery"? [["event_kind","业务事件"],["channel","渠道"],["status","投递状态"],["attempts","尝试次数"],["code","结果码"],["created_at","时间"]]:kind==="affiliate"? [["kind","活动类型"],["invoice_no","关联账单"],["label","项目"],["amount_cents","金额"],["status","状态"],["created_at","时间"]]:[["created_at","时间"],[kind==="sms"? "phone":kind==="inbox"? "subject":"recipient",kind==="sms"? "接收手机":kind==="inbox"? "主题":"接收地址"],[kind==="email"? "subject":kind==="sms"? "kind":"read_at",kind==="email"? "主题":kind==="sms"? "类型":"已读时间"],["status","状态"]];
  function cell(row: Row,key: string) {
    const value=row[key];
    const display=value===null||value===undefined||value===""? "—":String(value);
    const href=kind==="services"&&key==="name"? `/admin/services/${Number(row.id)}`:kind==="orders"&&key==="order_no"? `/admin/orders/${Number(row.id)}`:key==="invoice_no"&&row.invoice_id? `/admin/invoices/${Number(row.invoice_id)}`:kind==="invoices"&&key==="invoice_no"? `/admin/invoices/${Number(row.id)}`:kind==="tickets"&&key==="ticket_no"? `/admin/tickets/${Number(row.id)}`:null;
    if(href) return <Link className="break-words text-primary hover:underline" href={href}>{display}</Link>;
    if(key.endsWith("_cents")) return <span className="whitespace-nowrap tabular-nums">{renderValue(value,true)}</span>;
    if(key.endsWith("_at")) return value? date(String(value)):"—";
    if(key==="status") return kind==="delivery"? ({ pending: "待投递",sending: "投递中",accepted: "渠道已接受",failed: "投递失败",unknown: "待核验",skipped: "已跳过",archived: "已归档" } as Record<string,string>)[String(value)]||display:<Status value={String(value||"unknown")} />;
    if(key==="channel") return channelLabels[String(value)]||display;
    if(key==="direction") return ({ income: "流入",expense: "流出",neutral: "账户事件" } as Record<string,string>)[String(value)]||display;
    if(key==="source") return ({ payment: "网关到账",manual_receipt: "人工到账",external_refund: "外部退款",ledger: "钱包流水",credit_consumption: "历史账户消费",credit_repayment: "历史还款",invoice: "未记录来源账单" } as Record<string,string>)[String(value)]||display;
    if(key==="kind"&&kind==="affiliate") return ({ affiliate_account: "推广账户",affiliate_attribution: "推荐归因",affiliate_commission: "佣金",affiliate_withdrawal: "提现" } as Record<string,string>)[String(value)]||display;
    return <span className="break-words">{display}</span>;
  }
  if(!rows.length) return <div className="rounded border border-dashed px-4 py-10 text-center text-sm text-muted-foreground">当前客户与筛选范围内暂无{historyLabel(kind)}记录。</div>;
  return <div className="min-w-0 max-w-full overflow-x-auto rounded border">
    <Table>
      <TableHeader>
        <TableRow className="bg-muted/40">{columns.map(([key,title]) => <TableHead key={key} className="whitespace-nowrap text-xs">{title}</TableHead>)}</TableRow>
      </TableHeader>
      <TableBody>{rows.map((row,index) => <TableRow key={String(row.event_key||`${row.kind||kind}:${row.id||index}`)}>{columns.map(([key]) => <TableCell key={key} className="max-w-72 text-sm">{cell(row,key)}</TableCell>)}</TableRow>)}</TableBody>
    </Table>
  </div>;
}
function CustomerNotifications({ customerId }: { customerId: number }) {
  const [kind,setKind]=useState<"delivery"|"email"|"sms"|"inbox">("delivery");
  return <section className={`${panelClass} space-y-4`}>
    <div className="flex flex-wrap gap-2" role="group" aria-label="通知来源">{(["delivery","email","sms","inbox"] as const).map(value => <Button key={value} variant={kind===value? "default":"outline"} onClick={() => setKind(value)}>{historyLabel(value)}</Button>)}</div>
    <CustomerHistoryPage key={kind} kind={kind} customerId={customerId} />
  </section>;
}
export function ContactsPanel({ adminCustomerId }: { adminCustomerId?: number }) {
  const actor=useAdminIdentity();
  const canManage=useAdminPermission("customers.manage");
  const writeAllowed=!adminCustomerId||canManage;
  const root=adminCustomerId? `/admin/customers/${adminCustomerId}/contacts`:"/contacts";
  const { data,error,refresh }=useResource<Contact[]>(root);
  const [editing,setEditing]=useState<number|null>(null);
  const [editingVersion,setEditingVersion]=useState<number|null>(null);
  const [latestContact,setLatestContact]=useState<Contact|null>(null);
  const [username,setUsername]=useState("");
  const [email,setEmail]=useState("");
  const [companyName,setCompanyName]=useState("");
  const [phone,setPhone]=useState("");
  const [status,setStatus]=useState<"active"|"inactive"|"disabled">("inactive");
  const [invoiceEmails,setInvoiceEmails]=useState(false);
  const [supportEmails,setSupportEmails]=useState(false);
  const [message,setMessage]=useState("");
  const mutation=useCustomerDetailMutation(actor?.id,adminCustomerId||actor?.id||0,"contacts",!!adminCustomerId);
  const { busy }=mutation;

  function reset() {
    setEditing(null);
    setEditingVersion(null);
    setLatestContact(null);
    setUsername("");
    setEmail("");
    setCompanyName("");
    setPhone("");
    setStatus("inactive");
    setInvoiceEmails(false);
    setSupportEmails(false);
  }

  async function edit(contact: Contact) {
    if(!await mutation.acknowledgeConflict()) return;
    setEditing(contact.id);
    setEditingVersion(contact.version);
    setLatestContact(null);
    setUsername(contact.username);
    setEmail(contact.email);
    setCompanyName(contact.company_name);
    setPhone(contact.phone_number);
    setStatus(contact.status);
    setInvoiceEmails(contact.invoice_emails);
    setSupportEmails(contact.support_emails);
    setMessage("");
  }

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if(!writeAllowed||busy||mutation.conflict) return;
    setMessage("");
    try {
      const body={
        ...(editing? { expected_version: editingVersion }:{}),
        username,
        email,
        company_name: companyName,
        phone_number: phone,
        status,
        invoice_emails: invoiceEmails,
        support_emails: supportEmails,
      };
      const result=await mutation.run(editing? `${root}/${editing}`:root,editing? "PATCH":"POST",body);
      if(result) { reset(); refresh(); }
    } catch(e) {
      setMessage((e as Error).message);
    }
  }

  async function remove(id: number) {
    if(!writeAllowed||busy) return;
    setMessage("");
    try {
      const contact=data?.find((item) => item.id===id);
      if(!contact) throw new Error("联系人已变更，请刷新后重试");
      if(!mutation.uncertain&&!window.confirm(`确认删除联系人“${contact.username||contact.email}”？`)) return;
      const result=await mutation.run(`${root}/${id}`,"DELETE",{ expected_version: contact.version });
      if(result) { if(editing===id) reset(); refresh(); }
    } catch(e) {
      setMessage((e as Error).message);
    }
  }

  return (
    <section className="space-y-4">
      <div>
        <h2 className="text-sm font-semibold text-foreground">联系人管理</h2>
        <p className="text-xs text-muted-foreground mt-0.5">
          维护账单和工单通知联系人。联系人邮箱不能与账户重复。
        </p>
      </div>

      <PermissionGate permission="customers.manage" bypass={!adminCustomerId}>
        <form
          className="grid grid-cols-1 sm:grid-cols-2 gap-3 bg-card p-4 rounded border border-border"
          onSubmit={submit}
        >
          <fieldset disabled={mutation.frozen} className="contents">
            <Input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="姓名"
              aria-label="联系人姓名"
              maxLength={120}
              className="text-xs h-8 border-border"
            />
            <Input
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              type="email"
              placeholder="邮箱"
              aria-label="联系人邮箱"
              required
              className="text-xs h-8 border-border"
            />
            <Input
              value={companyName}
              onChange={(e) => setCompanyName(e.target.value)}
              placeholder="公司"
              aria-label="联系人公司"
              className="text-xs h-8 border-border"
            />
            <Input
              value={phone}
              onChange={(e) => setPhone(e.target.value)}
              placeholder="电话"
              aria-label="联系人电话"
              className="text-xs h-8 border-border"
            />
            <Select value={status} onValueChange={(v) => setStatus(v as typeof status)}>
              <SelectTrigger className="text-xs h-8 border-border" aria-label="联系人状态">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="active">启用</SelectItem>
                <SelectItem value="inactive">未激活</SelectItem>
                <SelectItem value="disabled">停用</SelectItem>
              </SelectContent>
            </Select>
            <label className="flex items-center gap-2 text-xs text-muted-foreground">
              <Checkbox
                checked={invoiceEmails}
                onCheckedChange={(v) => setInvoiceEmails(v===true)}
              />
              接收账单通知
            </label>
            <label className="flex items-center gap-2 text-xs text-muted-foreground">
              <Checkbox
                checked={supportEmails}
                onCheckedChange={(v) => setSupportEmails(v===true)}
              />
              接收工单通知
            </label>
            <div className="flex items-center gap-2">
              <Button
                disabled={mutation.frozen||mutation.conflict||!email.trim()}
                size="sm"
                className="bg-primary hover:bg-primary/90 text-white text-xs h-8"
              >
                {editing? "保存联系人":"添加联系人"}
              </Button>
              {editing&&(
                <Button type="button" variant="ghost" size="sm" onClick={reset} className="text-xs h-8">
                  取消
                </Button>
              )}
            </div>
          </fieldset>
        </form>
      </PermissionGate>

      <CustomerMutationMessage mutation={mutation} />
      {mutation.conflict&&<div className="space-y-3 rounded border p-3 text-sm">
        <p>联系人已变更；当前输入保留，请读取当前联系人后核对。</p>
        <Button type="button" variant="outline" disabled={busy} onClick={() => void mutation.readLatest<Contact[]>(root).then(items => { if(!items) return; refresh(); const item=items.find(value => value.id===editing); if(item) setLatestContact(item); else setMessage("当前联系人已不存在或当前操作为删除，请核对列表后选择原联系人。"); }).catch(error => setMessage((error as Error).message))}>读取最新联系人</Button>{latestContact&&<>
          <p className="break-words">服务器现值：{latestContact.username||"未命名"} · {latestContact.email} · {latestContact.phone_number||"未填写手机"} · 版本 {latestContact.version}</p>
          <Button type="button" variant="outline" onClick={async () => { if(!await mutation.acknowledgeConflict()) return; setEditingVersion(latestContact.version); setLatestContact(null); }}>已核对，保留输入并使用该版本</Button>
        </>}</div>}
      {mutation.uncertain&&<PermissionGate permission="customers.manage" bypass={!adminCustomerId}>
        <Button variant="outline" disabled={busy||!writeAllowed} onClick={() => { if(writeAllowed) void mutation.run(root,"POST",{}).then(result => { if(result) { reset(); refresh(); } }); }}>使用原联系人请求重试</Button>
      </PermissionGate>}
      {message&&<p role="alert" className="text-xs text-destructive">{message}</p>}
      {error? (
        <p role="alert" className="text-xs text-destructive">{error.message}</p>
      ):!data? (
        <Loading />
      ):(
        <div className="overflow-x-auto border border-border rounded">
          <Table>
            <TableHeader className="bg-muted">
              <TableRow className="border-border">
                <TableHead className="text-xs text-muted-foreground font-medium py-2.5">姓名</TableHead>
                <TableHead className="text-xs text-muted-foreground font-medium py-2.5">邮箱</TableHead>
                <TableHead className="text-xs text-muted-foreground font-medium py-2.5">电话</TableHead>
                <TableHead className="text-xs text-muted-foreground font-medium py-2.5">状态</TableHead>
                <TableHead className="text-xs text-muted-foreground font-medium py-2.5">通知</TableHead>
                <TableHead className="text-xs text-muted-foreground font-medium py-2.5 text-right" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.length===0? (
                <TableRow>
                  <TableCell colSpan={6} className="h-20 text-center text-xs text-muted-foreground">
                    暂无联系人
                  </TableCell>
                </TableRow>
              ):(
                data.map((contact) => (
                  <TableRow key={contact.id} className="border-border text-xs hover:bg-muted">
                    <TableCell>{contact.username||"未命名"}</TableCell>
                    <TableCell>{contact.email}</TableCell>
                    <TableCell>{contact.phone_number||"-"}</TableCell>
                    <TableCell>
                      <Status value={contact.status} />
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {[contact.invoice_emails&&"账单",contact.support_emails&&"工单"]
                        .filter(Boolean)
                        .join("、")||"无"}
                    </TableCell>
                    <TableCell className="text-right">
                      <PermissionGate permission="customers.manage" bypass={!adminCustomerId}>
                        <div className="flex items-center justify-end gap-1.5">
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={mutation.frozen} onClick={() => edit(contact)}
                            className="h-7 text-xs border-border"
                          >
                            编辑
                          </Button>
                          <Button
                            size="icon-sm"
                            variant="ghost"
                            aria-label="删除联系人"
                            title="删除联系人"
                            disabled={mutation.frozen}
                            onClick={() => void remove(contact.id)}
                            className="h-7 w-7 text-destructive hover:text-destructive"
                          >
                            <Trash2 size={13} />
                          </Button>
                        </div>
                      </PermissionGate>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      )}
    </section>
  );
}
