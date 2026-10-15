import { redirect } from "next/navigation";
import { requireUser } from "@/lib/session";
import { adminEntry } from "@/lib/admin-navigation";
import { AdminSalesSettlements } from "@/components/admin-sales-settlements";

export default async function Page() {
  const user = await requireUser(true);
  if (user.role !== "super_admin" && !user.permissions?.includes("sales.view")) redirect(adminEntry(user));
  return <AdminSalesSettlements />;
}
