"use client";

import { RenewalBenefitDetails,renewalBenefitLabel } from "./renewal-benefit";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  ArrowLeft,
  ArrowRight,
  Ban,
  GitMerge,
  RefreshCw,
  RotateCcw,
  Search,
  Wallet,
} from "lucide-react";
import { api, date, money, type OptionSnapshot, type User } from "@/lib/api";
import { can } from "@/lib/admin-navigation";
import { useResource } from "@/hooks/use-resource";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "./ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "./ui/table";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "./ui/dialog";
import { Failure, Loading, Status, cycles } from "./common";
import { Workspace, useAdminPermission } from "./workspace";
import {
  InvoiceCreateLink,
  ManualInvoiceTools,
  type ManualInvoiceRecord,
} from "./manual-invoices";
import {
  OnlinePayment,
  paymentLabels,
  type PaymentRecord,
} from "./online-payment";
import { PaymentReconciliation } from "./payment-reconciliation";
import { PurchaseFieldDetails } from "./purchase-fields";
import { AdminStatusTabs } from "./admin-status-tabs";
import type { PurchaseFieldSnapshot } from "@/lib/api";

type OrderItem = {
  purchase_fields?: PurchaseFieldSnapshot[];
  id: number;
  product_name: string;
  billing_cycle: string;
  quantity: number;
  price_cents: number;
  setup_fee_cents: number;
  total_cents: number;
  config_options: number[];
  config_snapshot: OptionSnapshot[];
  renewal_discount_percent: number;
  renewal_fixed_discount_cents: number;
  renewal_discount_limit: number;
  renewal_promo_code: string;
};
type Order = {
  id: number;
  order_no: string;
  status: string;
  review_status: string;
  total_cents: number;
  created_at: string;
  invoice_id: number | null;
  invoice_no?: string;
  invoice_status: string | null;
  discount_cents: number;
  promo_code: string;
  items: OrderItem[];
  services: {
    id: number;
    name: string;
    domain: string;
    status: string;
    next_due_at: string | null;
  }[];
};
type Invoice = Omit<ManualInvoiceRecord, "items"> & {
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
  discount_cents: number;
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
type OrderRow = Pick<
  Order,
  "id" | "order_no" | "status" | "total_cents" | "created_at"
>;
type InvoiceActionRow = Pick<
  Invoice,
  | "id"
  | "invoice_no"
  | "status"
  | "amount_cents"
  | "import_review_required"
  | "payment_pending"
  | "real_payment_pending"
  | "version"
> & { payments?: PaymentRecord[] };
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
function time(value: string | null | undefined) {
  return value ? date(value) : "-";
}

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
  const [draft, setDraft] = useState("");
  const [query, setQuery] = useState("");
  const [state, setState] = useState("all");
  const [offset, setOffset] = useState(0);
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
  >(`${admin ? "/admin" : ""}/${kind}/page?${params}`, false);
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
      {admin && <AdminStatusTabs label="账单状态" value={state} states={invoiceStates} onChange={value => { setState(value); setOffset(0); }} />}
      <form
        className={`grid ${admin ? "sm:grid-cols-[minmax(0,1fr)_auto]" : "sm:grid-cols-[10rem_minmax(0,1fr)_auto]"} gap-3 mb-4`}
        onSubmit={(e) => {
          e.preventDefault();
          setQuery(draft);
          setOffset(0);
        }}
      >
        {!admin && <Select
          value={state}
          onValueChange={(value) => {
            setState(value);
            setOffset(0);
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
          <div className={`${admin ? "admin-list-surface data-surface" : "border rounded-md"} overflow-auto`}>
            <Table>
              <TableHeader>
                <TableRow>
                  {kind === "invoices" && admin && mergePermission && (
                    <TableHead>
                      <Checkbox
                        aria-label="选择当前页可合并账单"
                        checked={
                          allPageSelected
                            ? true
                            : pageSelected.length > 0
                              ? "indeterminate"
                              : false
                        }
                        onCheckedChange={(value) => togglePage(value === true)}
                      />
                    </TableHead>
                  )}
                  <TableHead>
                    {kind === "orders" ? "订单号" : "账单号"}
                  </TableHead>
                  {admin && <TableHead>客户</TableHead>}
                  {kind === "invoices" && <TableHead>关联订单</TableHead>}
                  <TableHead>金额</TableHead>
                  <TableHead>
                    {kind === "orders" ? "创建时间" : "到期时间"}
                  </TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.items.length === 0 && (
                  <TableRow>
                    <TableCell
                      colSpan={
                        (admin ? 7 : kind === "invoices" ? 6 : 5) +
                        (kind === "invoices" && admin && mergePermission
                          ? 1
                          : 0)
                      }
                      className="h-28 text-center text-muted-foreground"
                    >
                      暂无记录
                    </TableCell>
                  </TableRow>
                )}
                {data.items.map((row) =>
                  "invoice_no" in row ? (
                    <TableRow key={row.id}>
                      {admin && mergePermission && (
                        <TableCell>
                          {row.invoice_kind === "manual" &&
                            row.status === "unpaid" && (
                              <Checkbox
                                aria-label={`选择账单 ${row.invoice_no}`}
                                checked={!!selected[row.id]}
                                onCheckedChange={(value) =>
                                  toggleInvoice(row, value === true)
                                }
                              />
                            )}
                        </TableCell>
                      )}
                      <TableCell>
                        <Link
                          className="font-mono text-xs text-primary hover:underline break-all whitespace-normal"
                          href={`${base}/invoices/${row.id}`}
                        >
                          {row.invoice_no}
                        </Link>
                      </TableCell>
                      {admin && <TableCell>{row.customer_name}</TableCell>}
                      <TableCell>
                        {row.order_id ? (
                          <OrderLink
                            admin={admin}
                            id={row.order_id}
                            label={row.order_no ?? `#${row.order_id}`}
                          />
                        ) : (
                          "-"
                        )}
                      </TableCell>
                      <TableCell className="money">
                        {money(row.amount_cents)}
                      </TableCell>
                      <TableCell>{time(row.due_at)}</TableCell>
                      <TableCell>
                        <Status value={row.status} />
                      </TableCell>
                      <TableCell>
                        <InvoiceActions
                          key={`${row.id}:${row.status}`}
                          invoice={row}
                          admin={admin}
                          refresh={refresh}
                        />
                      </TableCell>
                    </TableRow>
                  ) : (
                    <TableRow key={row.id}>
                      <TableCell>
                        <Link
                          className="font-mono text-xs text-primary hover:underline break-all whitespace-normal"
                          href={`/console/orders/${row.id}`}
                        >
                          {row.order_no}
                        </Link>
                      </TableCell>
                      <TableCell className="money">
                        {money(row.total_cents)}
                      </TableCell>
                      <TableCell>{time(row.created_at)}</TableCell>
                      <TableCell>
                        <Status value={row.status} />
                      </TableCell>
                      <TableCell>
                        <OrderCancel
                          key={`${row.id}:${row.status}`}
                          order={row}
                          refresh={refresh}
                        />
                      </TableCell>
                    </TableRow>
                  ),
                )}
              </TableBody>
            </Table>
          </div>
          <Dialog
            open={mergeOpen}
            onOpenChange={(value) => {
              if (!mergeBusy) setMergeOpen(value);
            }}
          >
            <DialogContent>
              <DialogHeader>
                <DialogTitle>合并人工账单</DialogTitle>
              </DialogHeader>
              <p className="text-sm text-muted-foreground">
                将生成一张新账单，原账单会作废并保留历史记录。
              </p>
              <div className="max-h-56 overflow-auto border rounded-md divide-y">
                {selections.map((item) => (
                  <div
                    key={item.invoice_id}
                    className="flex items-center justify-between gap-3 px-3 py-2 text-sm"
                  >
                    <span className="font-mono break-all">
                      {item.invoice_no}
                    </span>
                    <span className="money shrink-0">
                      {money(item.amount_cents)}
                    </span>
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
                <p
                  role="alert"
                  className="text-sm text-destructive break-words"
                >
                  {mergeError}
                </p>
              )}
              <DialogFooter>
                <Button
                  variant="outline"
                  disabled={mergeBusy}
                  onClick={() => setMergeOpen(false)}
                >
                  返回
                </Button>
                <Button
                  disabled={mergeBusy || !mergeReady}
                  onClick={() => void mergeInvoices()}
                >
                  <GitMerge />
                  {mergeBusy ? "合并中" : "确认合并"}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
          <div className="flex flex-wrap items-center justify-between gap-3 mt-5 text-sm">
            {admin && <div className="flex flex-wrap items-center gap-3">{mergePermission && <Button variant="outline" size="sm" disabled={!mergeReady} onClick={() => { setMergeError(""); setMergeOpen(true); }}><GitMerge />合并账单{selections.length ? ` (${selections.length})` : ""}</Button>}<span className="text-xs text-muted-foreground">本页金额 <span className="money">{money(data.items.reduce((sum, item) => sum + ("amount_cents" in item ? item.amount_cents : item.total_cents), 0))}</span></span></div>}
            <span>
              {data.total} 个{kind === "orders" ? "订单" : "账单"}
            </span>
            <div className="flex gap-2">
              <Button
                variant="outline"
                disabled={offset === 0}
                onClick={() => setOffset((v) => Math.max(0, v - 20))}
              >
                <ArrowLeft />
                上一页
              </Button>
              <Button
                variant="outline"
                disabled={offset + 20 >= data.total}
                onClick={() => setOffset((v) => v + 20)}
              >
                下一页
                <ArrowRight />
              </Button>
            </div>
          </div>
        </>
      )}
    </Workspace>
  );
}

function useOrderLink(admin: boolean) {
  const allowed = useAdminPermission("orders.view");
  return !admin || allowed;
}
function OrderLink({
  admin,
  id,
  label,
}: {
  admin: boolean;
  id: number;
  label: string;
}) {
  const allowed = useOrderLink(admin);
  return allowed ? (
    <Link
      className="text-primary hover:underline break-all"
      href={`${admin ? "/admin" : "/console"}/orders/${id}`}
    >
      {label}
    </Link>
  ) : (
    <span className="break-all">{label}</span>
  );
}
function ServiceLink({
  admin,
  id,
  label,
}: {
  admin: boolean;
  id: number;
  label: string;
}) {
  const allowed = useAdminPermission("services.view");
  return !admin || allowed ? (
    <Link
      className="text-primary hover:underline break-all"
      href={`${admin ? "/admin" : "/console"}/services/${id}`}
    >
      {label}
    </Link>
  ) : (
    <span>{label}</span>
  );
}

function OrderItems({ items }: { items: OrderItem[] }) {
  return (
    <div className="overflow-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>产品与配置</TableHead>
            <TableHead>周期价</TableHead>
            <TableHead>初装费</TableHead>
            <TableHead>数量</TableHead>
            <TableHead>小计</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.length === 0 && (
            <TableRow>
              <TableCell
                colSpan={5}
                className="h-24 text-center text-muted-foreground"
              >
                暂无项目
              </TableCell>
            </TableRow>
          )}
          {items.map((item) => (
            <TableRow key={item.id}>
              <TableCell className="min-w-52 whitespace-normal">
                <p className="font-medium break-all">{item.product_name}</p>
                <PurchaseFieldDetails fields={item.purchase_fields}/>
                <RenewalBenefitDetails terms={item}/>
                <div className="text-xs text-muted-foreground space-y-1 mt-2">
                  {(item.config_snapshot ?? []).map((choice) => (
                    <p key={choice.id} className="break-all">
                      {choice.group_name}：{choice.label}
                      {(choice.quantity ?? 1) > 1
                        ? ` × ${choice.quantity}`
                        : ""}
                    </p>
                  ))}
                  {!item.config_snapshot?.length &&
                    item.config_options?.length > 0 && (
                      <p>历史配置 #{item.config_options.join("、")}</p>
                    )}
                </div>
              </TableCell>
              <TableCell className="money">
                {money(item.price_cents)} /{" "}
                {cycles[item.billing_cycle] ?? item.billing_cycle}
              </TableCell>
              <TableCell className="money">
                {money(item.setup_fee_cents)}
              </TableCell>
              <TableCell>{item.quantity}</TableCell>
              <TableCell className="money">{money(item.total_cents)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function OrderCancel({
  order,
  refresh,
}: {
  order: Pick<Order, "id" | "order_no" | "status">;
  refresh: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const key = useRef("");
  if (order.status !== "pending") return null;
  async function cancel() {
    setBusy(true);
    setError("");
    if (!key.current)
      key.current = `web-order-cancel-${order.id}-${crypto.randomUUID()}`;
    try {
      await api(`/orders/${order.id}/cancel`, {
        method: "POST",
        body: "{}",
        headers: { "Idempotency-Key": key.current },
      });
      setOpen(false);
      refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        <Ban />
        取消订单
      </Button>
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!busy) setOpen(value);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>取消订单</DialogTitle>
          </DialogHeader>
          <p className="text-sm break-all">{order.order_no}</p>
          <p className="text-sm text-muted-foreground">
            此订单的未付款账单将一并取消。
          </p>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setOpen(false)}
            >
              返回
            </Button>
            <Button
              variant="destructive"
              disabled={busy}
              onClick={() => void cancel()}
            >
              <Ban />
              确认取消
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

export function CustomerOrderDetailView({ id }: { id: number }) {
  const { data, error, refresh } = useResource<Order>(`/orders/${id}`, false);
  return (
    <Workspace title="订单详情">
      <Button variant="ghost" className="mb-5" asChild>
        <Link href="/console/orders">
          <ArrowLeft />
          返回订单列表
        </Link>
      </Button>
      {error ? (
        <Failure error={error} retry={refresh} />
      ) : !data ? (
        <Loading />
      ) : (
        <>
          <div className="flex flex-wrap justify-between items-start gap-4 border-b pb-5">
            <div className="min-w-0">
              <h1 className="font-mono text-sm sm:text-xl font-semibold break-all">
                {data.order_no}
              </h1>
              <p className="text-xs text-muted-foreground mt-2">
                {time(data.created_at)}
              </p>
            </div>
            <div className="flex flex-wrap gap-2 items-center">
              <Status
                value={data.status === "pending" ? "unpaid" : data.status}
              />
              <Status value={data.review_status} />
              <OrderCancel
                key={`${data.id}:${data.status}`}
                order={data}
                refresh={refresh}
              />
            </div>
          </div>
          <div className="grid sm:grid-cols-2 gap-6 py-6">
            <div>
              <p className="text-sm text-muted-foreground">订单金额</p>
              <p className="text-2xl font-semibold mt-2 money">
                {money(data.total_cents)}
              </p>
              {data.discount_cents > 0 && (
                <p className="text-sm text-muted-foreground mt-2">
                  优惠 {money(data.discount_cents)}
                  {data.promo_code ? ` · ${data.promo_code}` : ""}
                </p>
              )}
            </div>
            <div>
              <p className="text-sm text-muted-foreground">账单</p>
              {data.invoice_id ? (
                <div className="mt-2 space-y-2">
                  <Link
                    className="text-primary font-mono text-sm break-all hover:underline"
                    href={`/console/invoices/${data.invoice_id}`}
                  >
                    {data.invoice_no}
                  </Link>
                  <div>
                    <Status value={data.invoice_status ?? "unpaid"} />
                  </div>
                </div>
              ) : (
                <p className="mt-2">-</p>
              )}
            </div>
          </div>
          <section className="border-y py-5">
            <h2 className="font-semibold mb-3">订单项目</h2>
            <OrderItems items={data.items} />
          </section>
          <section className="py-5">
            <h2 className="font-semibold mb-3">关联服务</h2>
            <div className="overflow-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>服务</TableHead>
                    <TableHead>标识</TableHead>
                    <TableHead>到期时间</TableHead>
                    <TableHead>状态</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.services.length === 0 && (
                    <TableRow>
                      <TableCell
                        colSpan={4}
                        className="h-24 text-center text-muted-foreground"
                      >
                        暂无服务
                      </TableCell>
                    </TableRow>
                  )}
                  {data.services.map((service) => (
                    <TableRow key={service.id}>
                      <TableCell>
                        <ServiceLink
                          admin={false}
                          id={service.id}
                          label={service.name}
                        />
                      </TableCell>
                      <TableCell className="font-mono text-xs">
                        {service.domain}
                      </TableCell>
                      <TableCell>{time(service.next_due_at)}</TableCell>
                      <TableCell>
                        <Status value={service.status} />
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          </section>
        </>
      )}
    </Workspace>
  );
}

function InvoiceActions({
  invoice,
  admin,
  refresh,
}: {
  invoice: InvoiceActionRow;
  admin: boolean;
  refresh: () => void;
}) {
  const allowed = useAdminPermission("invoices.refund");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState(false);
  const key = useRef<Record<string, string>>({});
  async function act(action: "pay" | "refund") {
    setBusy(true);
    setError("");
    const requestKey = `${action}:${invoice.version}`;
    key.current[requestKey] ??=
      `web-invoice-${action}-${invoice.id}-${crypto.randomUUID()}`;
    try {
      await api(
        `${action === "refund" ? "/admin" : ""}/invoices/${invoice.id}/${action}`,
        {
          method: "POST",
          body: JSON.stringify(
            action === "pay" ? { expected_version: invoice.version } : {},
          ),
          headers: { "Idempotency-Key": key.current[requestKey] },
        },
      );
      setConfirm(false);
      refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  if (admin && (invoice.status !== "paid" || !allowed)) return null;
  if (!admin && invoice.status !== "unpaid") return null;
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
            <Dialog
              open={confirm}
              onOpenChange={(value) => {
                if (!busy) setConfirm(value);
              }}
            >
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>退款至账户余额</DialogTitle>
                </DialogHeader>
                <p className="text-sm break-all">
                  {invoice.invoice_no} · {money(invoice.amount_cents)}
                </p>
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
                  <Button disabled={busy} onClick={() => void act("refund")}>
                    <RotateCcw />
                    确认退款
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>
          </>
        ) : (
          <>
            <Button
              size="sm"
              disabled={busy || invoice.import_review_required || invoice.real_payment_pending}
              onClick={() => void act("pay")}
            >
              <Wallet />
              余额支付
            </Button>
            {invoice.amount_cents > 0 && (
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
        <Link href={`${admin ? "/admin" : "/console"}/invoices`}>
          <ArrowLeft />
          返回账单列表
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
              {data.discount_cents > 0 && (
                <p className="text-sm text-muted-foreground mt-2">
                  优惠 {money(data.discount_cents)}
                  {data.promo_code ? ` · ${data.promo_code}` : ""}
                </p>
              )}
            </div>
            <div>
              <p className="text-sm text-muted-foreground">到期时间</p>
              <p className="mt-2 text-sm">{time(data.due_at)}</p>
            </div>
            <div>
              <p className="text-sm text-muted-foreground">付款时间</p>
              <p className="mt-2 text-sm">{time(data.paid_at)}</p>
            </div>
            <div>
              <p className="text-sm text-muted-foreground">关联订单</p>
              <div className="mt-2 text-sm">
                {data.order_id ? (
                  <OrderLink
                    admin={admin}
                    id={data.order_id}
                    label={data.order_no ?? `#${data.order_id}`}
                  />
                ) : (
                  "-"
                )}
              </div>
            </div>
          </div>
          {admin && (
            <div className="mb-4 grid gap-2 border-b pb-4 text-sm sm:grid-cols-2"><div><span className="text-muted-foreground mr-3">客户</span>{data.customer_name || `#${data.user_id}`}<span className="ml-2 text-muted-foreground break-all">{data.email}</span></div><div><span className="text-muted-foreground mr-3">账单类型</span>{data.invoice_kind === "manual" ? "人工账单" : "自动账单"}</div></div>
          )}
          <div className="pb-5 flex flex-wrap gap-3">
            <InvoiceActions
              key={`${data.id}:${data.version}:${data.status}`}
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
                        {entry.entry_type === "refund"
                          ? "余额退款"
                          : "余额支付"}
                      </TableCell>
                      <TableCell>#{entry.id}</TableCell>
                      <TableCell className="money">
                        {money(Math.abs(entry.delta_cents))}
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
                          <span className="text-muted-foreground">已过期</span>
                        ) : (
                          <Status value={payment.status} />
                        )}
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
        </>
      )}
    </Workspace>
  );
}
