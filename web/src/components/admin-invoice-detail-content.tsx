"use client";

import type { ReactNode } from "react";
import { money } from "@/lib/api";
import type { Invoice } from "./transactions";
import { AdminPageContainer } from "./admin-page-container";
import { invoiceKindLabel } from "./admin-invoice-results";
import { PurchaseDiscountSummary } from "./purchase-discount-details";
import { time, OrderLink } from "./transaction-links";
import { Status } from "./common";
import { Card, CardHeader, CardTitle, CardContent } from "./ui/card";

function Section({ title, children }: { title: string; children: ReactNode }) {
  return <Card role="region" aria-label={title} className="min-w-0 gap-4 shadow-none"><CardHeader className="px-5"><CardTitle><h2 className="text-sm">{title}</h2></CardTitle></CardHeader><CardContent className="min-w-0 px-5">{children}</CardContent></Card>;
}

// Domain components retain all payment, refund and result-recovery behavior.
export function AdminInvoiceDetailContent({ data, refresh, actions, items, payments, exceptions, refunds }: {
  data: Invoice;
  refresh: ReactNode;
  actions: ReactNode;
  items: ReactNode;
  payments: ReactNode;
  exceptions: ReactNode;
  refunds: ReactNode;
}) {
  return <AdminPageContainer pageTitle={data.invoice_no} pageDescription={`创建于 ${time(data.created_at)} · ${invoiceKindLabel(data.invoice_kind)}`} pageHeaderAction={<><Status value={data.status} />{refresh}</>}>
    <div className="grid min-w-0 gap-5 xl:grid-cols-[minmax(0,1fr)_20rem]">
      <aside className="min-w-0 space-y-5 xl:col-start-2 xl:row-start-1" aria-label="账单摘要">
        <Section title="账单摘要"><p className="text-xs text-muted-foreground">应收金额</p><p className="money mt-2 text-2xl font-semibold">{money(data.amount_cents)}</p><PurchaseDiscountSummary discountCents={data.discount_cents} groupDiscountCents={data.group_discount_cents} promoDiscountCents={data.promo_discount_cents} promoCode={data.promo_code} />
          <dl className="mt-5 space-y-4 text-sm"><div><dt className="text-xs text-muted-foreground">客户</dt><dd className="mt-1 break-words">{data.customer_name || `#${data.user_id}`}<p className="mt-1 break-all text-xs text-muted-foreground">{data.email}</p></dd></div><div><dt className="text-xs text-muted-foreground">到期时间</dt><dd className="mt-1">{time(data.due_at)}</dd></div><div><dt className="text-xs text-muted-foreground">付款时间</dt><dd className="mt-1">{time(data.paid_at)}</dd></div><div><dt className="text-xs text-muted-foreground">关联订单</dt><dd className="mt-1 break-all">{data.order_id ? <OrderLink admin id={data.order_id} label={data.order_no ?? `#${data.order_id}`} /> : "无关联订单"}</dd></div></dl>
        </Section>
        <Section title="账单操作"><div className="flex flex-wrap gap-3">{actions}</div></Section>
      </aside>
      <div className="min-w-0 space-y-5 xl:col-start-1 xl:row-start-1"><Section title="账单项目">{items}</Section><Section title="付款记录">{payments}</Section>{exceptions}{refunds}</div>
    </div>
  </AdminPageContainer>;
}
