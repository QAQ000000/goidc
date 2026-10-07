"use client";
import { useAdminListNavigation, useAdminListReturnHref } from "@/hooks/use-admin-list-navigation";

import { useRef, useState } from "react";
import Link from "next/link";
import {
  ArrowLeft,
  ArrowRight,
  Ban,
  FileCheck,
  FileUp,
  Pencil,
  Plus,
  Receipt,
  Save,
  Trash2,
} from "lucide-react";
import { useRouter } from "next/navigation";
import { api, date, money, upload } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Textarea } from "./ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "./ui/select";
import { Checkbox } from "./ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import { Failure, Loading } from "./common";
import { Badge } from "./ui/badge";
import { Workspace, useAdminPermission } from "./workspace";

type Profile = {
  id: number;
  profile_type: "individual" | "company";
  title: string;
  tax_number: string;
  bank_name: string;
  bank_account: string;
  company_address: string;
  phone: string;
  version: number;
};
type Address = {
  id: number;
  recipient: string;
  phone: string;
  province: string;
  city: string;
  region: string;
  address: string;
  postcode: string;
  is_default: boolean;
  version: number;
};
type SourceInvoice = {
  id: number;
  invoice_no: string;
  amount_cents: number;
  paid_at: string;
  invoice_kind: string;
};
type Application = {
  id: number;
  user_id: number;
  status: "pending" | "rejected" | "issued";
  amount_cents: number;
  review_note: string;
  document_number: string;
  carrier: string;
  tracking_number: string;
  version: number;
  created_at: string;
  reviewed_at: string | null;
  profile_type?: string;
  title?: string;
  recipient?: string;
  address?: string;
  customer_name?: string;
  customer_email?: string;
  profile_snapshot?: Record<string, string>;
  address_snapshot?: Record<string, string>;
  items?: {
    source_invoice_id: number;
    invoice_no: string;
    description: string;
    amount_cents: number;
  }[];
  documents?: {
    id: number;
    adjustment_id: number | null;
    filename: string;
    mime_type: string;
    size_bytes: number;
    sha256: string;
    downloads: number;
    download_url: string;
  }[];
  adjustments?: {
    id: number;
    action: "void" | "red";
    document_number: string;
    note: string;
    created_at: string;
  }[];
};
type Page<T> = { items: T[]; total: number; limit: number; offset: number };
const statusLabel = { pending: "待审核", rejected: "已驳回", issued: "已开具" };
const applicationStates = ["pending", "rejected", "issued"] as const;
function clean(value: string) {
  return value.trim();
}
function ApplicationStatus({ status }: { status: Application["status"] }) {
  return <Badge variant="outline">{statusLabel[status]}</Badge>;
}

export function InvoiceApplicationListView({
  admin = false,
}: {
  admin?: boolean;
}) {
  return (
    <Workspace title={admin ? "发票审核" : "发票申请"} admin={admin}>
      <ApplicationList admin={admin} />
    </Workspace>
  );
}
function ApplicationList({ admin }: { admin: boolean }) {
  const { status, setStatus, page, setPage, ready, suffix } = useAdminListNavigation(admin, applicationStates);
  const offset = page * 20;
  const setOffset = (value: number | ((current: number) => number)) => setPage(current => Math.floor((typeof value === "function" ? value(current * 20) : value) / 20));
  const { data, error, refresh } = useResource<Page<Application>>(
    ready ? `${admin ? "/admin" : ""}/invoice-applications?limit=20&offset=${offset}${status !== "all" ? `&status=${status}` : ""}` : null,
    false,
  );
  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-3 mb-6">
        <h1 className="page-title">
          {admin ? "发票审核" : "发票申请"}
        </h1>
        {!admin && (
          <div className="flex gap-2">
            <Button variant="outline" asChild>
              <Link href="/console/invoice-information">
                <Pencil />
                开票资料
              </Link>
            </Button>
            <Button asChild>
              <Link href="/console/invoice-applications/new">
                <Plus />
                申请发票
              </Link>
            </Button>
          </div>
        )}
      </div>
      {admin && <div className="mb-4 flex overflow-x-auto border-b" role="group" aria-label="发票申请状态">
        {Object.entries({ all: "全部", ...statusLabel }).map(([key, label]) => <button key={key} type="button" aria-pressed={status === key} onClick={() => { setStatus(key); setOffset(0); }} className={`shrink-0 border-b-2 px-4 py-3 text-sm ${status === key ? "border-primary font-medium text-primary" : "border-transparent text-muted-foreground"}`}>{label}</button>)}
      </div>}
      {!admin && <Select
        value={status}
        onValueChange={(value) => {
          setStatus(value);
          setOffset(0);
        }}
      >
        <SelectTrigger aria-label="发票申请状态" className="mb-4 w-44">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">全部状态</SelectItem>
          {Object.entries(statusLabel).map(([key, label]) => (
            <SelectItem value={key} key={key}>
              {label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>}
      {error ? (
        <Failure error={error} retry={refresh} />
      ) : !data ? (
        <Loading />
      ) : (
        <>
          <div className="admin-list-surface data-surface overflow-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left">
                  <th className="p-3">申请时间</th>
                  {admin && <th className="p-3">客户</th>}
                  <th className="p-3">抬头</th>
                  <th className="p-3">金额</th>
                  <th className="p-3">状态</th>
                  <th className="p-3">操作</th>
                </tr>
              </thead>
              <tbody>
                {data.items.length === 0 && (
                  <tr>
                    <td
                      colSpan={admin ? 6 : 5}
                      className="h-28 text-center text-muted-foreground"
                    >
                      暂无发票申请
                    </td>
                  </tr>
                )}
                {data.items.map((item) => (
                  <tr key={item.id} className="border-b last:border-0">
                    <td className="p-3 whitespace-nowrap">
                      {date(item.created_at)}
                    </td>
                    {admin && (
                      <td className="p-3">
                        {item.customer_name}
                        <br />
                        <span className="text-xs text-muted-foreground">
                          {item.customer_email}
                        </span>
                      </td>
                    )}
                    <td className="p-3">{item.title || "-"}</td>
                    <td className="p-3 money">{money(item.amount_cents)}</td>
                    <td className="p-3">
                      <ApplicationStatus status={item.status} />
                    </td>
                    <td className="p-3">
                      <Button variant="link" size="sm" asChild>
                        <Link
                          href={`${admin ? "/admin" : "/console"}/invoice-applications/${item.id}${suffix}`}
                        >
                          查看
                        </Link>
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="mt-4 flex flex-wrap justify-between gap-3 text-sm">
            <span>{data.total} 个申请</span>
            <div className="flex gap-2">
              <Button
                variant="outline"
                disabled={offset === 0}
                onClick={() => setOffset(offset - 20)}
              >
                <ArrowLeft />
                上一页
              </Button>
              <Button
                variant="outline"
                disabled={offset + 20 >= data.total}
                onClick={() => setOffset(offset + 20)}
              >
                下一页
                <ArrowRight />
              </Button>
            </div>
          </div>
        </>
      )}
    </>
  );
}

function ProfileEditor({
  profile,
  onDone,
}: {
  profile?: Profile;
  onDone: () => void;
}) {
  const [type, setType] = useState(profile?.profile_type ?? "individual");
  const [title, setTitle] = useState(profile?.title ?? "");
  const [tax, setTax] = useState(profile?.tax_number ?? "");
  const [phone, setPhone] = useState(profile?.phone ?? "");
  const [bankName, setBankName] = useState(profile?.bank_name ?? "");
  const [bankAccount, setBankAccount] = useState(profile?.bank_account ?? "");
  const [companyAddress, setCompanyAddress] = useState(
    profile?.company_address ?? "",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save() {
    setBusy(true);
    setError("");
    try {
      await api(`/invoice-profiles${profile ? `/${profile.id}` : ""}`, {
        method: profile ? "PUT" : "POST",
        body: JSON.stringify({
          profile_type: type,
          title: clean(title),
          tax_number: clean(tax),
          phone: clean(phone),
          bank_name: clean(bankName),
          bank_account: clean(bankAccount),
          company_address: clean(companyAddress),
          expected_version: profile?.version ?? 0,
        }),
      });
      onDone();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="grid sm:grid-cols-2 gap-3 border-b pb-5">
      <div>
        <Label>抬头类型</Label>
        <Select
          value={type}
          onValueChange={(value) => setType(value as "individual" | "company")}
          disabled={busy}
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="individual">个人</SelectItem>
            <SelectItem value="company">公司</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div>
        <Label htmlFor="invoice-title">发票抬头</Label>
        <Input
          id="invoice-title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          disabled={busy}
        />
      </div>
      {type === "company" && (
        <div>
          <Label htmlFor="invoice-tax">税号</Label>
          <Input
            id="invoice-tax"
            value={tax}
            onChange={(e) => setTax(e.target.value)}
            disabled={busy}
          />
        </div>
      )}
      <div>
        <Label htmlFor="invoice-phone">联系电话</Label>
        <Input
          id="invoice-phone"
          value={phone}
          onChange={(e) => setPhone(e.target.value)}
          disabled={busy}
        />
      </div>
      {type === "company" && (
        <>
          <div>
            <Label htmlFor="invoice-bank">开户行</Label>
            <Input
              id="invoice-bank"
              maxLength={160}
              value={bankName}
              onChange={(e) => setBankName(e.target.value)}
              disabled={busy}
            />
          </div>
          <div>
            <Label htmlFor="invoice-bank-account">银行账号</Label>
            <Input
              id="invoice-bank-account"
              maxLength={120}
              value={bankAccount}
              onChange={(e) => setBankAccount(e.target.value)}
              disabled={busy}
            />
          </div>
          <div className="sm:col-span-2">
            <Label htmlFor="invoice-company-address">公司地址</Label>
            <Input
              id="invoice-company-address"
              maxLength={300}
              value={companyAddress}
              onChange={(e) => setCompanyAddress(e.target.value)}
              disabled={busy}
            />
          </div>
        </>
      )}
      <div className="sm:col-span-2 flex flex-wrap gap-2">
        <Button
          disabled={
            busy || !clean(title) || (type === "company" && !clean(tax))
          }
          onClick={() => void save()}
        >
          <Save />
          保存抬头
        </Button>
        {error && (
          <p role="alert" className="text-sm text-destructive self-center">
            {error}
          </p>
        )}
      </div>
    </div>
  );
}

function AddressEditor({
  address,
  onDone,
}: {
  address?: Address;
  onDone: () => void;
}) {
  const [recipient, setRecipient] = useState(address?.recipient ?? "");
  const [phone, setPhone] = useState(address?.phone ?? "");
  const [value, setValue] = useState(address?.address ?? "");
  const [province, setProvince] = useState(address?.province ?? "");
  const [city, setCity] = useState(address?.city ?? "");
  const [region, setRegion] = useState(address?.region ?? "");
  const [postcode, setPostcode] = useState(address?.postcode ?? "");
  const [defaultValue, setDefaultValue] = useState(
    address?.is_default ?? false,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save() {
    setBusy(true);
    setError("");
    try {
      await api(`/invoice-addresses${address ? `/${address.id}` : ""}`, {
        method: address ? "PUT" : "POST",
        body: JSON.stringify({
          recipient: clean(recipient),
          phone: clean(phone),
          province: clean(province),
          city: clean(city),
          region: clean(region),
          address: clean(value),
          postcode: clean(postcode),
          is_default: defaultValue,
          expected_version: address?.version ?? 0,
        }),
      });
      onDone();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="grid sm:grid-cols-2 gap-3">
      <div>
        <Label htmlFor="invoice-recipient">收件人</Label>
        <Input
          id="invoice-recipient"
          value={recipient}
          onChange={(e) => setRecipient(e.target.value)}
          disabled={busy}
        />
      </div>
      <div>
        <Label htmlFor="invoice-address-phone">电话</Label>
        <Input
          id="invoice-address-phone"
          value={phone}
          onChange={(e) => setPhone(e.target.value)}
          disabled={busy}
        />
      </div>
      <div>
        <Label htmlFor="invoice-province">省份</Label>
        <Input
          id="invoice-province"
          maxLength={80}
          value={province}
          onChange={(e) => setProvince(e.target.value)}
          disabled={busy}
        />
      </div>
      <div>
        <Label htmlFor="invoice-city">城市</Label>
        <Input
          id="invoice-city"
          maxLength={80}
          value={city}
          onChange={(e) => setCity(e.target.value)}
          disabled={busy}
        />
      </div>
      <div>
        <Label htmlFor="invoice-region">区县</Label>
        <Input
          id="invoice-region"
          maxLength={80}
          value={region}
          onChange={(e) => setRegion(e.target.value)}
          disabled={busy}
        />
      </div>
      <div>
        <Label htmlFor="invoice-postcode">邮编</Label>
        <Input
          id="invoice-postcode"
          maxLength={30}
          value={postcode}
          onChange={(e) => setPostcode(e.target.value)}
          disabled={busy}
        />
      </div>
      <div className="sm:col-span-2">
        <Label htmlFor="invoice-address">收货地址</Label>
        <Textarea
          id="invoice-address"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          disabled={busy}
        />
      </div>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox
          checked={defaultValue}
          onCheckedChange={(v) => setDefaultValue(v === true)}
          disabled={busy}
        />
        设为默认地址
      </label>
      <div className="flex gap-2">
        <Button
          disabled={busy || !clean(recipient) || !clean(phone) || !clean(value)}
          onClick={() => void save()}
        >
          <Save />
          保存地址
        </Button>
        {error && (
          <p role="alert" className="text-sm text-destructive self-center">
            {error}
          </p>
        )}
      </div>
    </div>
  );
}

export function InvoiceApplicationCreateView() {
  const router = useRouter();
  const profiles = useResource<Profile[]>("/invoice-profiles", false);
  const addresses = useResource<Address[]>("/invoice-addresses", false);
  const [offset, setOffset] = useState(0);
  const invoices = useResource<Page<SourceInvoice>>(
    `/invoiceable-invoices?limit=20&offset=${offset}`,
    false,
  );
  const [profileID, setProfileID] = useState("");
  const [addressID, setAddressID] = useState("");
  const [selected, setSelected] = useState<Record<number, SourceInvoice>>({});
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const retry = useRef<{ body: string; key: string } | null>(null);
  const selections = Object.values(selected).sort((a, b) => a.id - b.id);
  const total = selections.reduce((sum, item) => sum + item.amount_cents, 0);
  const valid =
    !!profiles.data?.some((p) => String(p.id) === profileID) &&
    !!addresses.data?.some((a) => String(a.id) === addressID) &&
    selections.length > 0 &&
    selections.length <= 20;
  function toggle(invoice: SourceInvoice, value: boolean) {
    setSelected((current) => {
      const next = { ...current };
      if (value) next[invoice.id] = invoice;
      else delete next[invoice.id];
      return next;
    });
  }
  async function submit() {
    if (!valid || busy) return;
    const body = JSON.stringify({
      profile_id: Number(profileID),
      address_id: Number(addressID),
      invoice_ids: selections.map((item) => item.id),
    });
    if (retry.current?.body !== body)
      retry.current = {
        body,
        key: `invoice-application-${crypto.randomUUID()}`,
      };
    setBusy(true);
    setError("");
    try {
      const result = await api<{ application_id: number }>(
        "/invoice-applications",
        {
          method: "POST",
          body,
          headers: { "Idempotency-Key": retry.current.key },
        },
      );
      router.push(`/console/invoice-applications/${result.application_id}`);
      router.refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Workspace title="申请发票">
      <Button variant="ghost" asChild className="mb-5">
        <Link href="/console/invoice-applications">
          <ArrowLeft />
          返回发票申请
        </Link>
      </Button>
      <div className="max-w-3xl space-y-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h1 className="text-xl font-semibold">申请发票</h1>
          <Button variant="outline" asChild>
            <Link href="/console/invoice-information">
              <Pencil />
              开票资料
            </Link>
          </Button>
        </div>
        {profiles.error ? (
          <Failure error={profiles.error} retry={profiles.refresh} />
        ) : addresses.error ? (
          <Failure error={addresses.error} retry={addresses.refresh} />
        ) : !profiles.data || !addresses.data ? (
          <Loading />
        ) : (
          <section className="grid sm:grid-cols-2 gap-5">
            <div className="min-w-0 space-y-2">
              <Label>发票抬头</Label>
              <Select
                value={profileID}
                onValueChange={setProfileID}
                disabled={busy}
              >
                <SelectTrigger aria-label="发票抬头" className="w-full">
                  <SelectValue placeholder="选择抬头" />
                </SelectTrigger>
                <SelectContent>
                  {profiles.data.map((p) => (
                    <SelectItem value={String(p.id)} key={p.id}>
                      {p.title} ·{" "}
                      {p.profile_type === "company" ? "公司" : "个人"}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {profiles.data.length === 0 && (
                <p className="text-sm text-muted-foreground">暂无发票抬头</p>
              )}
            </div>
            <div className="min-w-0 space-y-2">
              <Label>邮寄地址</Label>
              <Select
                value={addressID}
                onValueChange={setAddressID}
                disabled={busy}
              >
                <SelectTrigger aria-label="邮寄地址" className="w-full">
                  <SelectValue placeholder="选择地址" />
                </SelectTrigger>
                <SelectContent>
                  {addresses.data.map((a) => (
                    <SelectItem value={String(a.id)} key={a.id}>
                      {a.recipient} · {a.address}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {addresses.data.length === 0 && (
                <p className="text-sm text-muted-foreground">暂无收货地址</p>
              )}
            </div>
          </section>
        )}
        <section>
          <h2 className="font-semibold mb-3">可开票账单</h2>
          {invoices.error ? (
            <Failure error={invoices.error} retry={invoices.refresh} />
          ) : !invoices.data ? (
            <Loading />
          ) : (
            <>
              <div className="border rounded-md divide-y">
                {invoices.data.items.length === 0 && (
                  <p className="p-6 text-center text-sm text-muted-foreground">
                    暂无可开票账单
                  </p>
                )}
                {invoices.data.items.map((invoice) => (
                  <label
                    className="flex items-center justify-between gap-3 p-3"
                    key={invoice.id}
                  >
                    <span className="flex min-w-0 items-center gap-3">
                      <Checkbox
                        aria-label={`选择账单 ${invoice.invoice_no}`}
                        disabled={
                          busy ||
                          (!selected[invoice.id] && selections.length >= 20)
                        }
                        checked={!!selected[invoice.id]}
                        onCheckedChange={(v) => toggle(invoice, v === true)}
                      />
                      <span className="font-mono text-xs break-all">
                        {invoice.invoice_no}
                      </span>
                    </span>
                    <span className="money shrink-0">
                      {money(invoice.amount_cents)}
                    </span>
                  </label>
                ))}
              </div>
              <div className="flex flex-wrap justify-between gap-3 mt-4 text-sm">
                <span>{invoices.data.total} 个可开票账单</span>
                <div className="flex gap-2">
                  <Button
                    variant="outline"
                    disabled={busy || offset === 0}
                    onClick={() => setOffset(offset - 20)}
                  >
                    <ArrowLeft />
                    上一页
                  </Button>
                  <Button
                    variant="outline"
                    disabled={busy || offset + 20 >= invoices.data.total}
                    onClick={() => setOffset(offset + 20)}
                  >
                    下一页
                    <ArrowRight />
                  </Button>
                </div>
              </div>
            </>
          )}
        </section>
        <div className="border-t pt-5 flex flex-wrap items-center justify-between gap-4">
          <div>
            <p className="text-sm text-muted-foreground">
              已选 {selections.length} 张账单
            </p>
            <p className="font-semibold money">{money(total)}</p>
          </div>
          <Button
            disabled={busy || !valid}
            onClick={() => {
              setError("");
              setConfirm(true);
            }}
          >
            <FileCheck />
            提交申请
          </Button>
        </div>
        <Dialog
          open={confirm}
          onOpenChange={(value) => {
            if (!busy) setConfirm(value);
          }}
        >
          <DialogContent showCloseButton={!busy}>
            <DialogHeader>
              <DialogTitle>确认发票申请</DialogTitle>
            </DialogHeader>
            <p className="text-sm break-all">
              {profiles.data?.find((p) => String(p.id) === profileID)?.title}
            </p>
            <p className="text-sm break-all">
              {addresses.data?.find((a) => String(a.id) === addressID)?.address}
            </p>
            <p className="text-lg font-semibold money">{money(total)}</p>
            {error && (
              <p role="alert" className="text-sm text-destructive">
                {error}
              </p>
            )}
            <DialogFooter>
              <Button
                variant="outline"
                disabled={busy}
                onClick={() => setConfirm(false)}
              >
                返回
              </Button>
              <Button disabled={busy || !valid} onClick={() => void submit()}>
                <FileCheck />
                {busy ? "提交中" : "确认提交"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>
    </Workspace>
  );
}

export function InvoiceInformationView() {
  const profiles = useResource<Profile[]>("/invoice-profiles", false);
  const addresses = useResource<Address[]>("/invoice-addresses", false);
  const [profileOpen, setProfileOpen] = useState(false);
  const [addressOpen, setAddressOpen] = useState(false);
  const [editingProfile, setEditingProfile] = useState<Profile>();
  const [editingAddress, setEditingAddress] = useState<Address>();
  const [deleteRecord, setDeleteRecord] = useState<{
    kind: "invoice-profiles" | "invoice-addresses";
    id: number;
    version: number;
    label: string;
  }>();
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState("");
  async function remove() {
    if (!deleteRecord) return;
    setDeleting(true);
    setDeleteError("");
    try {
      await api(`/${deleteRecord.kind}/${deleteRecord.id}`, {
        method: "DELETE",
        body: JSON.stringify({ expected_version: deleteRecord.version }),
      });
      setDeleteRecord(undefined);
      profiles.refresh();
      addresses.refresh();
    } catch (e) {
      setDeleteError((e as Error).message);
    } finally {
      setDeleting(false);
    }
  }
  return (
    <Workspace title="开票资料">
      <div className="mb-5">
        <Button variant="ghost" asChild>
          <Link href="/console/invoice-applications">
            <ArrowLeft />
            返回发票申请
          </Link>
        </Button>
      </div>
      {profiles.error ? (
        <Failure error={profiles.error} retry={profiles.refresh} />
      ) : addresses.error ? (
        <Failure error={addresses.error} retry={addresses.refresh} />
      ) : !profiles.data || !addresses.data ? (
        <Loading />
      ) : (
        <div className="space-y-8 max-w-3xl">
          <section>
            <div className="flex items-center justify-between gap-3 mb-3">
              <h2 className="font-semibold">发票抬头</h2>
              <Button
                size="sm"
                onClick={() => {
                  setEditingProfile(undefined);
                  setProfileOpen(true);
                }}
              >
                <Plus />
                新增抬头
              </Button>
            </div>
            <div className="border rounded-md divide-y">
              {profiles.data.length === 0 && (
                <p className="p-5 text-sm text-muted-foreground">暂无抬头</p>
              )}
              {profiles.data.map((profile) => (
                <div
                  key={profile.id}
                  className="flex flex-wrap items-center justify-between gap-3 p-3"
                >
                  <div>
                    <p className="font-medium">{profile.title}</p>
                    <p className="text-xs text-muted-foreground">
                      {profile.profile_type === "company"
                        ? `公司 · ${profile.tax_number}`
                        : "个人"}
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label="编辑发票抬头"
                    title="编辑发票抬头"
                    onClick={() => {
                      setEditingProfile(profile);
                      setProfileOpen(true);
                    }}
                  >
                    <Pencil />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    title="删除抬头"
                    aria-label={`删除抬头 ${profile.title}`}
                    onClick={() => {
                      setDeleteError("");
                      setDeleteRecord({
                        kind: "invoice-profiles",
                        id: profile.id,
                        version: profile.version,
                        label: profile.title,
                      });
                    }}
                  >
                    <Trash2 />
                  </Button>
                </div>
              ))}
            </div>
          </section>
          <section>
            <div className="flex items-center justify-between gap-3 mb-3">
              <h2 className="font-semibold">收货地址</h2>
              <Button
                size="sm"
                onClick={() => {
                  setEditingAddress(undefined);
                  setAddressOpen(true);
                }}
              >
                <Plus />
                新增地址
              </Button>
            </div>
            <div className="border rounded-md divide-y">
              {addresses.data.length === 0 && (
                <p className="p-5 text-sm text-muted-foreground">暂无地址</p>
              )}
              {addresses.data.map((address) => (
                <div
                  key={address.id}
                  className="flex flex-wrap items-center justify-between gap-3 p-3"
                >
                  <div>
                    <p className="font-medium">
                      {address.recipient} · {address.phone}
                    </p>
                    <p className="text-sm break-all">
                      {address.address}
                      {address.is_default ? " · 默认" : ""}
                    </p>
                  </div>
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label="编辑收货地址"
                    title="编辑收货地址"
                    onClick={() => {
                      setEditingAddress(address);
                      setAddressOpen(true);
                    }}
                  >
                    <Pencil />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    title="删除地址"
                    aria-label={`删除地址 ${address.recipient}`}
                    onClick={() => {
                      setDeleteError("");
                      setDeleteRecord({
                        kind: "invoice-addresses",
                        id: address.id,
                        version: address.version,
                        label: address.address,
                      });
                    }}
                  >
                    <Trash2 />
                  </Button>
                </div>
              ))}
            </div>
          </section>
          <Dialog open={profileOpen} onOpenChange={setProfileOpen}>
            <DialogContent className="max-h-[90dvh] overflow-y-auto">
              <DialogHeader>
                <DialogTitle>
                  {editingProfile ? "编辑发票抬头" : "新增发票抬头"}
                </DialogTitle>
              </DialogHeader>
              <ProfileEditor
                key={editingProfile?.id ?? "new"}
                profile={editingProfile}
                onDone={() => {
                  setProfileOpen(false);
                  profiles.refresh();
                }}
              />
            </DialogContent>
          </Dialog>
          <Dialog open={addressOpen} onOpenChange={setAddressOpen}>
            <DialogContent className="max-h-[90dvh] overflow-y-auto">
              <DialogHeader>
                <DialogTitle>
                  {editingAddress ? "编辑收货地址" : "新增收货地址"}
                </DialogTitle>
              </DialogHeader>
              <AddressEditor
                key={editingAddress?.id ?? "new"}
                address={editingAddress}
                onDone={() => {
                  setAddressOpen(false);
                  addresses.refresh();
                }}
              />
            </DialogContent>
          </Dialog>
          <Dialog
            open={!!deleteRecord}
            onOpenChange={(value) => {
              if (!value && !deleting) setDeleteRecord(undefined);
            }}
          >
            <DialogContent showCloseButton={!deleting}>
              <DialogHeader>
                <DialogTitle>删除开票资料</DialogTitle>
              </DialogHeader>
              <p className="text-sm break-all">{deleteRecord?.label}</p>
              {deleteError && (
                <p role="alert" className="text-sm text-destructive">
                  {deleteError}
                </p>
              )}
              <DialogFooter>
                <Button
                  variant="outline"
                  disabled={deleting}
                  onClick={() => setDeleteRecord(undefined)}
                >
                  返回
                </Button>
                <Button
                  variant="destructive"
                  disabled={deleting}
                  onClick={() => void remove()}
                >
                  <Trash2 />
                  确认删除
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </div>
      )}
    </Workspace>
  );
}

export function InvoiceApplicationDetailView({
  id,
  admin = false,
}: {
  id: number;
  admin?: boolean;
}) {
  return (
    <Workspace title="发票申请详情" admin={admin}>
      <ApplicationDetailContent id={id} admin={admin} />
    </Workspace>
  );
}
function ApplicationDetailContent({
  id,
  admin = false,
}: {
  id: number;
  admin?: boolean;
}) {
  const backHref = useAdminListReturnHref("/admin/invoice-applications", applicationStates);
  const { data, error, refresh } = useResource<Application>(
    `${admin ? "/admin" : ""}/invoice-applications/${id}`,
    false,
  );
  const [busy, setBusy] = useState(false);
  const [reviewError, setReviewError] = useState("");
  const [reviewStatus, setReviewStatus] = useState<
    "issued" | "rejected" | null
  >(null);
  const [note, setNote] = useState("");
  const [document, setDocument] = useState("");
  const [carrier, setCarrier] = useState("");
  const [tracking, setTracking] = useState("");
  const [adjustmentAction, setAdjustmentAction] = useState<"void" | "red" | null>(null);
  const [adjustmentNote, setAdjustmentNote] = useState("");
  const [adjustmentDocument, setAdjustmentDocument] = useState("");
  const [documentTarget, setDocumentTarget] = useState("0");
  const retry = useRef<{ body: string; key: string } | null>(null);
  const allowed = useAdminPermission("invoice_applications.manage");
  async function review(status: "issued" | "rejected") {
    if (busy || !data) return;
    setBusy(true);
    setReviewError("");
    try {
      const body = JSON.stringify({
        status,
        review_note: clean(note),
        document_number: status === "issued" ? clean(document) : "",
        carrier: status === "issued" ? clean(carrier) : "",
        tracking_number: status === "issued" ? clean(tracking) : "",
        expected_version: data?.version,
      });
      if (retry.current?.body !== body)
        retry.current = { body, key: `invoice-review-${crypto.randomUUID()}` };
      await api(`/admin/invoice-applications/${id}/review`, {
        method: "POST",
        body,
        headers: { "Idempotency-Key": retry.current.key },
      });
      setReviewStatus(null);
      refresh();
    } catch (e) {
      setReviewError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function adjust() {
    if (!adjustmentAction || !data || busy) return;
    setBusy(true); setReviewError("");
    try {
      await api(`/admin/invoice-applications/${id}/adjust`, { method: "POST", body: JSON.stringify({ action: adjustmentAction, note: clean(adjustmentNote), document_number: clean(adjustmentDocument), expected_version: data.version }), headers: { "Idempotency-Key": `invoice-adjust-${crypto.randomUUID()}` } });
      setAdjustmentAction(null); setAdjustmentNote(""); setAdjustmentDocument(""); refresh();
    } catch (e) { setReviewError((e as Error).message); } finally { setBusy(false); }
  }
  async function uploadDocument(file: File) {
    if (!data || busy) return;
    setBusy(true); setReviewError("");
    try {
      const form = new FormData(); form.append("file", file);
      const existing = data.documents?.find((doc) => (doc.adjustment_id ?? 0) === Number(documentTarget));
      if (existing) form.append("expected_sha256", existing.sha256);
      const target = documentTarget === "0" ? "document" : `adjustments/${documentTarget}/document`;
      await upload(`/admin/invoice-applications/${id}/${target}`, form);
      refresh();
    } catch (e) { setReviewError((e as Error).message); } finally { setBusy(false); }
  }
  return (
    <>
      <div className="mb-5">
        <Button variant="ghost" asChild>
          <Link href={admin ? backHref : "/console/invoice-applications"}>
            <Receipt />
            返回列表
          </Link>
        </Button>
      </div>
      {error ? (
        <Failure error={error} retry={refresh} />
      ) : !data ? (
        <Loading />
      ) : (
        <div className="space-y-5 max-w-4xl">
          <div className="flex flex-wrap justify-between gap-3">
            <div>
              <h1 className="page-title break-all">
                {data.title || data.profile_snapshot?.title || "发票申请"}
              </h1>
              <p className="text-sm text-muted-foreground">
                申请 #{data.id} · {money(data.amount_cents)}
              </p>
            </div>
            <ApplicationStatus status={data.status} />
          </div>
          {admin && (
            <p className="text-sm">
              {data.customer_name} · {data.customer_email}
            </p>
          )}
          <section className="border-y py-4">
            <h2 className="font-semibold mb-3">发票抬头</h2>
            <dl className="grid sm:grid-cols-2 gap-3 text-sm mb-6">
              {Object.entries({
                性质:
                  data.profile_snapshot?.profile_type === "company"
                    ? "公司"
                    : "个人",
                税号: data.profile_snapshot?.tax_number,
                开户行: data.profile_snapshot?.bank_name,
                银行账号: data.profile_snapshot?.bank_account,
                公司地址: data.profile_snapshot?.company_address,
                联系电话: data.profile_snapshot?.phone,
              }).map(
                ([label, value]) =>
                  value && (
                    <div className="min-w-0" key={label}>
                      <dt className="text-muted-foreground">{label}</dt>
                      <dd className="break-all">{value}</dd>
                    </div>
                  ),
              )}
            </dl>
            <h2 className="font-semibold mb-3">收货信息</h2>
            <p className="text-sm break-all">
              {data.recipient || data.address_snapshot?.recipient} ·{" "}
              {data.address_snapshot?.phone}
              <br />
              {[
                data.address_snapshot?.province,
                data.address_snapshot?.city,
                data.address_snapshot?.region,
                data.address_snapshot?.address,
                data.address_snapshot?.postcode,
              ]
                .filter(Boolean)
                .join(" ")}
            </p>
          </section>
          <section>
            <h2 className="font-semibold mb-3">账单项目</h2>
            <div className="border rounded-md divide-y">
              {data.items?.map((item) => (
                <div
                  key={item.source_invoice_id}
                  className="flex justify-between gap-3 p-3 text-sm"
                >
                  <span className="min-w-0 break-all">
                    {item.invoice_no} · {item.description}
                  </span>
                  <span className="money shrink-0">
                    {money(item.amount_cents)}
                  </span>
                </div>
              ))}
            </div>
          </section>
          {data.status === "issued" && (
            <section className="border-t pt-4 space-y-3">
              <h2 className="font-semibold">电子发票文件</h2>
              {data.documents?.length ? data.documents.map((file) => (
                <div key={file.id} className="flex flex-wrap items-center justify-between gap-2 text-sm">
                  <span className="break-all">{file.filename} · {file.mime_type} · {Math.ceil(file.size_bytes / 1024)} KB</span>
                  <Button variant="outline" size="sm" asChild><a href={file.download_url}><FileCheck />下载</a></Button>
                </div>
              )) : <p className="text-sm text-muted-foreground">暂无电子发票文件</p>}
              {admin && allowed && <div className="space-y-2"><Label>文件对应记录</Label><Select value={documentTarget} disabled={busy} onValueChange={setDocumentTarget}><SelectTrigger aria-label="文件对应记录"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="0">原发票</SelectItem>{data.adjustments?.map((item)=><SelectItem key={item.id} value={String(item.id)}>{item.action === "void" ? "作废记录" : "红字发票"} · {item.document_number || item.id}</SelectItem>)}</SelectContent></Select><p className="text-xs text-muted-foreground">上传至所选记录；已有文件会被替换。</p></div>}
              {admin && allowed && <label className="inline-flex"><Button asChild variant="outline" disabled={busy}><span><FileUp />上传文件</span></Button><input className="sr-only" type="file" accept=".pdf,.xml,application/pdf,application/xml" disabled={busy} onChange={(e) => { const file = e.target.files?.[0]; if (file) void uploadDocument(file); e.currentTarget.value = ""; }} /></label>}
            </section>
          )}
          {admin && allowed && data.status === "issued" && (
            <section className="border-t pt-4 space-y-3">
              <h2 className="font-semibold">作废或红冲</h2><p className="text-sm text-muted-foreground">登记已在线下完成的税务处理结果；登记不会办理税务作废、红冲或退款。</p>
              <div className="flex flex-wrap gap-2">
                <Button variant="destructive" disabled={busy} onClick={() => setAdjustmentAction("void")}><Ban />登记作废</Button>
                <Button variant="outline" disabled={busy} onClick={() => setAdjustmentAction("red")}><FileCheck />登记红冲</Button>
              </div>
              {data.adjustments?.map((item) => <p key={item.id} className="text-sm">{item.action === "void" ? "作废" : "红冲"} · {date(item.created_at)} · {item.document_number || item.note}</p>)}
            </section>
          )}
          {admin && allowed && data.status === "pending" && (
            <div className="flex gap-2">
              <Button
                variant="destructive"
                disabled={busy}
                onClick={() => {
                  setReviewError("");
                  setReviewStatus("rejected");
                }}
              >
                <Trash2 />
                驳回
              </Button>
              <Button
                disabled={busy}
                onClick={() => {
                  setReviewError("");
                  setReviewStatus("issued");
                }}
              >
                <FileCheck />
                登记已开具
              </Button>
            </div>
          )}
          {data.status !== "pending" && (
            <section className="border-t pt-4 text-sm space-y-2">
              <h2 className="font-semibold">审核记录</h2>
              <p>{data.reviewed_at ? date(data.reviewed_at) : ""}</p>
              <p className="break-all">{data.review_note}</p>
              {data.document_number && <p>发票编号：{data.document_number}</p>}
              {data.carrier && (
                <p>
                  快递：{data.carrier} · {data.tracking_number}
                </p>
              )}
            </section>
          )}
          <Dialog
            open={!!reviewStatus}
            onOpenChange={(value) => {
              if (!value && !busy) setReviewStatus(null);
            }}
          >
            <DialogContent showCloseButton={!busy}>
              <DialogHeader>
                <DialogTitle>
                  {reviewStatus === "issued" ? "登记发票开具" : "驳回申请"}
                </DialogTitle>
              </DialogHeader>
              {reviewStatus === "issued" && (
                <>
                  <Label htmlFor="application-document">发票编号</Label>
                  <Input
                    id="application-document"
                    maxLength={120}
                    value={document}
                    onChange={(e) => setDocument(e.target.value)}
                    disabled={busy}
                  />
                  <Label htmlFor="application-carrier">快递公司</Label>
                  <Input
                    id="application-carrier"
                    maxLength={120}
                    value={carrier}
                    onChange={(e) => setCarrier(e.target.value)}
                    disabled={busy}
                  />
                  <Label htmlFor="application-tracking">快递单号</Label>
                  <Input
                    id="application-tracking"
                    maxLength={160}
                    value={tracking}
                    onChange={(e) => setTracking(e.target.value)}
                    disabled={busy}
                  />
                </>
              )}
              <Label htmlFor="application-note">
                {reviewStatus === "rejected" ? "驳回原因" : "审核备注"}
              </Label>
              <Textarea
                id="application-note"
                maxLength={500}
                value={note}
                onChange={(e) => setNote(e.target.value)}
                disabled={busy}
              />
              {reviewError && (
                <p role="alert" className="text-sm text-destructive">
                  {reviewError}
                </p>
              )}
              <DialogFooter>
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() => setReviewStatus(null)}
                >
                  返回
                </Button>
                <Button
                  disabled={
                    busy ||
                    (!clean(document) && reviewStatus === "issued") ||
                    (!clean(note) && reviewStatus === "rejected")
                  }
                  onClick={() => {
                    if (reviewStatus) void review(reviewStatus);
                  }}
                >
                  <FileCheck />
                  确认
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
          <Dialog open={!!adjustmentAction} onOpenChange={(value) => { if (!value && !busy) setAdjustmentAction(null); }}>
            <DialogContent showCloseButton={!busy}><DialogHeader><DialogTitle>{adjustmentAction === "void" ? "登记作废" : "登记红冲"}</DialogTitle></DialogHeader>
              {adjustmentAction === "red" && <><Label htmlFor="adjustment-document">红字发票编号</Label><Input id="adjustment-document" maxLength={120} value={adjustmentDocument} onChange={(e) => setAdjustmentDocument(e.target.value)} disabled={busy} /></>}
              <Label htmlFor="adjustment-note">登记原因</Label><Textarea id="adjustment-note" maxLength={500} value={adjustmentNote} onChange={(e) => setAdjustmentNote(e.target.value)} disabled={busy} />
              <DialogFooter><Button variant="outline" disabled={busy} onClick={() => setAdjustmentAction(null)}>返回</Button><Button disabled={busy || !clean(adjustmentNote) || (adjustmentAction === "red" && !clean(adjustmentDocument))} onClick={() => void adjust()}><FileCheck />确认</Button></DialogFooter>
            </DialogContent>
          </Dialog>
          {reviewError && (
            <p role="alert" className="text-sm text-destructive">
              {reviewError}
            </p>
          )}
        </div>
      )}
    </>
  );
}
