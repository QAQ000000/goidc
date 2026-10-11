import { AdminSalesSettings } from "@/components/admin-sales-settings";
import { requireUser } from "@/lib/session";
import { redirect } from "next/navigation";

export default async function Page() {
  const user = await requireUser(true);
  if (user.role !== "super_admin" && !user.permissions?.includes("sales.view")) redirect("/console/account");
  return <AdminSalesSettings />;
}
