"use client";

import { cloneElement, isValidElement, useId, useState } from "react";
import { useRouter } from "next/navigation";
import { ArrowLeft, Eye, EyeOff, Save, UserRound } from "lucide-react";
import { type CustomerGroup } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Checkbox } from "./ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "./ui/dialog";
import { Workspace, useAdminIdentity, useAdminPermission } from "./workspace";
import { useCustomerCreateMutation } from "./admin-customer-create-mutation";
import { DirectoryOptionState, DirectorySelect, customerDirectoryActorKey } from "./admin-customer-directory-filters";
import { CustomerFieldInputs, customerFieldValues, useCustomerFieldDefinitions, useCustomerDraft, CustomerDraftNotice, customerListSuffix } from "./customer-field-inputs";

type SalesOwner = { id: number; username: string; display_name: string };
type CreateResult = { id: number };
const inputClass = "h-9 rounded border-border bg-card";

export function AdminCustomerCreateView() {
  const actor = useAdminIdentity();
  const manage = useAdminPermission("customers.manage");
  const mutation = useCustomerCreateMutation(actor?.id, manage);
  return <Workspace title="新建客户" admin><CustomerCreateForm key={customerDirectoryActorKey(actor)} mutation={mutation} /></Workspace>;
}

function CustomerCreateForm({ mutation }: { mutation: ReturnType<typeof useCustomerCreateMutation> }) {
  const router = useRouter();
  const actor = useAdminIdentity();
  const manage = useAdminPermission("customers.manage");
  const groupAllowed = useAdminPermission("customer_groups.manage");
  const groupDirectoryAllowed = useAdminPermission("customer_groups.view");
  const salesAllowed = useAdminPermission("@super");
  const groups = useResource<CustomerGroup[]>(manage && groupAllowed && groupDirectoryAllowed ? "/admin/customer-groups?options=true" : null, false);
  const sales = useResource<SalesOwner[]>(manage && salesAllowed ? "/admin/sales-owners" : null, false);
  const definitions = useCustomerFieldDefinitions(manage);
  const [username, setUsername] = useState("");
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [status, setStatus] = useState("active");
  const [gender, setGender] = useState("unknown");
  const [company, setCompany] = useState("");
  const [phone, setPhone] = useState("");
  const [qq, setQq] = useState("");
  const [country, setCountry] = useState("");
  const [province, setProvince] = useState("");
  const [address, setAddress] = useState("");
  const [postcode, setPostcode] = useState("");
  const [channel, setChannel] = useState("");
  const [paymentMethod, setPaymentMethod] = useState("bank");
  const [language, setLanguage] = useState("zh_CN");
  const [customerGroup, setCustomerGroup] = useState("");
  const [salesOwner, setSalesOwner] = useState("");
  const [adminNote, setAdminNote] = useState("");
  const [receiveMarketing, setReceiveMarketing] = useState(true);
  const [customFields, setCustomFields] = useState<Record<string, unknown>>({});
  const [preview, setPreview] = useState<Record<string, unknown> | null>(null);
  const snapshot = { username, name, email, status, gender, company, phone, qq, country, province, address, postcode, channel, paymentMethod, language, customerGroup, salesOwner, adminNote, receiveMarketing, customFields };
  const [initial] = useState(JSON.stringify(snapshot));
  const dirty = initial !== JSON.stringify(snapshot) || !!password;
  const draft = useCustomerDraft(manage && actor ? `customer-create:v2:${actor.id}` : null, snapshot, dirty);

  function restoreDraft() {
    const value = draft.draft;
    if (!value) return;
    setUsername(value.username); setName(value.name); setEmail(value.email); setStatus(value.status);
    setGender(value.gender); setCompany(value.company); setPhone(value.phone); setQq(value.qq);
    setCountry(value.country); setProvince(value.province); setAddress(value.address); setPostcode(value.postcode);
    setChannel(value.channel); setPaymentMethod(value.paymentMethod); setLanguage(value.language);
    setCustomerGroup(value.customerGroup); setSalesOwner(value.salesOwner); setAdminNote(value.adminNote);
    setReceiveMarketing(value.receiveMarketing); setCustomFields(value.customFields); setPassword(""); draft.clear();
  }
  function cancel() {
    if (mutation.frozen || dirty && !window.confirm("确认取消新建客户并丢弃草稿？")) return;
    draft.leave(); router.push(`/admin/customers${customerListSuffix()}`);
  }
  function success(result: CreateResult) {
    draft.leave(); setPassword(""); setPreview(null);
    router.push(`/admin/customers/${result.id}${customerListSuffix()}`); router.refresh();
  }
  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!manage || mutation.busy) return;
    if (mutation.uncertain) {
      const result = await mutation.run({});
      if (result) success(result);
      return;
    }
    mutation.setError("");
    if (!definitions.ready) { mutation.setError("请先完成客户字段定义加载。"); return; }
    const bytes = (value: string) => new TextEncoder().encode(value).length;
    const normalizedUsername = username.trim().toLowerCase() || email.trim().split("@")[0].toLowerCase();
    if (bytes(normalizedUsername) < 2 || bytes(normalizedUsername) > 64) { mutation.setError("用户名需为2–64字节；留空时请检查邮箱前缀长度。"); return; }
    if (bytes(name.trim()) > 120) { mutation.setError("显示名不能超过120字节。"); return; }
    if (bytes(password) < 8 || bytes(password) > 72) { mutation.setError("密码长度需为8–72字节，中文字符通常占多个字节。"); return; }
    let fields: Record<string, unknown>;
    try { fields = customerFieldValues(definitions.fields, customFields); }
    catch (error) { mutation.setError((error as Error).message); return; }
    const payload: Record<string, unknown> = {
      email: email.trim(), password, status, gender,
      company_name: company.trim(), phone: phone.trim(), qq: qq.trim(), country: country.trim(),
      province: province.trim(), address: address.trim(), postcode: postcode.trim(), source_channel: channel.trim(),
      payment_method: paymentMethod, language, admin_note: adminNote.trim(), receive_marketing: receiveMarketing,
      custom_fields: fields, custom_fields_version: definitions.version,
    };
    if (username.trim()) payload.username = username.trim();
    if (name.trim()) payload.display_name = name.trim();
    if (groupAllowed && customerGroup) payload.group_id = Number(customerGroup);
    if (salesAllowed && salesOwner) payload.sales_owner_id = Number(salesOwner);
    setPreview(payload);
  }
  async function confirmCreate() {
    if (!preview || mutation.busy || !manage) return;
    const result = await mutation.run(preview);
    if (result) success(result);
    else {
      setPreview(null);
      // A definite rejection allows correction; unknown results keep the exact
      // payload and password in the existing mutation hook's memory only.
    }
  }

  if (!manage) return <p role="alert" className="rounded border border-border bg-card p-5">没有新建客户权限。</p>;
  const frozen = mutation.frozen || preview !== null;
  return <div className="min-w-0 space-y-5 text-foreground">
    <div className="flex flex-wrap items-start gap-3"><Button type="button" variant="outline" className="rounded shadow-none" aria-label="返回客户列表" disabled={mutation.frozen} onClick={cancel}><ArrowLeft size={16} />返回列表</Button><div><h1 className="text-[22px] font-semibold leading-[30px]">新建客户</h1><p className="mt-1 text-sm text-muted-foreground">创建普通客户账号，完成后进入客户工作区。</p></div></div>
    <CustomerDraftNotice available={!!draft.draft} restore={restoreDraft} discard={draft.clear} disabled={frozen} />
    <form onSubmit={submit} className="space-y-5">
      <fieldset disabled={frozen} className="grid min-w-0 items-start gap-5 xl:grid-cols-[minmax(0,1fr)_300px]">
        <div className="min-w-0 space-y-5">
          <CreateSection title="账号信息" description="邮箱和密码为必填项，用户名与显示名可留空自动生成。">
            <div className="grid min-w-0 gap-4 sm:grid-cols-2">
              <CreateField label="邮箱" required><Input type="email" autoComplete="off" required maxLength={254} value={email} onChange={event => setEmail(event.target.value)} placeholder="customer@example.com" className={inputClass} /></CreateField>
              <CreateField label="用户名" hint="留空时使用邮箱 @ 前的内容，需为2–64字节。"><Input autoComplete="off" maxLength={64} value={username} onChange={event => setUsername(event.target.value)} placeholder="可选" className={inputClass} /></CreateField>
              <CreateField label="显示名" hint="留空时使用用户名。"><Input maxLength={120} value={name} onChange={event => setName(event.target.value)} placeholder="客户姓名或称呼" className={inputClass} /></CreateField>
              <CreateField id="new-customer-password" label="初始密码" required hint="8–72字节；密码不保存到浏览器草稿。"><div className="relative"><Input id="new-customer-password" type={showPassword ? "text" : "password"} autoComplete="new-password" required value={password} onChange={event => setPassword(event.target.value)} className={`${inputClass} pr-10`} /><Button type="button" variant="ghost" size="icon-sm" className="absolute top-0.5 right-1" aria-label={showPassword ? "隐藏密码" : "显示密码"} onClick={() => setShowPassword(!showPassword)}>{showPassword ? <EyeOff size={16} /> : <Eye size={16} />}</Button></div></CreateField>
              <DirectorySelect allowEmpty={false} label="账号状态" value={status} onChange={setStatus} placeholder="请选择" options={[{ value: "active", label: "已启用" }, { value: "disabled", label: "已停用" }]} />
            </div>
          </CreateSection>
          <CreateSection title="客户资料" description="资料手机号用于联系，不代表已验证的登录手机号。">
            <div className="grid min-w-0 gap-4 sm:grid-cols-2">
              <CreateField label="公司"><Input maxLength={200} value={company} onChange={event => setCompany(event.target.value)} className={inputClass} /></CreateField>
              <DirectorySelect allowEmpty={false} label="性别" value={gender} onChange={setGender} placeholder="请选择" options={[{ value: "unknown", label: "未知" }, { value: "male", label: "男" }, { value: "female", label: "女" }]} />
              <CreateField label="资料手机号"><Input type="tel" maxLength={80} value={phone} onChange={event => setPhone(event.target.value)} placeholder="可含国家或地区区号" className={inputClass} /></CreateField>
              <CreateField label="QQ"><Input maxLength={40} value={qq} onChange={event => setQq(event.target.value)} className={inputClass} /></CreateField>
              <CreateField label="国家或地区"><Input maxLength={100} value={country} onChange={event => setCountry(event.target.value)} className={inputClass} /></CreateField>
              <CreateField label="省份"><Input maxLength={100} value={province} onChange={event => setProvince(event.target.value)} className={inputClass} /></CreateField>
              <CreateField label="详细地址"><Input maxLength={500} value={address} onChange={event => setAddress(event.target.value)} className={inputClass} /></CreateField>
              <CreateField label="邮编"><Input maxLength={40} value={postcode} onChange={event => setPostcode(event.target.value)} className={inputClass} /></CreateField>
              <CreateField label="了解途径"><Input maxLength={200} value={channel} onChange={event => setChannel(event.target.value)} className={inputClass} /></CreateField>
              <DirectorySelect allowEmpty={false} label="偏好支付方式" value={paymentMethod} onChange={setPaymentMethod} placeholder="请选择" options={[{ value: "bank", label: "银行转账" }, { value: "alipay", label: "支付宝" }, { value: "wechat", label: "微信支付" }]} />
              <DirectorySelect allowEmpty={false} label="语言" value={language} onChange={setLanguage} placeholder="请选择" options={[{ value: "zh_CN", label: "简体中文" }, { value: "en_US", label: "English" }]} />
              <div className="flex items-center gap-2 self-end pb-2 text-sm"><Checkbox id="customer-marketing" checked={receiveMarketing} onCheckedChange={checked => setReceiveMarketing(checked === true)} /><label htmlFor="customer-marketing">接收营销邮件</label></div>
              <div className="sm:col-span-2"><CreateField label="内部备注" hint="仅后台使用。"><Textarea maxLength={4000} rows={3} value={adminNote} onChange={event => setAdminNote(event.target.value)} className="rounded border-border bg-card" /></CreateField></div>
            </div>
          </CreateSection>
          <CustomerFieldInputs definitions={definitions} values={customFields} onChange={setCustomFields} disabled={frozen} />
        </div>
        <aside className="min-w-0 space-y-5">
          <section className="space-y-3 rounded border border-border bg-card p-5"><div className="flex items-center gap-2 text-base font-semibold"><UserRound size={18} className="text-primary" />普通客户身份</div><p className="text-sm leading-6 text-muted-foreground">此账号用于客户前台与购买服务，不具有后台管理权限。</p></section>
          <CreateSection title="归属设置">
            <div className="space-y-5"><div className="space-y-2"><DirectorySelect label="客户分组" value={customerGroup} onChange={setCustomerGroup} searchable placeholder="未分组" options={(groups.data ?? []).map(group => ({ value: String(group.id), label: group.name }))} disabled={!groupAllowed || !groupDirectoryAllowed || !groups.data} /><DirectoryOptionState allowed={groupAllowed && groupDirectoryAllowed} data={groups.data} error={groups.error} retry={groups.refresh} label="客户分组" /></div>{salesAllowed && <div className="space-y-2"><DirectorySelect label="销售归属" value={salesOwner} onChange={setSalesOwner} searchable placeholder="无销售归属" options={(sales.data ?? []).map(owner => ({ value: String(owner.id), label: owner.display_name || owner.username }))} disabled={!sales.data} /><DirectoryOptionState allowed data={sales.data} error={sales.error} retry={sales.refresh} label="销售" /></div>}</div>
          </CreateSection>
        </aside>
      </fieldset>
      <div className="space-y-3 rounded border border-border bg-card p-4 sm:p-5">
        {mutation.error && <p role="alert" className="break-words text-sm text-destructive">{mutation.error}</p>}
        {mutation.conflict && <div role="status" className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground"><span>输入已保留。若客户字段定义发生变化，请重新读取并核对。</span><Button type="button" variant="outline" size="sm" disabled={mutation.frozen} onClick={definitions.refresh}>重新读取字段定义</Button></div>}
        <div className="flex flex-wrap items-center justify-between gap-3"><p className="text-xs text-muted-foreground">{mutation.uncertain ? "结果待核对，请保留此页面并使用原请求重试。" : "提交前将核对邮箱、客户身份与归属。"}</p><div className="flex gap-2"><Button type="button" variant="outline" className="rounded shadow-none" disabled={mutation.frozen} onClick={cancel}>取消</Button><Button type="submit" className="rounded bg-primary hover:bg-primary/90" disabled={mutation.busy || preview !== null || !definitions.ready && !mutation.uncertain}><Save size={16} />{mutation.uncertain ? "使用原请求重试" : mutation.busy ? "正在创建…" : "创建客户"}</Button></div></div>
      </div>
    </form>
    <Dialog open={preview !== null} onOpenChange={open => { if (!open && !mutation.busy) setPreview(null); }}><DialogContent className="max-w-md rounded-lg"><DialogHeader><DialogTitle>确认创建客户</DialogTitle><DialogDescription>请核对账号与归属。初始密码不会在确认框中展示。</DialogDescription></DialogHeader><dl className="grid min-w-0 grid-cols-[80px_minmax(0,1fr)] gap-x-3 gap-y-3 text-sm"><dt className="text-muted-foreground">身份</dt><dd>普通客户</dd><dt className="text-muted-foreground">邮箱</dt><dd className="break-all">{String(preview?.email ?? "")}</dd><dt className="text-muted-foreground">用户名</dt><dd className="break-all">{String(preview?.username ?? "按邮箱自动生成")}</dd><dt className="text-muted-foreground">客户分组</dt><dd>{groups.data?.find(group => group.id === preview?.group_id)?.name ?? "未分组"}</dd><dt className="text-muted-foreground">销售归属</dt><dd>{sales.data?.find(owner => owner.id === preview?.sales_owner_id)?.display_name ?? "无销售归属"}</dd></dl><div className="flex justify-end gap-2 border-t border-border pt-4"><Button type="button" variant="outline" disabled={mutation.busy} onClick={() => setPreview(null)}>返回修改</Button><Button type="button" disabled={mutation.busy} onClick={() => void confirmCreate()}>{mutation.busy ? "正在创建…" : "确认创建"}</Button></div></DialogContent></Dialog>
  </div>;
}

function CreateSection({ title, description, children }: { title: string; description?: string; children: React.ReactNode }) {
  return <section className="min-w-0 overflow-hidden rounded border border-border bg-card"><div className="border-b border-border px-5 py-3"><h2 className="text-base font-semibold">{title}</h2>{description && <p className="mt-1 text-xs leading-5 text-muted-foreground">{description}</p>}</div><div className="min-w-0 p-5">{children}</div></section>;
}

function CreateField({ id, label, required, hint, children }: { id?: string; label: string; required?: boolean; hint?: string; children: React.ReactNode }) {
  const generatedID = useId();
  const fieldID = id ?? generatedID;
  const content = isValidElement<{ id?: string }>(children) && children.type !== "div" ? cloneElement(children, { id: fieldID }) : children;
  return <div className="min-w-0 space-y-1.5 text-sm text-muted-foreground"><label htmlFor={fieldID} className="block">{label}{required && <span className="ml-1 text-destructive">*</span>}</label>{content}{hint && <span className="block text-xs leading-5 text-muted-foreground">{hint}</span>}</div>;
}
