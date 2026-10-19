"use client";

import { RenewalBenefitDetails } from "./renewal-benefit";
import { useRef, useState } from "react";
import Link from "next/link";
import { ArrowLeft, Ban } from "lucide-react";
import { api, money, type OptionSnapshot } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { ResourceTable } from "./resource-table";
import { MutationDialog } from "./mutation-dialog";
import { Button } from "./ui/button";
import { TableCell } from "./ui/table";
import { Failure, Loading, Status, cycles } from "./common";
import { Workspace } from "./workspace";
import { PurchaseFieldDetails } from "./purchase-fields";
import type { PurchaseFieldSnapshot } from "@/lib/api";
import { time, ServiceLink } from "./transaction-links";

export type OrderItem = {
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

export type OrderRow = Pick<
  Order,
  "id" | "order_no" | "status" | "total_cents" | "created_at"
>;

export function OrderItems({ items }: { items: OrderItem[] }) {
  return (
    <div className="overflow-auto">
      <ResourceTable
        items={items}
        headers={["产品与配置", "周期价", "初装费", "数量", "小计"]}
        rowKey={(item) => item.id}
        empty="暂无项目"
        className=""
        renderRow={(item) => (
          <>
            <TableCell className="min-w-52 whitespace-normal">
              <p className="font-medium break-all">{item.product_name}</p>
              <PurchaseFieldDetails fields={item.purchase_fields} />
              <RenewalBenefitDetails terms={item} />
              <div className="text-xs text-muted-foreground space-y-1 mt-2">
                {(item.config_snapshot ?? []).map((choice) => (
                  <p key={choice.id} className="break-all">
                    {choice.group_name}：{choice.label}
                    {(choice.quantity ?? 1) !== 1 ? ` × ${choice.quantity}` : ""}
                  </p>
                ))}
                {!item.config_snapshot?.length && item.config_options?.length > 0 && (
                  <p>历史配置 #{item.config_options.join("、")}</p>
                )}
              </div>
            </TableCell>
            <TableCell className="money">
              {money(item.price_cents)} / {cycles[item.billing_cycle] ?? item.billing_cycle}
            </TableCell>
            <TableCell className="money">{money(item.setup_fee_cents)}</TableCell>
            <TableCell>{item.quantity}</TableCell>
            <TableCell className="money">{money(item.total_cents)}</TableCell>
          </>
        )}
      />
    </div>
  );
}

export function OrderCancel({
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
      <MutationDialog
        open={open}
        busy={busy}
        onClose={() => setOpen(false)}
        title={<>取消订单</>}
        confirm={
          <>
            <Ban />
            确认取消
          </>
        }
        onConfirm={() => void cancel()}
        confirmDisabled={busy}
        confirmVariant="destructive"
      >
        <p className="text-sm break-all">{order.order_no}</p>
        <p className="text-sm text-muted-foreground">此订单的未付款账单将一并取消。</p>
        {error && (
          <p role="alert" className="text-sm text-destructive">
            {error}
          </p>
        )}
      </MutationDialog>
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
      <ResourceTable
        items={data.services}
        headers={["服务", "标识", "到期时间", "状态"]}
        rowKey={(service) => service.id}
        empty="暂无服务"
        className=""
        renderRow={(service) => (
          <>
            <TableCell>
              <ServiceLink admin={false} id={service.id} label={service.name} />
            </TableCell>
            <TableCell className="font-mono text-xs">{service.domain}</TableCell>
            <TableCell>{time(service.next_due_at)}</TableCell>
            <TableCell>
              <Status value={service.status} />
            </TableCell>
          </>
        )}
      />
            </div>
          </section>
        </>
      )}
    </Workspace>
  );
}
