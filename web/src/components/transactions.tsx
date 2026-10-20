"use client";
import { PurchaseDiscountSummary } from "./purchase-discount-details";

import { renewalBenefitLabel } from "./renewal-benefit";
import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { ArrowLeft, GitMerge, RefreshCw, RotateCcw, Search, Wallet } from "lucide-react";
import { api, money, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { transactionReturnHref } from "@/lib/transaction-return";
import { useResource } from "@/hooks/use-resource";
import { useAdminListNavigation, useAdminListReturnHref } from "@/hooks/use-admin-list-navigation";
import { ResourceTable, ResourcePagination, DetailFields } from "./resource-table";
import { MutationDialog } from "./mutation-dialog";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { Failure, Loading, Status } from "./common";
import { Workspace, useAdminPermission } from "./workspace";
import { InvoiceCreateLink, ManualInvoiceTools, type ManualInvoiceRecord } from "./manual-invoices";
import { OnlinePayment, paymentLabels, type PaymentRecord } from "./online-payment";
import { TopupInvoiceActions, type TopupState } from "./topup-invoice-actions";
import { ExternalRefunds } from "./external-refunds";
import { PaymentClose } from "./payment-close";
import { PaymentExceptionPanel } from "./payment-exception-panel";
import { PaymentReconciliation } from "./payment-reconciliation";
import { AdminStatusTabs } from "./admin-status-tabs";
import { type OrderItem, type OrderRow, OrderItems, OrderCancel } from "./transaction-orders";
import { time, OrderLink, ServiceLink } from "./transaction-links";

type Invoice = Omit<ManualInvoiceRecord, "items"> & {
  topup?: TopupState;
  id: number;
  invoice_no: string;
  order_id: number | null;
  order_no: string | null;
  customer_name?: string;
  email?: string;
  status: string;
  amount_cents: number;
  created_at: string;
  due_at: string | null;
  paid_at: string | null;
  import_review_required: boolean;
  payment_pending: boolean;
  real_payment_pending: boolean;
  payment_close_anomaly?: boolean;
  topup_can_retry?: boolean;
  external_payment_paid: boolean;
 credit_payment?:{remaining_cents:number;amount_cents:number;status:string}|null;
  discount_cents: number;
  group_discount_cents?: number | null;
  promo_discount_cents?: number | null;
  promo_code: string;
  items: {
    id: number;
    description: string;
    item_type: string;
    amount_cents: number;
    renewal_base_cents: number | null;
    renewal_discount_cents: number;
    renewal_discount_percent: number;
    renewal_fixed_discount_cents: number;
    renewal_discount_limit: number;
    renewal_discount_ordinal: number;
    renewal_promo_code: string;
    upgrade_base_cents: number | null;
    upgrade_discount_cents: number;
    upgrade_promo_code: string;
    service_id: number | null;
    service_name: string | null;
  }[];
  order_items: OrderItem[];
  payments: PaymentRecord[];
  ledger: {
    id: number;
    entry_type: string;
    delta_cents: number;
    balance_after: number;
    created_at: string;
  }[];
};

type TransactionPage<T> = {
  items: T[];
  total: number;
  limit: number;
  offset: number;
};

type InvoiceActionRow = Pick<
  Invoice,
  | "id"
  | "invoice_no"
  | "status"
  | "amount_cents"
  | "import_review_required"
  | "payment_pending"
  | "real_payment_pending"
  | "topup_can_retry"
  | "payment_close_anomaly"
  | "external_payment_paid"
 | "credit_payment"
  | "version"
> & { payments?: PaymentRecord[]; invoice_kind?: string; topup?: TopupState; topup_status?: string; topup_pending_liability_cents?: number };

type InvoiceRow = InvoiceActionRow &
  Pick<
    Invoice,
    | "user_id"
    | "invoice_kind"
    | "order_id"
    | "order_no"
    | "customer_name"
    | "due_at"
  >;

type MergeSelection = {
  invoice_id: number;
  expected_version: number;
  invoice_no: string;
  amount_cents: number;
  user_id: number;
  customer_name?: string;
};

const orderStates = {
  pending: "待支付",
  paid: "已支付",
  active: "已启用",
  cancelled: "已取消",
  refunded: "已退款",
};

const invoiceStates = {
  unpaid: "待支付",
  paid: "已支付",
  cancelled: "已取消",
  refunded: "已退款",
};

const orderStatusValues = Object.keys(orderStates);

const invoiceStatusValues = Object.keys(invoiceStates);

export function CustomerOrdersView() {
  return <TransactionList kind="orders" />;
}

export function InvoiceListView({ admin = false }: { admin?: boolean }) {
  return <TransactionList kind="invoices" admin={admin} />;
}

function TransactionList({
  kind,
  admin = false,
}: {
  kind: "orders" | "invoices";
  admin?: boolean;
}) {
  const router = useRouter();
  const { data: currentUser } = useResource<User>(admin ? "/me" : null, false);
  const mergePermission = admin && can(currentUser, "invoices.manage");
  const { draft, setDraft, query, setQuery, status: state, setStatus: setState, page, setPage, ready, suffix } = useAdminListNavigation(admin || kind === "invoices", kind === "orders" ? orderStatusValues : invoiceStatusValues);
  const offset = page * 20;
  const [selected, setSelected] = useState<Record<number, MergeSelection>>({});
  const [mergeOpen, setMergeOpen] = useState(false);
  const [mergeBusy, setMergeBusy] = useState(false);
  const [mergeError, setMergeError] = useState("");
  const mergeRetry = useRef<{ body: string; key: string } | null>(null);
  const title =
    kind === "orders" ? "我的订单" : admin ? "财务账单" : "我的账单";
  const params = new URLSearchParams({
    q: query,
    limit: "20",
    offset: String(offset),
  });
  if (state !== "all") params.set("status", state);
  const { data, error, refresh } = useResource<
    TransactionPage<OrderRow | InvoiceRow>
  >(ready ? `${admin ? "/admin" : ""}/${kind}/page?${params}` : null, false);
  const base = admin ? "/admin" : "/console";
  const selections = Object.values(selected).sort(
    (a, b) => a.invoice_id - b.invoice_id,
  );
  const sameCustomer =
    selections.length > 0 &&
    selections.every((item) => item.user_id === selections[0].user_id);
  const mergeReady =
    admin &&
    mergePermission &&
    selections.length >= 2 &&
    selections.length <= 20 &&
    sameCustomer;
  const pageMergeable =
    kind === "invoices" && admin && mergePermission
      ? (data?.items.filter(
          (row): row is InvoiceRow =>
            "invoice_no" in row &&
            row.invoice_kind === "manual" &&
            row.status === "unpaid",
        ) ?? [])
      : [];
  const pageSelected = pageMergeable.filter((row) => !!selected[row.id]);
  const allPageSelected =
    pageMergeable.length > 0 && pageSelected.length === pageMergeable.length;
  function toggleInvoice(row: InvoiceRow, checked: boolean) {
    setSelected((current) => {
      const next = { ...current };
      if (checked)
        next[row.id] = {
          invoice_id: row.id,
          expected_version: row.version,
          invoice_no: row.invoice_no,
          amount_cents: row.amount_cents,
          user_id: row.user_id,
          customer_name: row.customer_name,
        };
      else delete next[row.id];
      return next;
    });
  }
  function togglePage(checked: boolean) {
    setSelected((current) => {
      const next = { ...current };
      for (const row of pageMergeable) {
        if (checked)
          next[row.id] = {
            invoice_id: row.id,
            expected_version: row.version,
            invoice_no: row.invoice_no,
            amount_cents: row.amount_cents,
            user_id: row.user_id,
            customer_name: row.customer_name,
          };
        else delete next[row.id];
      }
      return next;
    });
  }
  async function mergeInvoices() {
    if (!mergeReady || mergeBusy) return;
    const body = JSON.stringify({
      sources: selections.map((item) => ({
        invoice_id: item.invoice_id,
        expected_version: item.expected_version,
      })),
    });
    if (mergeRetry.current?.body !== body)
      mergeRetry.current = { body, key: `manual-merge-${crypto.randomUUID()}` };
    setMergeBusy(true);
    setMergeError("");
    try {
      const result = await api<{ invoice_id: number }>(
        "/admin/invoices/merge",
        {
          method: "POST",
          body,
          headers: { "Idempotency-Key": mergeRetry.current.key },
        },
      );
      setMergeOpen(false);
      setSelected({});
      router.push(`/admin/invoices/${result.invoice_id}`);
      router.refresh();
    } catch (e) {
      setMergeError((e as Error).message);
    } finally {
      setMergeBusy(false);
    }
  }
  return (
    <Workspace title={title} admin={admin}>
      <div className={`${admin ? "admin-page-heading " : ""}flex flex-wrap items-center justify-between gap-3 mb-6`}>
        <h1 className={admin ? "page-title" : "text-2xl font-semibold"}>{title}</h1>
        <div className="flex flex-wrap gap-2">
          {admin && <InvoiceCreateLink />}
          {mergeReady && !admin && (
            <Button
              variant="outline"
              onClick={() => {
                setMergeError("");
                setMergeOpen(true);
              }}
            >
              <GitMerge />
              合并账单
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon"
            aria-label="刷新交易列表"
            title="刷新交易列表"
            onClick={refresh}
          >
            <RefreshCw />
          </Button>
        </div>
      </div>
      {admin && <AdminStatusTabs label="账单状态" value={state} states={invoiceStates} onChange={value => { setState(value); setPage(0); }} />}
      <form
        className={`grid ${admin ? "sm:grid-cols-[minmax(0,1fr)_auto]" : "sm:grid-cols-[10rem_minmax(0,1fr)_auto]"} gap-3 mb-4`}
        onSubmit={(e) => {
          e.preventDefault();
          setQuery(draft);
          setPage(0);
        }}
      >
        {!admin && <Select
          value={state}
          onValueChange={(value) => {
            setState(value);
            setPage(0);
          }}
        >
          <SelectTrigger
            aria-label={kind === "orders" ? "订单状态筛选" : "账单状态筛选"}
            className="w-full"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部状态</SelectItem>
            {Object.entries(
              kind === "orders" ? orderStates : invoiceStates,
            ).map(([value, label]) => (
              <SelectItem key={value} value={value}>
                {label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>}
        <Input
          aria-label="搜索交易"
          placeholder={
            kind === "orders"
              ? "订单号或产品名称"
              : admin
                ? "账单号、订单号、项目或客户"
                : "账单号、订单号或项目"
          }
          value={draft}
          maxLength={120}
          onChange={(e) => setDraft(e.target.value)}
        />
        <Button variant="outline" type="submit">
          <Search />
          搜索
        </Button>
      </form>
      {error ? (
        <Failure error={error} retry={refresh} />
      ) : !data ? (
        <Loading />
      ) : (
        <>
          <ResourceTable<OrderRow | InvoiceRow>
            items={data.items}
            rowKey={(row) => row.id}
            className={`${admin ? "admin-list-surface data-surface" : "border rounded-md"} overflow-auto`}
            headers={[
              ...(kind === "invoices" && admin && mergePermission
                ? [
                    <Checkbox
                      key="select"
                      aria-label="选择当前页可合并账单"
                      checked={allPageSelected ? true : pageSelected.length > 0 ? "indeterminate" : false}
                      onCheckedChange={(value) => togglePage(value === true)}
                    />,
                  ]
                : []),
              kind === "orders" ? "订单号" : "账单号",
              ...(admin ? ["客户"] : []),
              ...(kind === "invoices" ? ["关联订单"] : []),
              "金额",
              kind === "orders" ? "创建时间" : "到期时间",
              "状态",
              "操作",
            ]}
            renderRow={(row) =>
              "invoice_no" in row ? (
                <>
                  {admin && mergePermission && (
                    <TableCell>
                      {row.invoice_kind === "manual" && row.status === "unpaid" && (
                        <Checkbox
                          aria-label={`选择账单 ${row.invoice_no}`}
                          checked={!!selected[row.id]}
                          onCheckedChange={(value) => toggleInvoice(row, value === true)}
                        />
                      )}
                    </TableCell>
                  )}
                  <TableCell>
                    <Link
                      className="font-mono text-xs text-primary hover:underline break-all whitespace-normal"
                      href={`${base}/invoices/${row.id}${suffix}`}
                    >
                      {row.invoice_no}
                    </Link>
                  </TableCell>
                  {admin && <TableCell>{row.customer_name}</TableCell>}
                  <TableCell>
                    {row.order_id ? (
                      <OrderLink admin={admin} id={row.order_id} label={row.order_no ?? `#${row.order_id}`} />
                    ) : (
                      "-"
                    )}
                  </TableCell>
                  <TableCell className="money">{money(row.amount_cents)}</TableCell>
                  <TableCell>{time(row.due_at)}</TableCell>
                  <TableCell>
                    <Status value={row.status} />
                  </TableCell>
                  <TableCell>
                    <InvoiceActions
                      key={`${row.id}:${row.status}`}
                      listSuffix={suffix}
                      invoice={row}
                      admin={admin}
                      refresh={refresh}
                    />
                  </TableCell>
                </>
              ) : (
                <>
                  <TableCell>
                    <Link
                      className="font-mono text-xs text-primary hover:underline break-all whitespace-normal"
                      href={`/console/orders/${row.id}`}
                    >
                      {row.order_no}
                    </Link>
                  </TableCell>
                  <TableCell className="money">{money(row.total_cents)}</TableCell>
                  <TableCell>{time(row.created_at)}</TableCell>
                  <TableCell>
                    <Status value={row.status} />
                  </TableCell>
                  <TableCell>
                    <OrderCancel key={`${row.id}:${row.status}`} order={row} refresh={refresh} />
                  </TableCell>
                </>
              )
            }
          />
          <MutationDialog
            open={mergeOpen}
            busy={mergeBusy}
            onClose={() => setMergeOpen(false)}
            title={<>合并人工账单</>}
            confirm={
              <>
                <GitMerge />
                {mergeBusy ? "合并中" : "确认合并"}
              </>
            }
            onConfirm={() => void mergeInvoices()}
            confirmDisabled={mergeBusy || !mergeReady}
          >
            <p className="text-sm text-muted-foreground">将生成一张新账单，原账单会作废并保留历史记录。</p>
            <div className="max-h-56 overflow-auto border rounded-md divide-y">
              {selections.map((item) => (
                <div
                  key={item.invoice_id}
                  className="flex items-center justify-between gap-3 px-3 py-2 text-sm"
                >
                  <span className="font-mono break-all">{item.invoice_no}</span>
                  <span className="money shrink-0">{money(item.amount_cents)}</span>
                </div>
              ))}
            </div>
            {!sameCustomer && (
              <p role="alert" className="text-sm text-destructive">
                只能合并同一客户的账单。
              </p>
            )}
            {selections.length > 20 && (
              <p role="alert" className="text-sm text-destructive">
                最多合并 20 张账单。
              </p>
            )}
            {mergeError && (
              <p role="alert" className="text-sm text-destructive break-words">
                {mergeError}
              </p>
            )}
          </MutationDialog>
          <ResourcePagination
            total={data.total}
            offset={offset}
            limit={20}
            unit={kind === "orders" ? "订单" : "账单"}
            onOffsetChange={(value) => setPage(value / 20)}
          >
            {admin && (
              <div className="flex flex-wrap items-center gap-3">
                {mergePermission && (
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={!mergeReady}
                    onClick={() => {
                      setMergeError("");
                      setMergeOpen(true);
                    }}
                  >
                    <GitMerge />
                    合并账单{selections.length ? ` (${selections.length})` : ""}
                  </Button>
                )}
                <span className="text-xs text-muted-foreground">
                  本页金额{" "}
                  <span className="money">
                    {money(
                      data.items.reduce(
                        (sum, item) => sum + ("amount_cents" in item ? item.amount_cents : item.total_cents),
                        0,
                      ),
                    )}
                  </span>
                </span>
              </div>
            )}
          </ResourcePagination>
        </>
      )}
    </Workspace>
  );
}

function InvoiceActions({
  invoice,
  admin,
  refresh,
  listSuffix = "",
}: {
  invoice: InvoiceActionRow;
  admin: boolean;
  listSuffix?: string;
  refresh: () => void;
}) {
  const allowed = useAdminPermission("invoices.refund");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState(false);
  const key = useRef<Record<string, string>>({});
 const paymentOwner=useRef<number|null>(null);
 const originalPaymentOwner=useRef<number|null>(null);
 const [paymentOwnerError,setPaymentOwnerError]=useState("");
 const [paymentOwnerRevision,setPaymentOwnerRevision]=useState(0);
 const [paymentOwnerReady,setPaymentOwnerReady]=useState(false);
 useEffect(()=>{
   if(admin)return;
   const controller=new AbortController();
   paymentOwner.current=null;
   queueMicrotask(()=>{if(!controller.signal.aborted){setPaymentOwnerReady(false);setPaymentOwnerError("");}});
   void api<User>("/me",{signal:controller.signal}).then(u=>{
     if(controller.signal.aborted)return;
     if(u.role!=="customer")throw new Error("请使用客户账户付款");
     if(originalPaymentOwner.current!==null&&originalPaymentOwner.current!==u.id)throw new Error("账户已切换，请使用原账户重新打开账单。");
     originalPaymentOwner.current=u.id;
     paymentOwner.current=u.id;
     setPaymentOwnerReady(true);
   }).catch((e:Error)=>{if(!controller.signal.aborted)setPaymentOwnerError(`无法核实付款账户：${e.message}`);});
   return()=>controller.abort();
 },[admin,invoice.id,paymentOwnerRevision]);

  async function act(action: "pay" | "refund") {
    if(busy||(!admin&&!paymentOwnerReady))return;
    setBusy(true);
    setError("");
    const requestKey = `${action}:${invoice.version}`;
    key.current[requestKey] ??= `web-invoice-${action}-${invoice.id}-${crypto.randomUUID()}`;
    try {
      if(!admin&&action==="pay"){
        if(paymentOwner.current===null)throw new Error("付款账户尚未核实，请重新核实账户。");
        try{
          const live=await api<User>("/me");
          if(live.role!=="customer"||live.id!==paymentOwner.current)throw new Error("账户已切换，请使用原账户重新打开账单。");
        }catch(e){paymentOwner.current=null;setPaymentOwnerReady(false);setPaymentOwnerError((e as Error).message);throw e;}
      }
      await api(`${action === "refund" ? "/admin" : ""}/invoices/${invoice.id}/${action}`, {
        method: "POST",
        body: JSON.stringify(action!=="refund"?{expected_version:invoice.version}:{}),
        headers: { "Idempotency-Key": key.current[requestKey] },
      });
      setConfirm(false);
      refresh();
    } catch(e){setError((e as Error).message);}
    finally{setBusy(false);}
  }
  if (invoice.invoice_kind === "topup" && !invoice.topup) {
    const labels: Record<string, string> = { created: "待付款", accepted: "付款待核对", credited: "已入账", pending_liability: "已收到款，余额待核对", cancelled: "已取消" };
    return <div className="space-y-2">
      {invoice.topup_status && <p className="text-xs text-muted-foreground whitespace-normal">{labels[invoice.topup_status] ?? "充值状态待核对"}</p>}
      <Button size="sm" variant="outline" asChild><Link href={`${admin ? "/admin" : "/console"}/invoices/${invoice.id}${listSuffix}`}>查看充值账单</Link></Button>
    </div>;
  }
  if (invoice.invoice_kind === "topup") return <TopupInvoiceActions invoice={invoice} admin={admin} refresh={refresh} />;
  if (admin && (invoice.status !== "paid" || !allowed)) return null;
  if (admin && invoice.credit_payment) return <p className="text-sm">该付款方式已下线，无法在此办理退款。</p>;
  if (admin && invoice.external_payment_paid) {
    return (
      <p className="max-w-sm text-xs text-muted-foreground whitespace-normal">
        在线支付暂不支持自动原路退款。请由财务在商户后台人工退款并核验实际到账，不会退入本站余额。
      </p>
    );
  }
  if (!admin && invoice.status !== "unpaid"&&paymentOwnerReady) return <OnlinePayment invoice={invoice} refresh={refresh} />;
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-2">
        {admin ? (
          <>
            <Button
              size="sm"
              variant="outline"
              disabled={busy || invoice.import_review_required}
              onClick={() => setConfirm(true)}
            >
              <RotateCcw />
              退款至余额
            </Button>
            <MutationDialog
              open={confirm}
              busy={busy}
              onClose={() => setConfirm(false)}
              title="退款至账户余额"
              confirm={
                <>
                  <RotateCcw />
                  确认退款
                </>
              }
              onConfirm={() => void act("refund")}
              confirmDisabled={busy}
            >
              <p className="text-sm break-all">
                {invoice.invoice_no} · {money(invoice.amount_cents)}
              </p>
              {error && (
                <p role="alert" className="text-sm text-destructive">
                  {error}
                </p>
              )}
            </MutationDialog>
          </>
        ) : (
          <>
            {!paymentOwnerReady && <div className="w-full space-y-2 text-sm" role={paymentOwnerError ? "alert" : "status"}><p>{paymentOwnerError || "正在核实付款账户，核实完成前暂不可付款。"}</p>{paymentOwnerError && <Button size="sm" variant="outline" disabled={busy} onClick={()=>setPaymentOwnerRevision(value=>value+1)}>重新核实付款账户</Button>}</div>}
            <Button
              size="sm"
              disabled={busy || !paymentOwnerReady || invoice.import_review_required || invoice.real_payment_pending}
              onClick={() => void act("pay")}
            >
              <Wallet />
              余额支付
            </Button>
            {invoice.amount_cents > 0 && paymentOwnerReady && (
              <OnlinePayment invoice={invoice} refresh={refresh} />
            )}
          </>
        )}
      </div>
      {invoice.import_review_required && (
        <p className="text-xs text-muted-foreground">账单待复核</p>
      )}
      {!admin && invoice.payment_pending && (
        <p className="text-xs text-muted-foreground">
          {invoice.real_payment_pending ? "已有在线支付，请继续原支付单；处理完成前无法改用余额支付或取消。" : "支付单待处理"}
        </p>
      )}
      {error && !confirm && (
        <p role="alert" className="text-xs text-destructive break-words">
          {error}
        </p>
      )}
    </div>
  );
}

export function InvoiceDetailView({
  id,
  admin = false,
}: {
  id: number;
  admin?: boolean;
}) {
  const searchParams = useSearchParams();
  const transactionBack = admin ? transactionReturnHref(searchParams.get("return_to")) : null;
  const renewal = admin && searchParams.get("kind") === "renew";
  const supplierReturn=searchParams.get("supplier_return");
  const supplierBack=admin && supplierReturn && /^\/admin\/suppliers\/renewals(?:\?|$)/.test(supplierReturn) ? supplierReturn : null;
  const returnHref = useAdminListReturnHref(renewal ? "/admin/orders" : `${admin ? "/admin" : "/console"}/invoices`, renewal ? ["pending", "paid", "cancelled", "refunded"] : invoiceStatusValues, "all", renewal ? { kind: "renew", limit: "25", customer_id: "" } : undefined);
  const { data, error, refresh } = useResource<Invoice>(
    `${admin ? "/admin" : ""}/invoices/${id}`,
    false,
  );
  useEffect(() => {
    if (!data?.payment_pending) return;
    const timer = window.setInterval(refresh, 3000);
    return () => window.clearInterval(timer);
  }, [data?.payment_pending, refresh]);
  return (
    <Workspace title="账单详情" admin={admin}>
      <Button variant="ghost" className="mb-5" asChild>
        <Link href={admin ? transactionBack ?? supplierBack ?? returnHref : (searchParams.get("from") === "topup" ? "/console/account" : returnHref)}>
          <ArrowLeft />
          {!admin && searchParams.get("from") === "topup" ? "返回账户余额" : transactionBack ? "返回交易流水" : supplierBack ? "返回供应商续费列表" : renewal ? "返回续费订单列表" : "返回账单列表"}
        </Link>
      </Button>
      {error ? (
        <Failure error={error} retry={refresh} />
      ) : !data ? (
        <Loading />
      ) : (
        <>
          <div className={`${admin ? "admin-page-heading " : ""}border-b pb-5 flex flex-wrap justify-between items-start gap-4`}>
            <div className="min-w-0">
              <h1 className="font-mono text-sm sm:text-xl font-semibold break-all">
                {data.invoice_no}
              </h1>
              <p className="text-xs text-muted-foreground mt-2">
                {time(data.created_at)}
              </p>
            </div>
            <div className="flex flex-wrap gap-3 items-center">
              <Status value={data.status} />
              <Button
                variant="ghost"
                size="icon"
                aria-label="刷新账单"
                title="刷新账单"
                onClick={refresh}
              >
                <RefreshCw />
              </Button>
            </div>
          </div>
          <div className="py-4 grid sm:grid-cols-2 lg:grid-cols-4 gap-5">
            <div>
              <p className="text-sm text-muted-foreground">账单金额</p>
              <p className="text-2xl font-semibold money mt-2">
                {money(data.amount_cents)}
              </p>
              <PurchaseDiscountSummary discountCents={data.discount_cents} groupDiscountCents={data.group_discount_cents} promoDiscountCents={data.promo_discount_cents} promoCode={data.promo_code} />
            </div>
            <DetailFields
              className="contents"
              fields={{
                到期时间: time(data.due_at),
                付款时间: time(data.paid_at),
                关联订单: data.order_id ? (
                  <OrderLink admin={admin} id={data.order_id} label={data.order_no ?? `#${data.order_id}`} />
                ) : (
                  "-"
                ),
              }}
            />
          </div>
          {admin && (
            <div className="mb-4 grid gap-2 border-b pb-4 text-sm sm:grid-cols-2"><div><span className="text-muted-foreground mr-3">客户</span>{data.customer_name || `#${data.user_id}`}<span className="ml-2 text-muted-foreground break-all">{data.email}</span></div><div><span className="text-muted-foreground mr-3">账单类型</span>{data.invoice_kind === "topup" ? "在线充值账单" : data.invoice_kind === "manual" ? "人工账单" : "自动账单"}</div></div>
          )}
          <div className="pb-5 flex flex-wrap gap-3">
            <InvoiceActions
              key={data.invoice_kind === "topup" ? `topup:${data.id}` : `${data.id}:${data.version}:${data.status}`}
              invoice={data}
              admin={admin}
              refresh={refresh}
            />
            {admin && (
              <ManualInvoiceTools
                key={`${data.id}:${data.version}`}
                invoice={data}
                refresh={refresh}
              />
            )}
          </div>
          <section className="border-y py-5">
            <h2 className="font-semibold mb-3">账单项目</h2>
            {data.items.length > 0 ? (
              <div className="overflow-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>项目</TableHead>
                      <TableHead>服务</TableHead>
                      <TableHead>金额</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {data.items.map((item) => (
                      <TableRow key={item.id}>
                        <TableCell className="whitespace-normal break-all">
                          {item.description}
                          {item.upgrade_promo_code && <p className="text-xs text-muted-foreground mt-1 break-words">升级差额 {money(item.upgrade_base_cents ?? 0)} · {item.upgrade_promo_code}（升级优惠 {money(item.upgrade_discount_cents)}）</p>}
                          {item.renewal_promo_code && <p className="text-xs text-muted-foreground mt-1 break-words">原价 {money(item.renewal_base_cents ?? 0)} · {item.renewal_promo_code} {renewalBenefitLabel(item)}（实际优惠 {money(item.renewal_discount_cents)}）{item.renewal_discount_limit>0?` · 第 ${item.renewal_discount_ordinal} / ${item.renewal_discount_limit} 次`:""}</p>}
                        </TableCell>
                        <TableCell>
                          {item.service_id ? (
                            <ServiceLink
                              admin={admin}
                              id={item.service_id}
                              label={item.service_name ?? `#${item.service_id}`}
                            />
                          ) : (
                            "-"
                          )}
                        </TableCell>
                        <TableCell className="money">
                          {money(item.amount_cents)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            ) : (
              <OrderItems items={data.order_items} />
            )}
          </section>
          <section className="py-5 border-b">
            <h2 className="font-semibold mb-3">付款记录</h2>
            {data.payment_close_anomaly && <p role="alert" className="mb-3 text-sm text-destructive">已关闭支付出现迟到收款异常，账单已冻结，请联系财务核验；请勿重复付款或取消。原收款与操作证据保留。</p>}
            <div className="overflow-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>渠道</TableHead>
                    <TableHead>流水号</TableHead>
                    <TableHead>金额</TableHead>
                    <TableHead>时间</TableHead>
                    <TableHead>状态</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.ledger.map((entry) => (
                    <TableRow key={`balance-${entry.id}`}>
                      <TableCell>
                        {entry.entry_type === "refund" && entry.delta_cents < 0 ? "外部退款余额冲正" : data.invoice_kind === "topup" ? "在线充值入账" : entry.entry_type === "refund"
                          ? "余额退款"
                          : "余额支付"}
                      </TableCell>
                      <TableCell>#{entry.id}</TableCell>
                      <TableCell className="money">
                        {money(entry.entry_type === "refund" && entry.delta_cents < 0 ? entry.delta_cents : Math.abs(entry.delta_cents))}
                      </TableCell>
                      <TableCell>{time(entry.created_at)}</TableCell>
                      <TableCell>
                        <Status value="succeeded" />
                      </TableCell>
                    </TableRow>
                  ))}
                  {data.payments.map((payment) => (
                    <TableRow key={`gateway-${payment.id}`}>
                      <TableCell>
                        {paymentLabels[payment.provider] ?? payment.provider}
                      </TableCell>
                      <TableCell className="min-w-40 font-mono text-xs break-all whitespace-normal">
                        {payment.external_transaction_id || payment.provider_payment_id}
                      </TableCell>
                      <TableCell className="money">
                        {money(payment.amount_cents)}
                      </TableCell>
                      <TableCell>
                        {time(payment.completed_at ?? payment.created_at)}
                      </TableCell>
                      <TableCell className="min-w-48">
                        {payment.expired ? (
                          <span className="text-muted-foreground">本地已过期</span>
                        ) : (
                          <Status value={payment.status} />
                        )}
                        {admin && <PaymentClose invoiceId={data.id} payment={payment} admin={admin} refresh={refresh} />}
                        <PaymentReconciliation
                          invoiceId={data.id}
                          payment={payment}
                          admin={admin}
                          refresh={refresh}
                        />
                      </TableCell>
                    </TableRow>
                  ))}
                  {data.ledger.length === 0 && data.payments.length === 0 && (
                    <TableRow>
                      <TableCell
                        colSpan={5}
                        className="h-24 text-center text-muted-foreground"
                      >
                        暂无付款记录
                      </TableCell>
                    </TableRow>
                  )}
                </TableBody>
              </Table>
            </div>
          </section>
          {admin && <PaymentExceptionPanel invoiceId={data.id} payments={data.payments} refresh={refresh} />}
          {admin && <ExternalRefunds invoice={data} refreshInvoice={refresh} />}
        </>
      )}
    </Workspace>
  );
}

export { CustomerOrderDetailView } from "./transaction-orders";
