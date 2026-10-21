"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Plus } from "lucide-react";
import { Button } from "./ui/button";
import { useAdminPermission } from "./workspace";

export function CustomerBusinessCreateActions({ customerId, kind }: { customerId: number; kind: "billing" | "tickets" }) {
  const search = useSearchParams();
  const orders = useAdminPermission("orders.manage");
  const invoices = useAdminPermission("invoices.manage");
  const viewInvoices = useAdminPermission("invoices.view");
  const tickets = useAdminPermission("tickets.manage");
  const returnTo = `/admin/customers/${customerId}${search.size ? `?${search.toString()}` : ""}`;
  const query = new URLSearchParams({ customer_id: String(customerId), return_to: returnTo }).toString();
  return <div className="flex flex-wrap gap-2">
    {kind === "billing" && orders && <Button asChild variant="outline" size="sm"><Link href={`/admin/orders/new?${query}`}><Plus size={14} />代下单</Link></Button>}
    {kind === "billing" && invoices && viewInvoices && <Button asChild variant="outline" size="sm"><Link href={`/admin/invoices/new?${query}`}><Plus size={14} />创建人工账单</Link></Button>}
    {kind === "tickets" && tickets && <Button asChild variant="outline" size="sm"><Link href={`/admin/tickets/new?${query}`}><Plus size={14} />代建工单</Link></Button>}
  </div>;
}
