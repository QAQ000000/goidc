"use client";
import { ContactsPanel } from "./admin-customer-detail";
import { AccountVerification } from "./account-verification";
import Link from "next/link";
import { Table, TableHeader, TableHead, TableBody, TableRow, TableCell } from "@/components/ui/table";
import { date, money, type User } from "@/lib/api";
import { useResource } from "@/hooks/use-resource";
import { Failure, Loading } from "./common";
import { Workspace } from "./workspace";
import { CustomerTopup } from "./customer-topup";
import { PageHeading } from "./resource-page";
import { AccountSecurity } from "./account-security";

type Account = {
  phone?: string;
  phone_verified?: boolean;
  email_verified?: boolean;
  balance_cents: number;
  ledger: {
    id: number;
    entry_type: string;
    reference_type?: string;
    reference_id?: number;
    delta_cents: number;
    balance_after: number;
    note: string;
    created_at: string;
  }[];
};

export function AccountView() {
  const { data, error, refresh } = useResource<Account>("/account");
  const me = useResource<User>("/me");
  return (
    <Workspace title="账户余额">
      <PageHeading title="账户余额" description="余额流水与可用金额" />
      {error ? <Failure error={error} retry={refresh} /> : !data ? <Loading /> : (
        <>
          <section className="data-surface p-6 mb-6">
            <p className="text-sm text-muted-foreground">可用余额</p>
            <strong className="text-3xl money block mt-2">{money(data.balance_cents)}</strong>
          </section>
          <CustomerTopup />
          {me.error ? <Failure error={me.error} retry={me.refresh} /> : !me.data ? <Loading /> : <AccountVerification email={me.data.email} phone={data.phone} phoneVerified={data.phone_verified} emailVerified={data.email_verified} onVerified={refresh} />}
          <AccountSecurity />
          <ContactsPanel />
          <div className="data-surface overflow-auto">
            <Table>
              <TableHeader><TableRow><TableHead>时间</TableHead><TableHead>类型</TableHead><TableHead>变动</TableHead><TableHead>余额</TableHead><TableHead>备注</TableHead></TableRow></TableHeader>
              <TableBody>
                {data.ledger.length === 0 ? <TableRow><TableCell colSpan={5} className="h-28 text-center text-muted-foreground">暂无流水</TableCell></TableRow> : data.ledger.map((entry) => (
                  <TableRow key={entry.id}>
                    <TableCell>{date(entry.created_at)}</TableCell>
                    <TableCell>{entry.entry_type === "payment" ? "账单支付" : entry.entry_type === "refund" ? "退款" : entry.reference_type === "gateway_topup" || entry.entry_type === "online_topup" ? "在线充值入账" : entry.entry_type === "topup" ? "管理员充值" : "余额调整"}</TableCell>
                    <TableCell className={entry.delta_cents < 0 ? "text-destructive money" : "text-emerald-700 money"}>{entry.delta_cents > 0 ? "+" : ""}{money(entry.delta_cents)}</TableCell>
                    <TableCell className="money">{money(entry.balance_after)}</TableCell>
                    <TableCell>{entry.note || "-"}{entry.reference_type === "gateway_topup" && entry.reference_id && <Link className="block text-primary hover:underline" href={`/console/invoices/${entry.reference_id}?from=topup`}>查看充值账单</Link>}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </>
      )}
    </Workspace>
  );
}
