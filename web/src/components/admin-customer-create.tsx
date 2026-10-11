"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { HelpCircle } from "lucide-react";
import { type CustomerGroup, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Workspace, useAdminPermission } from "./workspace";
import { CustomerFieldInputs, customerFieldValues, useCustomerFieldDefinitions, useCustomerMutation, useCustomerDraft, CustomerDraftNotice, CustomerMutationMessage, customerListSuffix } from "./customer-field-inputs";

type SalesOwner = { id: number; username: string; display_name: string };

export function AdminCustomerCreateView() {
  return <Workspace title="添加客户" admin><CustomerCreateForm /></Workspace>;
}

function CustomerCreateForm() {
  const router = useRouter();
  const manage = useAdminPermission("customers.manage");
  const user = useResource<User>("/me", false);
  const groupAllowed = useAdminPermission("customer_groups.manage");
  const salesAllowed = useAdminPermission("@super");
  const groupsResource = useResource<CustomerGroup[]>(groupAllowed ? "/admin/customer-groups?options=true" : null, false);
  const salesResource = useResource<SalesOwner[]>(salesAllowed ? "/admin/sales-owners" : null, false);

  // 个人资料
  const [name, setName] = useState("");
  const [gender, setGender] = useState("");
  const [company, setCompany] = useState("");
  const [country, setCountry] = useState("中国");
  const [province, setProvince] = useState("");
  const [address, setAddress] = useState("");
  const [postcode, setPostcode] = useState("");
  const [channel, setChannel] = useState("");

  // 账户信息
  const [phonePrefix, setPhonePrefix] = useState("+86");
  const [phone, setPhone] = useState("");
  const [email, setEmail] = useState("");
  const [qq, setQq] = useState("");
  const [password, setPassword] = useState("");

  // 其他设置
  const [paymentMethod, setPaymentMethod] = useState("bank");
  const [language, setLanguage] = useState("zh_CN");
  const [salesOwner, setSalesOwner] = useState("none");
  const [customerGroup, setCustomerGroup] = useState("none");
  const [status, setStatus] = useState("active");
  const [adminNote, setAdminNote] = useState("");
  const [receiveMarketing, setReceiveMarketing] = useState(true);
  const definitions = useCustomerFieldDefinitions(manage);
  const [customFields, setCustomFields] = useState<Record<string, unknown>>({});
  const mutation = useCustomerMutation();
  const { busy, uncertain, setError } = mutation;
  const snapshot = { name, gender, company, country, province, address, postcode, channel, phonePrefix, phone, email, qq, paymentMethod, language, salesOwner, customerGroup, status, adminNote, receiveMarketing, customFields };
  const [initial] = useState(JSON.stringify(snapshot));
  const dirty = initial !== JSON.stringify(snapshot) || !!password;
  const draft = useCustomerDraft(manage && user.data ? `customer-create:${user.data.id}` : null, snapshot, dirty);
  function restoreDraft() {
    const value = draft.draft; if (!value) return;
    setName(value.name); setGender(value.gender); setCompany(value.company); setCountry(value.country); setProvince(value.province); setAddress(value.address); setPostcode(value.postcode); setChannel(value.channel); setPhonePrefix(value.phonePrefix); setPhone(value.phone); setEmail(value.email); setQq(value.qq); setPaymentMethod(value.paymentMethod); setLanguage(value.language); setSalesOwner(value.salesOwner); setCustomerGroup(value.customerGroup); setStatus(value.status); setAdminNote(value.adminNote); setReceiveMarketing(value.receiveMarketing); setCustomFields(value.customFields); setPassword(""); draft.clear();
  }
  function cancel() { if (busy || dirty && !window.confirm("确认取消添加客户并丢弃草稿？")) return; draft.leave(); router.push(`/admin/customers${customerListSuffix()}`); }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy || !manage) return;
    if (uncertain) { const res = await mutation.run<{id: number}>("/admin/customers", "POST", {}); if (res) { draft.leave(); router.push(`/admin/customers/${res.id}${customerListSuffix()}`); router.refresh(); } return; }
    setError("");
    if (!definitions.ready) { setError(definitions.error?.message ?? "客户字段仍在加载，请稍后重试"); return; }

    if (!name.trim()) {
      setError("请输入姓名");
      return;
    }
    if (!email.trim()) {
      setError("请输入邮箱");
      return;
    }
    const passwordBytes = new TextEncoder().encode(password).length;
    if (passwordBytes < 8 || passwordBytes > 72) {
      setError("密码长度需在8-72字节之间");
      return;
    }

    try {
      let dynamicFields: Record<string, unknown>;
      try { dynamicFields = customerFieldValues(definitions.fields, customFields); } catch (err) { setError((err as Error).message); return; }
      const payload: Record<string, unknown> = {
        display_name: name.trim(),
        username: phone.trim() || email.split("@")[0] || name.trim(),
        email: email.trim(),
        password,
        status,
        company_name: company.trim(),
        phone: phone.trim() ? `${phonePrefix} ${phone.trim()}` : "",
        gender: gender || "unknown", country, province, address, postcode,
        source_channel: channel, qq, payment_method: paymentMethod, language,
        admin_note: adminNote, receive_marketing: receiveMarketing,
        custom_fields: dynamicFields, custom_fields_version: definitions.version,
      };

      if (groupAllowed && customerGroup !== "none") {
        payload.group_id = Number(customerGroup);
      }
      if (salesAllowed && salesOwner !== "none") {
        payload.sales_owner_id = Number(salesOwner);
      }

      const res = await mutation.run<{ id: number }>("/admin/customers", "POST", payload);
      if (res) { draft.leave(); router.push(`/admin/customers/${res.id}${customerListSuffix()}`); router.refresh(); }
    } catch (err) { setError((err as Error).message); }
  }

  if (!manage) return <p role="alert">没有添加客户权限。</p>;

  return (
    <>
      <div className="space-y-5">
        <h1 className="text-xl font-bold text-[#303133]">添加客户</h1>

        <CustomerDraftNotice available={!!draft.draft} restore={restoreDraft} discard={draft.clear} disabled={mutation.frozen} />
        <form onSubmit={submit} className="space-y-5">
          <fieldset disabled={mutation.frozen} className="min-w-0 space-y-5">
          {/* 个人资料 */}
          <section className="bg-white rounded border border-[#ebeef5] overflow-hidden shadow-sm">
            <div className="bg-[#f4f6fa] px-4 py-2.5 border-b border-[#ebeef5]">
              <h2 className="text-sm font-medium text-[#303133]">个人资料</h2>
            </div>
            <div className="p-5 space-y-4">
              <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-4">
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">
                    <span className="text-red-500 mr-1">*</span>姓名
                  </label>
                  <Input
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder=""
                    required
                    className="h-9 border-[#dcdfe6] focus-visible:border-[#409eff]"
                  />
                </div>
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">性别</label>
                  <Select value={gender} onValueChange={setGender}>
                    <SelectTrigger className="h-9 border-[#dcdfe6]">
                      <SelectValue placeholder="请选择性别" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="male">男</SelectItem>
                      <SelectItem value="female">女</SelectItem>
                      <SelectItem value="unknown">未知</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">所在公司</label>
                  <Input
                    value={company}
                    onChange={(e) => setCompany(e.target.value)}
                    placeholder=""
                    className="h-9 border-[#dcdfe6]"
                  />
                </div>
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">国家</label>
                  <Input
                    value={country}
                    onChange={(e) => setCountry(e.target.value)}
                    placeholder=""
                    className="h-9 border-[#dcdfe6]"
                  />
                </div>
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">省</label>
                  <Input
                    value={province}
                    onChange={(e) => setProvince(e.target.value)}
                    placeholder=""
                    className="h-9 border-[#dcdfe6]"
                  />
                </div>
              </div>

              <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-4">
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">地址</label>
                  <Input
                    value={address}
                    onChange={(e) => setAddress(e.target.value)}
                    placeholder=""
                    className="h-9 border-[#dcdfe6]"
                  />
                </div>
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">邮编</label>
                  <Input
                    value={postcode}
                    onChange={(e) => setPostcode(e.target.value)}
                    placeholder=""
                    className="h-9 border-[#dcdfe6]"
                  />
                </div>
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">了解途径</label>
                  <Input
                    value={channel}
                    onChange={(e) => setChannel(e.target.value)}
                    placeholder=""
                    className="h-9 border-[#dcdfe6]"
                  />
                </div>
              </div>
            </div>
          </section>

          {/* 账户信息 */}
          <section className="bg-white rounded border border-[#ebeef5] overflow-hidden shadow-sm">
            <div className="bg-[#f4f6fa] px-4 py-2.5 border-b border-[#ebeef5]">
              <h2 className="text-sm font-medium text-[#303133]">账户信息</h2>
            </div>
            <div className="p-5">
              <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-4 gap-4">
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">手机</label>
                  <div className="flex">
                    <Select value={phonePrefix} onValueChange={setPhonePrefix}>
                      <SelectTrigger className="h-9 w-20 rounded-r-none border-[#dcdfe6] border-r-0">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="+86">+86</SelectItem>
                        <SelectItem value="+852">+852</SelectItem>
                        <SelectItem value="+1">+1</SelectItem>
                      </SelectContent>
                    </Select>
                    <Input
                      value={phone}
                      onChange={(e) => setPhone(e.target.value)}
                      placeholder="请输入手机号码"
                      className="h-9 rounded-l-none border-[#dcdfe6]"
                    />
                  </div>
                </div>
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">
                    <span className="text-red-500 mr-1">*</span>邮箱
                  </label>
                  <Input
                    type="email"
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    placeholder=""
                    required
                    className="h-9 border-[#dcdfe6]"
                  />
                </div>
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">QQ</label>
                  <Input
                    value={qq}
                    onChange={(e) => setQq(e.target.value)}
                    placeholder=""
                    className="h-9 border-[#dcdfe6]"
                  />
                </div>
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">
                    <span className="text-red-500 mr-1">*</span>密码
                  </label>
                  <Input
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    placeholder=""
                    required
                    className="h-9 border-[#dcdfe6]"
                  />
                </div>
              </div>
            </div>
          </section>

          {/* 其他设置 */}
          <section className="bg-white rounded border border-[#ebeef5] overflow-hidden shadow-sm">
            <div className="bg-[#f4f6fa] px-4 py-2.5 border-b border-[#ebeef5]">
              <h2 className="text-sm font-medium text-[#303133]">其他设置</h2>
            </div>
            <div className="p-5 space-y-4">
              <div className="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-5 gap-4">
                <div>
                  <label className="flex items-center gap-1 text-xs font-medium text-[#606266] mb-1.5">
                    支付方式
                    <HelpCircle size={13} className="text-[#909399]" />
                  </label>
                  <Select value={paymentMethod} onValueChange={setPaymentMethod}>
                    <SelectTrigger className="h-9 border-[#dcdfe6]">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="bank">银行转账</SelectItem>
                      <SelectItem value="alipay">支付宝</SelectItem>
                      <SelectItem value="wechat">微信支付</SelectItem>
                    </SelectContent>
                  </Select>
                </div>

                <div>
                  <label className="flex items-center gap-1 text-xs font-medium text-[#606266] mb-1.5">
                    语言
                    <HelpCircle size={13} className="text-[#909399]" />
                  </label>
                  <Select value={language} onValueChange={setLanguage}>
                    <SelectTrigger className="h-9 border-[#dcdfe6]">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="zh_CN">中文简体</SelectItem>
                      <SelectItem value="en_US">English</SelectItem>
                    </SelectContent>
                  </Select>
                </div>

                <div>
                  <label className="flex items-center gap-1 text-xs font-medium text-[#606266] mb-1.5">
                    销售
                    <HelpCircle size={13} className="text-[#909399]" />
                  </label>
                  <Select value={salesOwner} onValueChange={setSalesOwner} disabled={!salesAllowed}>
                    <SelectTrigger className="h-9 border-[#dcdfe6]">
                      <SelectValue placeholder="无" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="none">无</SelectItem>
                      {salesResource.data?.map((owner) => (
                        <SelectItem key={owner.id} value={String(owner.id)}>
                          {owner.display_name || owner.username}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>

                <div>
                  <label className="flex items-center gap-1 text-xs font-medium text-[#606266] mb-1.5">
                    客户分组
                    <HelpCircle size={13} className="text-[#909399]" />
                  </label>
                  <Select value={customerGroup} onValueChange={setCustomerGroup} disabled={!groupAllowed}>
                    <SelectTrigger className="h-9 border-[#dcdfe6]">
                      <SelectValue placeholder="无" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="none">无</SelectItem>
                      {groupsResource.data?.map((group) => (
                        <SelectItem key={group.id} value={String(group.id)}>
                          {group.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>

                <div>
                  <label className="flex items-center gap-1 text-xs font-medium text-[#606266] mb-1.5">
                    状态
                    <HelpCircle size={13} className="text-[#909399]" />
                  </label>
                  <Select value={status} onValueChange={setStatus}>
                    <SelectTrigger className="h-9 border-[#dcdfe6]">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="active">正常</SelectItem>
                      <SelectItem value="disabled">已停用</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>

              <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 items-center">
                <div>
                  <label className="block text-xs font-medium text-[#606266] mb-1.5">管理员备注</label>
                  <Textarea
                    value={adminNote}
                    onChange={(e) => setAdminNote(e.target.value)}
                    rows={2}
                    className="border-[#dcdfe6] text-xs"
                  />
                </div>
                <div className="flex items-center gap-3 pt-4">
                  <span className="text-xs font-medium text-[#606266]">接收营销信息</span>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={receiveMarketing}
                    onClick={() => setReceiveMarketing(!receiveMarketing)}
                    className={`relative inline-flex h-5 w-10 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none ${
                      receiveMarketing ? "bg-[#409eff]" : "bg-gray-300"
                    }`}
                  >
                    <span
                      aria-hidden="true"
                      className={`pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out ${
                        receiveMarketing ? "translate-x-5" : "translate-x-0"
                      }`}
                    />
                  </button>
                </div>
              </div>
            </div>
          </section>

          </fieldset>
          <CustomerFieldInputs definitions={definitions} values={customFields} onChange={setCustomFields} disabled={busy || uncertain} />
          <CustomerMutationMessage mutation={mutation} />
          {mutation.conflict && <Button type="button" variant="outline" onClick={definitions.refresh}>更新字段版本，保留当前输入</Button>}
          <div className="flex flex-wrap items-center gap-3 pt-2">
            <Button
              type="submit"
              disabled={busy || !definitions.ready && !uncertain}
              className="bg-[#409eff] hover:bg-[#337ecc] text-white px-6 h-9 font-normal text-sm rounded shadow-none"
            >
              {uncertain ? "使用原请求重试" : busy ? "添加中..." : "添加客户"}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={cancel}
              className="border-[#dcdfe6] text-[#606266] hover:bg-[#f5f7fa] px-6 h-9 font-normal text-sm rounded"
            >
              返回
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={cancel}
              className="border-[#dcdfe6] text-[#606266] hover:bg-[#f5f7fa] px-6 h-9 font-normal text-sm rounded"
            >
              取消添加
            </Button>
          </div>
        </form>
      </div>
    </>
  );
}
