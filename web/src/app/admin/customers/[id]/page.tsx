import { notFound, redirect } from "next/navigation";
import { AdminCustomerDetailView } from "@/components/admin-customer-detail";
import { requireUser } from "@/lib/session";
import { can, adminEntry } from "@/lib/admin-navigation";

export default async function Page({ params }: { params: Promise<{ id: string }> }) {
  const user = await requireUser(true);
  if (!can(user, "customers.view")) redirect(adminEntry(user));
  const { id } = await params;
  const value = Number(id);
  if (!Number.isSafeInteger(value) || value < 1) notFound();
  return <AdminCustomerDetailView customerId={value} />;
}
