"use client";

import type { ReactNode } from "react";
import { date, money } from "@/lib/api";
import type { Application } from "./invoice-applications";
import { ApplicationStatus } from "./invoice-application-status";
import { AdminPageContainer } from "./admin-page-container";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";

function Section({ title, children }: { title: string; children: ReactNode }) {
  return <Card role="region" aria-label={title} className="min-w-0 gap-4 shadow-none"><CardHeader className="px-5"><CardTitle><h2 className="text-sm">{title}</h2></CardTitle></CardHeader><CardContent className="min-w-0 px-5">{children}</CardContent></Card>;
}

function DomainSection({ title, children }: { title: string; children: ReactNode }) {
  return <Card role="region" aria-label={title} className="min-w-0 shadow-none"><CardContent className="min-w-0 px-5">{children}</CardContent></Card>;
}

// Domain slots preserve the original review, adjustment, upload and download protocols.
export function AdminInvoiceApplicationDetail({ data, refresh, snapshot, items, files, adjustments, review, history, dialogs, feedback }: {
  data: Application; refresh: ReactNode; snapshot: ReactNode; items: ReactNode;
  files: ReactNode; adjustments: ReactNode; review: ReactNode; history: ReactNode;
  dialogs: ReactNode; feedback: ReactNode;
}) {
  return <AdminPageContainer pageTitle={`发票申请 #${data.id}`} pageDescription="人工登记开具、作废或红冲结果；未接入税务开票服务。" pageHeaderAction={<><ApplicationStatus status={data.status} />{refresh}</>}>
    <div className="grid min-w-0 gap-5 xl:grid-cols-[minmax(0,1fr)_20rem]">
      <div className="min-w-0 space-y-5">
        <Section title="申请资料快照">{snapshot}</Section>
        <DomainSection title="账单项目">{items}</DomainSection>
        {history && <DomainSection title="审核记录">{history}</DomainSection>}
        {files && <DomainSection title="电子发票文件">{files}</DomainSection>}
        {!!data.adjustments?.length && <Section title="调整记录"><div className="space-y-4 text-sm">{data.adjustments.map(item => <div key={item.id} className="space-y-1 break-words"><p className="font-medium">{item.action === "void" ? "作废" : "红冲"} · {date(item.created_at)}</p>{item.document_number && <p className="break-all">红字发票编号：{item.document_number}</p>}<p className="whitespace-pre-wrap">{item.note}</p></div>)}</div></Section>}
      </div>
      <aside className="min-w-0 space-y-5" aria-label="发票申请摘要">
        <Section title="申请摘要"><p className="text-xs text-muted-foreground">申请金额</p><p className="money mt-2 text-2xl font-semibold">{money(data.amount_cents)}</p><dl className="mt-5 space-y-4 text-sm"><div><dt className="text-xs text-muted-foreground">发票抬头</dt><dd className="mt-1 break-words">{data.title || data.profile_snapshot?.title || "未提供抬头"}</dd></div><div><dt className="text-xs text-muted-foreground">客户</dt><dd className="mt-1 break-words">{data.customer_name || `#${data.user_id}`}<p className="mt-1 break-all text-xs text-muted-foreground">{data.customer_email}</p></dd></div><div><dt className="text-xs text-muted-foreground">申请时间</dt><dd className="mt-1">{date(data.created_at)}</dd></div></dl></Section>
        {review && <Section title="审核操作">{review}</Section>}
        {adjustments && <DomainSection title="登记税务调整">{adjustments}</DomainSection>}
        {feedback}
      </aside>
    </div>
    {dialogs}
  </AdminPageContainer>;
}
