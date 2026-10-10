import { AdminCustomerResourcesView } from "@/components/admin-customer-resources";
import { requireUser } from "@/lib/session";
import { redirect } from "next/navigation";

export default async function Page() {
  const user = await requireUser(true);
  if (user.role !== "super_admin") redirect("/console/account");
  return <AdminCustomerResourcesView />;
}
