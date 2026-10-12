import { SupplierJobsView } from "@/components/supplier-jobs";
import { requireUser } from "@/lib/session";
import { can, adminEntry } from "@/lib/admin-navigation";
import { redirect } from "next/navigation";

export default async function Page() {
  const user = await requireUser(true);
  if (!can(user, "products.view")) redirect(adminEntry(user));
  return <SupplierJobsView mode="jobs" />;
}
