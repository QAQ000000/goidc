"use client";

import type { ReactNode } from "react";
import Link from "next/link";
import { date, money, type PurchaseFieldSnapshot } from "@/lib/api";
import type { Row } from "./admin-data-lists";
import { AdminPageContainer } from "./admin-page-container";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { ResourceTable } from "./resource-table";
import { Status, cycles } from "./common";
import { PermissionGate } from "./workspace";
import { PurchaseDiscountDetails, PurchaseDiscountSummary, type PurchasePricingLine } from "./purchase-discount-details";
import { PurchaseFieldDetails } from "./purchase-fields";
import { RenewalBenefitDetails } from "./renewal-benefit";
import { OrderRefundImpact, orderRefundHold } from "./external-refund-order";

function Section({ title, children }: { title: string; children: ReactNode }) {
  return <Card role="region" aria-label={title} className="min-w-0 gap-4 shadow-none"><CardHeader className="px-5"><CardTitle><h2 className="text-sm">{title}</h2></CardTitle></CardHeader><CardContent className="min-w-0 px-5">{children}</CardContent></Card>;
}

function Configuration({ item }: { item: Row }) {
  const snapshot = Array.isArray(item.config_snapshot) ? item.config_snapshot as Row[] : [];
  return <div className="mt-2 space-y-1 text-xs text-muted-foreground">{snapshot.map(option => <p className="whitespace-normal break-all" key={String(option.id)}>{String(option.group_name)}：{String(option.label)}（{String(option.value)}）{Number(option.quantity ?? 1) !== 1 ? ` × ${option.quantity}` : ""} · 周期增减 {money(Number(option.price_delta_cents))} · 初装增减 {money(Number(option.setup_delta_cents))}</p>)}{snapshot.length === 0 && Array.isArray(item.config_options) && item.config_options.length > 0 && <p>历史配置编号：{item.config_options.join("、")}；未保留名称快照。</p>}</div>;
}

// Domain components provide authorized actions and the original notes form.
export function AdminOrderDetailContent({ data, actions, links, notes }: { data: Row; actions: ReactNode; links: ReactNode; notes: ReactNode }) {
  const hold = orderRefundHold(data.external_refund_order_hold);
  return <AdminPageContainer pageTitle={String(data.order_no)} pageDescription={`客户：${String(data.display_name)} · 创建于 ${date(String(data.created_at))}`} pageHeaderAction={actions}>
    {hold && <OrderRefundImpact reversal={hold} />}
    <div className="grid min-w-0 gap-5 xl:grid-cols-[minmax(0,1fr)_20rem]">
      <aside className="min-w-0 space-y-5 xl:col-start-2 xl:row-start-1" aria-label="订单摘要">
        <Section title="订单摘要"><dl className="space-y-4 text-sm">
          <div><dt className="text-xs text-muted-foreground">订单净应付</dt><dd className="money mt-2 text-2xl font-semibold">{money(Number(data.total_cents))}</dd></div>
          <div><dt className="text-xs text-muted-foreground">账单</dt><dd className="mt-2 space-y-1 break-all">{data.invoice_id == null ? "尚未生成账单" : <><p>{String(data.invoice_no)}</p><Status value={String(data.invoice_status)} /></>}</dd></div>
          <div><dt className="text-xs text-muted-foreground">客户邮箱</dt><dd className="mt-2 break-all">{String(data.email)}</dd></div>
        </dl><div className="mt-5 flex flex-wrap gap-2">{links}</div></Section>
        <Section title="建单信息"><dl className="space-y-4 text-sm"><div><dt className="text-xs text-muted-foreground">付款偏好</dt><dd className="mt-2 break-words">{String(data.payment_provider || "由客户付款时选择")}</dd></div><div><dt className="text-xs text-muted-foreground">订单通知</dt><dd className="mt-2 break-words">{({ not_requested: "未请求发送", queued: "已加入发送队列", skipped_unverified: "未发送：客户邮箱尚未验证", skipped_channel: "未发送：邮件渠道未启用", skipped_template: "未发送：通知模板未启用" } as Record<string, string>)[String(data.email_status)] ?? String(data.email_status || "-")}</dd></div></dl></Section>
      </aside>
      <div className="min-w-0 space-y-5 xl:col-start-1 xl:row-start-1">
        <Section title="订单项目">
          <PurchaseDiscountSummary discountCents={Number(data.discount_cents ?? 0)} groupDiscountCents={data.group_discount_cents == null ? undefined : Number(data.group_discount_cents)} promoDiscountCents={data.promo_discount_cents == null ? undefined : Number(data.promo_discount_cents)} promoCode={String(data.promo_code ?? "")} />
          <ResourceTable items={data.items as Row[]} rowKey={item => String(item.id)} className="mt-4 min-w-0" ariaLabel="订单项目" empty="暂无订单项目" columns={[
            { id: "product", header: "产品与配置", className: "min-w-48 max-w-80 whitespace-normal", render: item => <>{String(item.product_name)}<Configuration item={item} /><PurchaseDiscountDetails pricing={item.pricing as PurchasePricingLine | undefined} />{item.first_price_override_cents != null && <p className="text-xs text-muted-foreground">首次内部价格：{money(Number(item.first_price_override_cents))}</p>}{item.renewal_price_override_cents != null && <p className="text-xs text-muted-foreground">续费内部价格：{money(Number(item.renewal_price_override_cents))}</p>}<RenewalBenefitDetails terms={{ renewal_discount_percent: Number(item.renewal_discount_percent), renewal_fixed_discount_cents: Number(item.renewal_fixed_discount_cents), renewal_discount_limit: Number(item.renewal_discount_limit), renewal_promo_code: String(item.renewal_promo_code ?? "") }} /><PurchaseFieldDetails fields={Array.isArray(item.purchase_fields) ? item.purchase_fields as PurchaseFieldSnapshot[] : []} /></> },
            { id: "cycle", header: "账期", render: item => cycles[String(item.billing_cycle)] || String(item.billing_cycle) },
            { id: "quantity", header: "数量", render: item => String(item.quantity) },
            { id: "amount", header: "净金额", className: "money", render: item => money(item.pricing ? (item.pricing as PurchasePricingLine).net_cents : Number(item.total_cents)) },
          ]} />
        </Section>
        <Section title="服务实例"><ResourceTable items={data.services as Row[]} rowKey={service => String(service.id)} className="min-w-0" ariaLabel="订单服务实例" empty="暂无服务实例" columns={[
          { id: "service", header: "服务", className: "whitespace-normal", render: service => <PermissionGate permission="services.view"><Link className="text-primary hover:underline" href={`/admin/services/${service.id}`}>{String(service.name)}</Link></PermissionGate> },
          { id: "domain", header: "域名", className: "max-w-60 break-all font-mono text-xs whitespace-normal", render: service => String(service.domain) },
          { id: "status", header: "状态", render: service => <Status value={String(service.status)} /> },
        ]} /></Section>
        <Section title="管理员备注">{notes}</Section>
      </div>
    </div>
  </AdminPageContainer>;
}
